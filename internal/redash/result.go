package redash

import (
	"fmt"
)

// Column は結果の1列。
type Column struct {
	// Name は列名。マスクのパターンはこの値に対して照合する。
	Name string `json:"name"`
	// Type は Redash が推定した型（integer / float / boolean / string / date / datetime）。
	Type string `json:"type"`
}

// Row は1行分の値。Columns と同じ長さ・同じ順序で並ぶ。
//
// 各要素は encoding/json が返す値だが、数値は float64 ではなく json.Number。
// user_id のような 2^53 を超えうる整数を float64 に通すと桁が落ち、
// 出力もマスク後のハッシュも元の値と対応しなくなる。これは応答を読む
// Client.do が json.Decoder.UseNumber を立てていることで成り立っている。
type Row []any

// Result はクエリ1回分の結果。
type Result struct {
	Columns []Column
	Rows    []Row
}

// queryResultEnvelope は GET /api/query_results/{id} の応答。
type queryResultEnvelope struct {
	QueryResult *queryResult `json:"query_result"`
}

type queryResult struct {
	ID   int64 `json:"id"`
	Data *struct {
		Columns []Column `json:"columns"`
		// Rows は列名をキーにしたオブジェクトの配列。JSON のオブジェクトは
		// 順序を持たないため、ここからは列順を復元できない。順序は Columns にしかない。
		Rows []map[string]any `json:"rows"`
	} `json:"data"`
}

// toResult は応答を Result に落とす。
//
// 列順の保持がこの関数の主な仕事（Issue #3 / #5）。rows のオブジェクトを
// Columns の順に射影して並びを確定させる。
//
// 射影は同時に「出力される列は columns に宣言されたものだけ」を保証する。
// rows 側にだけ現れるキーは落ちる。マスク（#4）は列名で対象を決めるため、
// 宣言されていない列がそのまま出力に混ざる方が危ない。落とす側に倒す。
func (qr *queryResult) toResult() (*Result, error) {
	if qr.Data == nil {
		return nil, fmt.Errorf("Redash の応答に data がありません (結果 %d)", qr.ID)
	}

	cols := qr.Data.Columns
	index := make(map[string]int, len(cols))
	for i, c := range cols {
		if c.Name == "" {
			return nil, fmt.Errorf("Redash の応答の columns[%d] に name がありません (結果 %d)", i, qr.ID)
		}
		// Redash は列名の重複をサーバ側で潰している（query_runner/__init__.py の
		// fetch_columns が name, name1, name2... と振り直す）。重複が来るのは
		// その保証が崩れているときで、そのまま進めると同じ値を2列に複製して
		// 出すことになる。読み手にはそれが起きたと分からない。
		if _, dup := index[c.Name]; dup {
			return nil, fmt.Errorf("Redash の応答に同じ列名が2つあります: %q (結果 %d)",
				clipMessage(c.Name), qr.ID)
		}
		index[c.Name] = i
	}

	// 列が無いのに行がある応答は、射影すると全ての値が消える。
	// 空の結果として返すと、行が無かったのか捨てたのか区別できない。
	if len(cols) == 0 && len(qr.Data.Rows) > 0 {
		return nil, fmt.Errorf("Redash の応答に columns が無いまま rows が %d 行あります (結果 %d)",
			len(qr.Data.Rows), qr.ID)
	}

	rows := make([]Row, 0, len(qr.Data.Rows))
	for _, raw := range qr.Data.Rows {
		row := make(Row, len(cols))
		for name, i := range index {
			// 値が無い列は NULL と同じ（nil のまま）に扱う。Redash は列名と値を
			// zip して行を作るため通常は起きない。
			if v, ok := raw[name]; ok {
				row[i] = v
			}
		}
		rows = append(rows, row)
	}

	return &Result{Columns: cols, Rows: rows}, nil
}
