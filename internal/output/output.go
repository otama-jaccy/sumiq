// Package output は mask.Apply 済みの結果セットを table / json / csv で書き出す。
//
// 方針の根拠は docs/adr/0004-output-formats.md にある。
//
// # マスク済みであることを前提にする
//
// この package は redash.Result を書き出すだけで、マスクは掛けない。
// drop で消えた列も、redact/hash/partial/null で置き換わった値も、
// 呼び出し側が mask.Engine.Apply を通した後の Result であることを前提にする。
package output

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/otama-jaccy/sumiq/internal/mask"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// Format は出力形式（ADR-0004 §1）。
type Format string

const (
	Table Format = "table"
	JSON  Format = "json"
	CSV   Format = "csv"
)

// noneMarker はマスクサマリで「該当なし」を表す（ADR-0004 §5 の表示例に合わせる）。
const noneMarker = "--"

// Render は res を format で out に、マスクサマリを errW に書く。
//
// res は mask.Engine.Apply が返した後の結果であることを前提にする。drop された
// 列は res に残っていない前提で、この関数は列の除去を行わない。
//
// tty は out が対話端末かどうか。装飾（table の罫線）の有無だけを左右し、
// 形式そのものは変えない（ADR-0004 §2）。呼び出し側が判定して渡す
// （internal/output はプロセスの状態を自分で見ない）。
func Render(out, errW io.Writer, format Format, res *redash.Result, sum mask.Summary, tty bool) error {
	if res == nil {
		return errors.New("output: 結果がありません")
	}

	switch format {
	case Table:
		if err := renderTable(out, res, tty); err != nil {
			return fmt.Errorf("output: table の書き出しに失敗しました: %w", err)
		}
	case JSON:
		if err := renderJSON(out, res); err != nil {
			return fmt.Errorf("output: json の書き出しに失敗しました: %w", err)
		}
	case CSV:
		if err := renderCSV(out, res); err != nil {
			return fmt.Errorf("output: csv の書き出しに失敗しました: %w", err)
		}
	default:
		return fmt.Errorf("output: 扱えない format です: %q", format)
	}

	if err := writeSummary(errW, sum, len(res.Rows)); err != nil {
		return fmt.Errorf("output: マスクサマリの書き出しに失敗しました: %w", err)
	}
	return nil
}

// writeSummary はマスクサマリを stderr 向けに書く（ADR-0004 §5）。
//
//	Masked: email (partial), contact (redact, email 由来)
//	Dropped: --
//	Exempted: n (none, count が email の伝播を止めた)
//	Rows: 342
//
// 該当が無くてもどの行も省略しない。マスクされた列が無いことと、まだ結果を
// 見ていないことは区別できないため、毎回同じ4行を出す。
//
// Exempted も同じ理由で毎回出す。許可関数（propagation_exempt_functions）で
// 伝播が止まったのはマスクの弱化であり、起きなかった回と区別が付かなければ
// 通知の意味が無い。
//
// 別名から伝播したマスクには由来（email 由来）を添える。internal/sqlalias は
// 過剰近似でスコープも潰すため、身に覚えのない列が伏せられたときに原因を
// 辿れるのはここだけになる。
func writeSummary(errW io.Writer, sum mask.Summary, rows int) error {
	_, err := fmt.Fprintf(errW, "Masked: %s\nDropped: %s\nExempted: %s\nRows: %d\n",
		columnList(sum.MaskedKept(), methodDetail),
		columnList(sum.Dropped(), viaDetail),
		columnList(sum.Exempted(), exemptedDetail),
		rows)
	return err
}

// columnList は列を並べる。detail が空を返した列は名前だけを出す。
// 該当する列が無ければ noneMarker を返す。
func columnList(cols []mask.ColumnMask, detail func(mask.ColumnMask) string) string {
	if len(cols) == 0 {
		return noneMarker
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.Name
		if d := detail(c); d != "" {
			parts[i] += " (" + d + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// methodDetail は方法と、伝播で決まったならその由来を返す。
func methodDetail(c mask.ColumnMask) string {
	if c.Via == "" {
		return string(c.Method)
	}
	return string(c.Method) + ", " + viaDetail(c)
}

// viaDetail は伝播元を返す。直接マッチで決まったなら空。
// drop で消えた列は方法が自明なので、由来だけを添える。
func viaDetail(c mask.ColumnMask) string {
	if c.Via == "" {
		return ""
	}
	return c.Via + " 由来"
}

// exemptedDetail は方法と、どの許可関数がどの列の伝播を止めたかを返す。
func exemptedDetail(c mask.ColumnMask) string {
	stops := make([]string, len(c.Exempted))
	for i, x := range c.Exempted {
		stops[i] = fmt.Sprintf("%s が %s の伝播を止めた", x.Function, x.Column)
	}
	return string(c.Method) + ", " + strings.Join(stops, "、")
}

// columnNames は列名だけを入力順に取り出す。table と csv のヘッダで共有する。
func columnNames(cols []redash.Column) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names
}

// cellAt は行から列 i の値を取る。列数より短い行は欠けた値を NULL と同じ
// 扱いにする（redash.toResult や mask.Apply と同じ方針）。res が
// mask.Engine.Apply の出力である限り実際には起きないが、Render はその前提を
// 越えて任意の *redash.Result を受け取れる以上、越えた入力でも panic せず
// NULL 側に倒す。
func cellAt(row redash.Row, i int) any {
	if i < len(row) {
		return row[i]
	}
	return nil
}
