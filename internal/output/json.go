package output

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/otama-jaccy/sumiq/internal/redash"
)

// renderJSON はオブジェクトの配列として書く（ADR-0004 §4）。0 行なら "[]"。
//
// 列順は encoding/json の map ではなく、行ごとに手組みした JSON オブジェクトで
// 保持する。map で経由すると encoding/json がキーをソートし、SQL の結果順が
// 失われる。
func renderJSON(out io.Writer, res *redash.Result) error {
	// 列名のキーは列数ぶんだけ確定していて全行で同じなので、行ごとに
	// marshal し直さず1回だけ作って使い回す。
	keys := make([]json.RawMessage, len(res.Columns))
	for i, c := range res.Columns {
		key, err := json.Marshal(c.Name)
		if err != nil {
			return err
		}
		keys[i] = key
	}

	// make で確保するため rows が 0 件でも nil にならず、json.Marshal は
	// "[]" を返す。nil スライスだと "null" になってしまう。
	rows := make([]json.RawMessage, len(res.Rows))
	for i, row := range res.Rows {
		raw, err := marshalRow(keys, row)
		if err != nil {
			return err
		}
		rows[i] = raw
	}

	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	_, err = out.Write(append(b, '\n'))
	return err
}

// marshalRow は1行を列の並び順どおりの JSON オブジェクトにする。
//
// 値は encoding/json にそのまま渡す。json.Number は数値として、nil は
// null として、マスク済みの文字列は引用符付きの文字列として出る
// （型は internal/mask が確定させている。ここでは判断しない）。
func marshalRow(keys []json.RawMessage, row redash.Row) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(key)
		buf.WriteByte(':')

		val, err := json.Marshal(cellAt(row, i))
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
