package output

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/otama-jaccy/sumiq/internal/mask"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// renderTable は text/tabwriter で表を書く（ADR-0004 §8）。外部依存は増やさない。
//
// 0 行なら stdout には何も書かない（ADR-0004 §6）。ヘッダだけを書いても
// 「取得したが空だった」のか「まだ何も見ていない」のか区別が付かず、
// 行数は stderr の Rows: 0 だけが伝えればよい。
//
// 長い値は切り詰めない。マスク済みの値を切り詰めると更に誤読を招くため、
// 表が崩れる場合は json か csv を使う、という ADR の判断をそのまま実装する。
func renderTable(out io.Writer, res *redash.Result, tty bool) error {
	if len(res.Rows) == 0 {
		return nil
	}

	tw := newTabwriter(out, tty)

	if _, err := fmt.Fprintln(tw, strings.Join(columnNames(res.Columns), "\t")); err != nil {
		return err
	}

	cells := make([]string, len(res.Columns))
	for _, row := range res.Rows {
		for i := range cells {
			cells[i] = tableCell(cellAt(row, i))
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}

	return tw.Flush()
}

// newTabwriter は table 出力用の tabwriter.Writer を作る。
//
// 非 TTY では装飾（列区切りの罫線）を落とす。tabwriter.Debug は列の
// 境界に '|' を挿む標準ライブラリの機能で、タブ区切りという形式自体は
// 変えずに見た目だけ切り替えられる（ADR-0004 §2）。
func newTabwriter(out io.Writer, tty bool) *tabwriter.Writer {
	var flags uint
	if tty {
		flags = tabwriter.Debug
	}
	return tabwriter.NewWriter(out, 0, 4, 2, ' ', flags)
}

// tableCell は値1つを table のセルの文字列にする。
//
// NULL は "NULL" と書く。mask method: null による置換も、マスクされて
// いない列の本物の NULL も、Go 側ではどちらも nil で区別が付かない
// （internal/mask は値を見て分岐しない）。表現も分けない。
func tableCell(v any) string {
	if v == nil {
		return "NULL"
	}
	return escapeForTable(mask.RenderValue(v))
}

// tableEscaper は tabwriter がセル・行の境界として読む制御文字を、
// 見える形のエスケープ列に変える。
//
// method: none や partial で残った自由記述の値がタブや改行を含むと、
// tabwriter はそれを列・行の区切りと解釈し、以降のセルが隣の行や列に
// ずれる。切り詰めとは違い、情報を失わず表現だけを変えて表の構造を守る。
var tableEscaper = strings.NewReplacer(
	"\t", `\t`,
	"\n", `\n`,
	"\r", `\r`,
	"\f", `\f`,
	"\v", `\v`,
)

func escapeForTable(s string) string {
	return tableEscaper.Replace(s)
}
