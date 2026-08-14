package output

import (
	"encoding/csv"
	"io"

	"github.com/otama-jaccy/sumiq/internal/mask"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// renderCSV は encoding/csv で書く。0 行でもヘッダ行だけは書く（ADR-0004 §6）。
//
// NULL は空フィールドにする。CSV は null と空文字列を区別できない
// （ADR-0004 §3）。区別が要るなら json を使う、という制約を受け入れる。
func renderCSV(out io.Writer, res *redash.Result) error {
	w := csv.NewWriter(out)

	if err := w.Write(columnNames(res.Columns)); err != nil {
		return err
	}

	record := make([]string, len(res.Columns))
	for _, row := range res.Rows {
		for i := range record {
			v := cellAt(row, i)
			record[i] = ""
			if v != nil {
				record[i] = mask.RenderValue(v)
			}
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}
