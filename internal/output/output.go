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
//	Masked: email (partial), memo (redact)
//	Dropped: --
//	Rows: 342
//
// 行数が 0 件でも省略しない。マスクされた列が無いことと、まだ結果を見て
// いないことは区別できないため、毎回同じ3行を出す。
func writeSummary(errW io.Writer, sum mask.Summary, rows int) error {
	maskedStr := noneMarker
	if masked := sum.MaskedKept(); len(masked) > 0 {
		parts := make([]string, len(masked))
		for i, c := range masked {
			parts[i] = fmt.Sprintf("%s (%s)", c.Name, c.Method)
		}
		maskedStr = strings.Join(parts, ", ")
	}

	droppedStr := noneMarker
	if dropped := sum.Dropped(); len(dropped) > 0 {
		droppedStr = strings.Join(dropped, ", ")
	}

	_, err := fmt.Fprintf(errW, "Masked: %s\nDropped: %s\nRows: %d\n", maskedStr, droppedStr, rows)
	return err
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
