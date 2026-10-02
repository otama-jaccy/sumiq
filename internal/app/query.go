package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/mask"
	"github.com/otama-jaccy/sumiq/internal/output"
	"github.com/otama-jaccy/sumiq/internal/redash"
	"github.com/otama-jaccy/sumiq/internal/rowguard"
	"github.com/otama-jaccy/sumiq/internal/sqlalias"
)

// QueryParams は Query 1回分の呼び出しパラメータ。コマンドライン引数の詰め替え先。
type QueryParams struct {
	// DataSource は -d/--data-source で指定されたデータソース名。
	DataSource string
	// Format は --format で指定された出力形式。
	Format output.Format
	// ConfigPath は --config で明示指定された設定ファイルのパス。
	ConfigPath string
	// SQL は実行する SQL 本文。
	SQL string
	// Save は --save。true なら結果を出した後に SQL を Redash の保存クエリにする。
	Save bool
}

// savedQueryTag は sumiq が作る保存クエリに付けるタグ。人間が一括で掃除するための目印。
const savedQueryTag = "sumiq"

// Query は設定を解決し、SQL を実行して、マスク済みの結果を出力する。
//
// ローカル定義のデータソースを使う場合は、実行のたびに Deps.Err へ警告を書く
// （ADR-0003 §6）。
func Query(ctx context.Context, deps Deps, p QueryParams) error {
	resolved, err := config.Resolve(config.Options{
		Dir:        deps.Dir,
		Environ:    deps.Environ,
		ConfigPath: p.ConfigPath,
	})
	if err != nil {
		return err
	}
	if err := resolved.Validate(); err != nil {
		return err
	}

	ds, layer, ok := resolved.DataSource(p.DataSource)
	if !ok {
		return fmt.Errorf("データソース %q は設定に定義されていません", p.DataSource)
	}
	if !layer.Reviewed() {
		if _, err := fmt.Fprintf(deps.Err, "Warning: %s はローカル定義です。マスク方針はレビューされていません。\n",
			p.DataSource); err != nil {
			return fmt.Errorf("警告を書き出せませんでした: %w", err)
		}
	}

	if err := rowguard.ValidateQuery(resolved.Config.Query, ds); err != nil {
		return err
	}

	// 列の由来（別名）の解析はネットワークに依存しない。
	analysis := sqlalias.Analyze(p.SQL, resolved.Config.Masking.ExemptFunctionNames())

	// マスクルールの検証もネットワークに依存しない。Redash へのクエリ実行
	// （時間のかかるジョブ投入・ポーリングを伴う）より前に済ませ、設定の
	// 誤りを実行前に検出する。
	engine, err := mask.New(resolved.Config, p.DataSource)
	if err != nil {
		return err
	}

	// alias_guard: strict（既定）で由来を辿れなければ、Redash に一切
	// リクエストを飛ばさずここで止める。判定不能をどう扱うかは
	// internal/mask が決める（app では判定しない）。
	if err := engine.PrecheckAlias(analysis); err != nil {
		return err
	}

	apiKey, err := resolved.APIKey()
	if err != nil {
		return err
	}
	client, err := redash.New(redash.Options{
		Endpoint:     resolved.Config.Redash.Endpoint,
		APIKey:       apiKey,
		Timeout:      resolved.Config.Redash.Timeout.Duration(),
		PollInterval: resolved.Config.Redash.PollInterval.Duration(),
	})
	if err != nil {
		return err
	}

	autoLimit := rowguard.EffectiveAutoLimit(resolved.Config.Query, ds)
	res, err := client.Execute(ctx, redash.Query{
		SQL:          p.SQL,
		DataSourceID: ds.ID,
		AutoLimit:    autoLimit,
		// max_rows を fetch の取得段階にも渡す。rowguard.Check の判定は
		// 取得済みの結果に対して行われるため、それだけでは auto_limit: false
		// で巨大な結果を引いたときに判定へ辿り着く前の OOM を防げない
		// （ADR-0003 §10、Issue #16）。
		RowLimit: resolved.Config.Query.MaxRows,
	})
	if err != nil {
		return err
	}

	res, err = rowguard.Check(deps.Err, res, resolved.Config.Query, ds)
	if err != nil {
		return err
	}

	masked, sum, err := engine.Apply(res, analysis)
	if err != nil {
		return err
	}

	// ここに来るのは alias_guard: off のときだけ（strict は上で止まる）。
	// 伝播が効いていないことは出力を見ても分からないため、毎回警告を出す。
	if u := sum.AliasUndetermined; u != nil {
		if _, err := fmt.Fprintf(deps.Err, "Warning: SQL から結果列の由来を辿れませんでした（%v）。"+
			"alias_guard: %s のため実行を続けました。別名で改名された列にマスクは伝播していません。\n",
			u, config.AliasGuardOff); err != nil {
			return fmt.Errorf("警告を書き出せませんでした: %w", err)
		}
	}

	if err := output.Render(deps.Out, deps.Err, p.Format, masked, sum, deps.TTY); err != nil {
		return err
	}
	if !p.Save {
		return nil
	}

	// 保存は結果を出した後に行う。保存に失敗してもマスク済みの結果は渡し、
	// 失敗はエラーとして返して終了コードを非 0 にする。
	saved, err := client.Save(ctx, redash.SaveQuery{
		Name:         savedQueryName(ds.ID, p.SQL),
		SQL:          p.SQL,
		DataSourceID: ds.ID,
		Tag:          savedQueryTag,
		AutoLimit:    autoLimit,
	})
	if err != nil {
		return fmt.Errorf("結果は出力しましたが、Redash に保存クエリを作れませんでした: %w", err)
	}
	return output.WriteSaved(deps.Err, saved)
}

// savedQueryName は保存クエリの名前を返す。名前が重複検出のキーを兼ねる。
//
// SQL の空白は正規化しない。正規化を誤って別の SQL を同一視するより、
// 重複した保存クエリができる方が安全。
func savedQueryName(dataSourceID int, sql string) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(dataSourceID) + "\n" + sql))
	return "sumiq: " + hex.EncodeToString(sum[:])[:12]
}
