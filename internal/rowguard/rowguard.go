// Package rowguard は auto_limit と max_rows という役割の異なる2つの行数制御を扱う。
//
// 方針の根拠は docs/adr/0003-config-file-design.md §10 にある。
//
// auto_limit は Redash に apply_auto_limit を渡す最適化であり、効けば転送量を
// 減らせるが、CTE を含むクエリや非 SQL のデータソースでは黙って効かない。
// max_rows は取得後にこのパッケージが必ず行う判定であり、こちらが実際の防御線になる。
package rowguard

import (
	"errors"
	"fmt"
	"io"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// maxAutoLimitRows は Redash が auto_limit で必ず切る行数。
//
// BaseQueryRunner.limit_query が " LIMIT 1000" に固定しており、API から
// 数値を渡す経路が無い（ADR-0003 §10 のコンテキストにある redash 本体の抜粋）。
const maxAutoLimitRows = 1000

// EffectiveAutoLimit は ds の上書きを踏まえた、このデータソースに使う
// auto_limit の値を返す。
//
// データソース単位の指定があればそれを優先する。Oracle / SQL Server のように
// apply_auto_limit でクエリが壊れるデータソースを個別に false にするための
// 上書き経路であり（ADR-0003 §10）、無指定ならグローバルの Query.AutoLimit
// （既定 true）を使う。
func EffectiveAutoLimit(q config.Query, ds config.DataSource) bool {
	if ds.AutoLimit != nil {
		return *ds.AutoLimit
	}
	return effectiveBool(q.AutoLimit, true)
}

// effectiveBool はゼロ値と「未指定」を区別する *bool を、既定値付きで解く。
func effectiveBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// ValidateQuery は、実際に使う auto_limit の値と max_rows の組み合わせが
// 到達可能かを検証する。
//
// autoLimit は EffectiveAutoLimit で解決した後の値を渡すこと。auto_limit は
// データソース単位で上書きされるため、グローバルの config.Query.AutoLimit を
// そのまま見ると、上書きで false になっているデータソースにまで
// 到達不能エラーを出してしまう。
//
// auto_limit が効くと Redash は結果を maxAutoLimitRows 件で切るため、
// max_rows がそれを超えていても超過判定に到達しない。
func ValidateQuery(autoLimit bool, maxRows int) error {
	if !autoLimit {
		return nil
	}
	if maxRows > maxAutoLimitRows {
		return fmt.Errorf("query.auto_limit: true のとき query.max_rows は %d を超えて指定できません: %d。"+
			"Redash は auto_limit が効くと結果を %d 件で切るため、それを超える max_rows には到達できません。"+
			"max_rows を %d 以下にするか、auto_limit を false にしてください",
			maxAutoLimitRows, maxRows, maxAutoLimitRows, maxAutoLimitRows)
	}
	return nil
}

// ExceededError は結果が max_rows を超え、on_exceed: error のため
// 出力を中止したことを表す。
type ExceededError struct {
	MaxRows int
	Got     int
}

func (e *ExceededError) Error() string {
	return fmt.Sprintf("結果が %d 行あり、query.max_rows (%d) を超えています。"+
		"query.on_exceed: error のため出力を中止しました", e.Got, e.MaxRows)
}

// Check は取得済みの結果を max_rows で判定する。
//
// res.Rows 全体の長さを見てから判定する。ストリーミングはしない —
// 何行あるか自体が安全装置の判定材料であり、書き出しながら数える実装は
// on_exceed: error のときに部分出力を書いてしまう。
//
// **呼び出し側はエラーを確認してから出力を書き始めること。** この関数自体は
// stdout に何も書かない。エラーを返したときの戻り値の *redash.Result は nil
// であり、そのまま output.Render に渡せば安全に倒れる。
//
// on_exceed: truncate で切り詰めた場合は、その事実を errW に書く。
// on_exceed: error で超過した場合は errW に何も書かず ExceededError を返す。
func Check(errW io.Writer, res *redash.Result, q config.Query) (*redash.Result, error) {
	if res == nil {
		return nil, errors.New("rowguard: 判定対象の結果がありません")
	}
	if q.MaxRows <= 0 {
		return nil, fmt.Errorf("rowguard: query.max_rows が指定されていません: %d", q.MaxRows)
	}
	if len(res.Rows) <= q.MaxRows {
		return res, nil
	}

	switch q.OnExceed {
	case config.OnExceedTruncate:
		out := &redash.Result{Columns: res.Columns, Rows: res.Rows[:q.MaxRows]}
		if _, err := fmt.Fprintf(errW, "Warning: 結果が %d 行あり、max_rows (%d) 件に切り詰めました\n",
			len(res.Rows), q.MaxRows); err != nil {
			return nil, fmt.Errorf("rowguard: 切り詰めの警告を書き出せませんでした: %w", err)
		}
		return out, nil
	case config.OnExceedError, "":
		return nil, &ExceededError{MaxRows: q.MaxRows, Got: len(res.Rows)}
	default:
		return nil, fmt.Errorf("rowguard: query.on_exceed: %q は扱えません", q.OnExceed)
	}
}
