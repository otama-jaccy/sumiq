package output

import (
	"encoding/csv"
	"io"

	"github.com/otama-jaccy/sumiq/internal/redash"
)

// renderCSV は encoding/csv で書く。0 行でもヘッダ行だけは書く（ADR-0004 §6）。
//
// NULL は空フィールドにする。CSV は null と空文字列を区別できない
// （ADR-0004 §3）。区別が要るなら json を使う、という制約を受け入れる。
func renderCSV(out io.Writer, res *redash.Result) error {
	w := csv.NewWriter(out)

	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = c.Name
	}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, row := range res.Rows {
		record := make([]string, len(res.Columns))
		for i := range record {
			var v any
			if i < len(row) {
				v = row[i]
			}
			if v != nil {
				record[i] = renderScalar(v)
			}
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}
