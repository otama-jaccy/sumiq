package app

import (
	"context"
	"fmt"

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
}

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

	// 列の由来（別名）の解析もネットワークに依存しない。alias_guard: strict
	// （既定）で解析できなければ、Redash に一切リクエストを飛ばさずここで止める。
	analysis := sqlalias.Analyze(p.SQL, resolved.Config.Masking.ExemptFunctionNames())
	if u := analysis.Undetermined(); u != nil {
		if ds.AliasGuard.Strict() {
			return fmt.Errorf("SQL から列の由来を解析できませんでした: %w。"+
				"データソース %q に alias_guard: %s を共有ファイル（%s）で指定すると、"+
				"解析できないクエリでも実行できます（その場合、別名で改名された列に"+
				"マスクは伝播しません）", u, p.DataSource, config.AliasGuardOff, config.SharedFileName)
		}
		if _, err := fmt.Fprintf(deps.Err, "Warning: SQL から列の由来を解析できませんでした（%v）。"+
			"alias_guard: %s のため実行を続けます。別名で改名された列にマスクは伝播しません。\n",
			u, config.AliasGuardOff); err != nil {
			return fmt.Errorf("警告を書き出せませんでした: %w", err)
		}
	}

	// マスクルールの検証はネットワークに依存しない。Redash へのクエリ実行
	// （時間のかかるジョブ投入・ポーリングを伴う）より前に済ませ、設定の
	// 誤りを実行前に検出する。
	engine, err := mask.New(resolved.Config, p.DataSource)
	if err != nil {
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

	res, err := client.Execute(ctx, redash.Query{
		SQL:          p.SQL,
		DataSourceID: ds.ID,
		AutoLimit:    rowguard.EffectiveAutoLimit(resolved.Config.Query, ds),
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

	return output.Render(deps.Out, deps.Err, p.Format, masked, sum, deps.TTY)
}
