package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

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

	// 非 TTY では装飾（列区切りの罫線）を落とす。tabwriter.Debug は列の
	// 境界に '|' を挿む標準ライブラリの機能で、タブ区切りという形式自体は
	// 変えずに見た目だけ切り替えられる（ADR-0004 §2）。
	var flags uint
	if tty {
		flags = tabwriter.Debug
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', flags)

	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = c.Name
	}
	if _, err := fmt.Fprintln(tw, strings.Join(header, "\t")); err != nil {
		return err
	}

	for _, row := range res.Rows {
		cells := make([]string, len(res.Columns))
		for i := range cells {
			var v any
			if i < len(row) {
				v = row[i]
			}
			cells[i] = tableCell(v)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}

	return tw.Flush()
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
	return renderScalar(v)
}

// renderScalar は nil 以外の値を文字列にする。table と csv で共有する。
func renderScalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprintf("%v", v)
}
