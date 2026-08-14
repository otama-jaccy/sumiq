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

	"github.com/otama-jaccy/sumiq/internal/config"
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
//
// drop された列は Masked には出さず、Dropped だけに出す。sum.Masked() は
// method: none 以外すべて（drop を含む）を返すが、Dropped 側で既に
// 名指ししている列を Masked にも重複して出すと、同じ列が2箇所に出る
// ノイズになる。
func writeSummary(errW io.Writer, sum mask.Summary, rows int) error {
	var masked []mask.ColumnMask
	for _, c := range sum.Masked() {
		if c.Method != config.MaskDrop {
			masked = append(masked, c)
		}
	}
	maskedStr := noneMarker
	if len(masked) > 0 {
		parts := make([]string, len(masked))
		for i, c := range masked {
			parts[i] = fmt.Sprintf("%s (%s)", c.Name, c.Method)
		}
		maskedStr = strings.Join(parts, ", ")
	}

	dropped := sum.Dropped()
	droppedStr := noneMarker
	if len(dropped) > 0 {
		droppedStr = strings.Join(dropped, ", ")
	}

	_, err := fmt.Fprintf(errW, "Masked: %s\nDropped: %s\nRows: %d\n", maskedStr, droppedStr, rows)
	return err
}
