package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/otama-jaccy/sumiq/internal/redash"
)

// dataSourceColumns は `data-sources list` の出力列（この順）。
//
// 呼び出し側で書き換えられないよう、参照ではなく毎回新しいスライスを返す
// （パッケージレベルの可変変数を避ける）。
func dataSourceColumns() []string {
	return []string{"id", "name", "type", "paused"}
}

// RenderDataSources は ds を format で out に書く。
//
// Render（redash.Result + mask.Summary 前提）とは独立した formatter で、
// mask.Summary を必要としない。データソース一覧はマスク対象の
// ユーザーデータではないため、stderr へのマスクサマリも書かない
// （Issue #33 決定3）。
func RenderDataSources(out io.Writer, format Format, ds []redash.DataSource, tty bool) error {
	switch format {
	case Table:
		if err := renderDataSourcesTable(out, ds, tty); err != nil {
			return fmt.Errorf("output: table の書き出しに失敗しました: %w", err)
		}
	case JSON:
		if err := renderDataSourcesJSON(out, ds); err != nil {
			return fmt.Errorf("output: json の書き出しに失敗しました: %w", err)
		}
	case CSV:
		if err := renderDataSourcesCSV(out, ds); err != nil {
			return fmt.Errorf("output: csv の書き出しに失敗しました: %w", err)
		}
	default:
		return fmt.Errorf("output: 扱えない format です: %q", format)
	}
	return nil
}

// renderDataSourcesTable は text/tabwriter で表を書く。0 件なら何も書かない
// （ADR-0004 §6）。
func renderDataSourcesTable(out io.Writer, ds []redash.DataSource, tty bool) error {
	if len(ds) == 0 {
		return nil
	}

	tw := newTabwriter(out, tty)

	if _, err := fmt.Fprintln(tw, strings.Join(dataSourceColumns(), "\t")); err != nil {
		return err
	}
	for _, d := range ds {
		row := []string{
			strconv.Itoa(d.ID),
			escapeForTable(d.Name),
			escapeForTable(d.Type),
			strconv.FormatBool(d.Paused),
		}
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// renderDataSourcesJSON はオブジェクトの配列として書く。0 件なら "[]"。
func renderDataSourcesJSON(out io.Writer, ds []redash.DataSource) error {
	if ds == nil {
		ds = []redash.DataSource{}
	}
	b, err := json.Marshal(ds)
	if err != nil {
		return err
	}
	_, err = out.Write(append(b, '\n'))
	return err
}

// renderDataSourcesCSV は encoding/csv で書く。0 件でもヘッダ行だけは書く
// （ADR-0004 §6）。
func renderDataSourcesCSV(out io.Writer, ds []redash.DataSource) error {
	w := csv.NewWriter(out)

	if err := w.Write(dataSourceColumns()); err != nil {
		return err
	}
	for _, d := range ds {
		record := []string{
			strconv.Itoa(d.ID),
			d.Name,
			d.Type,
			strconv.FormatBool(d.Paused),
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}
