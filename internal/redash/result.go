package redash

import (
	"encoding/json"
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

type queryResult struct {
	ID   int64            `json:"id"`
	Data *queryResultData `json:"data"`
}

type queryResultData struct {
	Columns []Column `json:"columns"`
	// Rows は列名をキーにしたオブジェクトの配列。JSON のオブジェクトは
	// 順序を持たないため、ここからは列順を復元できない。順序は Columns にしかない。
	Rows []map[string]any `json:"rows"`
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

// decodeQueryResult は GET /api/query_results/{id} の応答本文から queryResult
// を組み立てる。
//
// 応答を1回で丸ごとデコードせず rows を1件ずつ読み、rowLimit が 1 以上の
// ときは rowLimit+1 件を超えた分を保持せずに読み捨てる。auto_limit: false で
// 巨大なテーブルを引いたとき、rows を全部メモリに載せてから max_rows を
// 判定すると、その判定に辿り着く前に OOM で落ちる（Issue #16）。+1 件を
// 保持するのは、呼び出し側（rowguard.Check）が既存の
// len(Result.Rows) > max_rows という比較のまま超過を判定できるようにするため。
// rowLimit <= 0 は上限なし（RowLimit を指定しない呼び出し側との後方互換）。
//
// rows の要素を読み捨てても、応答そのものの読み取りは最後まで続ける。
// columns と rows が JSON オブジェクトのどちらに先に現れるかは仕様として
// 保証されていない。rows 側で上限に達した時点で読むのをやめて接続を
// 切る案も検討したが、それは「rows が columns より後に来る」という
// 保証のない前提に乗ることになり、その前提が崩れた応答では正しい
// columns に辿り着けないまま「columns が無い」と誤って落とすことになる
// （toResult の columns/rows 不整合チェック）。安全側に倒し、保持しない
// 分もバイト自体は読み切る。応答本文そのものの上限は maxResponseBytes が
// 別に持つ（limits.go）。
func decodeQueryResult(dec *json.Decoder, rowLimit int) (*queryResult, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	var qr *queryResult
	for dec.More() {
		key, err := decodeObjectKey(dec)
		if err != nil {
			return nil, err
		}
		if key != "query_result" {
			if err := discardValue(dec); err != nil {
				return nil, err
			}
			continue
		}
		qr, err = decodeQueryResultObject(dec, rowLimit)
		if err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); err != nil { // 閉じの '}'
		return nil, err
	}
	if qr == nil {
		return nil, &shapeError{msg: "Redash の応答に query_result がありません"}
	}
	return qr, nil
}

// shapeError は JSON の構文としては正しいが、期待した Redash の応答の形と
// 違うことを表す（キーが無い・値の種類が違う等）。json.Decoder 自身が返す
// 構文エラーや型不一致のエラーと区別するための印。fetch はこれを
// 「JSON として読めませんでした」に包まない。包むと、構文として読めている
// のに読めなかったと言う自己矛盾した文言になる。
type shapeError struct{ msg string }

func (e *shapeError) Error() string { return e.msg }

// decodeQueryResultObject は "query_result" キーの値を読む。
func decodeQueryResultObject(dec *json.Decoder, rowLimit int) (*queryResult, error) {
	isObj, err := enterObjectOrNull(dec)
	if err != nil {
		return nil, err
	}
	if !isObj {
		// null 等。ID も Data も取れないが、空の queryResult を返せば
		// toResult() の「data がありません」で正しく落ちる。
		return &queryResult{}, nil
	}

	qr := &queryResult{}
	for dec.More() {
		key, err := decodeObjectKey(dec)
		if err != nil {
			return nil, err
		}
		switch key {
		case "id":
			if err := dec.Decode(&qr.ID); err != nil {
				return nil, err
			}
		case "data":
			data, err := decodeQueryResultData(dec, rowLimit)
			if err != nil {
				return nil, err
			}
			qr.Data = data
		default:
			if err := discardValue(dec); err != nil {
				return nil, err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // 閉じの '}'
		return nil, err
	}
	return qr, nil
}

// decodeQueryResultData は "data" キーの値を読む。
func decodeQueryResultData(dec *json.Decoder, rowLimit int) (*queryResultData, error) {
	isObj, err := enterObjectOrNull(dec)
	if err != nil {
		return nil, err
	}
	if !isObj {
		return nil, nil
	}

	data := &queryResultData{}
	for dec.More() {
		key, err := decodeObjectKey(dec)
		if err != nil {
			return nil, err
		}
		switch key {
		case "columns":
			if err := dec.Decode(&data.Columns); err != nil {
				return nil, err
			}
		case "rows":
			rows, err := decodeRows(dec, rowLimit)
			if err != nil {
				return nil, err
			}
			data.Rows = rows
		default:
			if err := discardValue(dec); err != nil {
				return nil, err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // 閉じの '}'
		return nil, err
	}
	return data, nil
}

// decodeRows は "rows" キーの値（配列）を読む。rowLimit+1 件までは保持し
// （rowLimit <= 0 なら無制限）、それを超えた要素は生バイト列のまま読み捨てる。
// デコードそのものはやめない（decodeQueryResult のコメントを参照）。
//
// 保持しない要素を map[string]any へ完全にデコードすると、ネストした値の
// 分だけ無駄な割り当てを払うことになる。json.RawMessage で受ければ、
// その要素の JSON トークンを読み進めるだけで Go の値は組み立てない。
func decodeRows(dec *json.Decoder, rowLimit int) ([]map[string]any, error) {
	if err := expectDelim(dec, '['); err != nil {
		return nil, err
	}
	unlimited := rowLimit <= 0
	limit := rowLimit + 1
	var rows []map[string]any
	if !unlimited {
		rows = make([]map[string]any, 0, limit)
	}
	for dec.More() {
		if !unlimited && len(rows) >= limit {
			var discard json.RawMessage
			if err := dec.Decode(&discard); err != nil {
				return nil, err
			}
			continue
		}
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if _, err := dec.Token(); err != nil { // 閉じの ']'
		return nil, err
	}
	return rows, nil
}

// enterObjectOrNull は次の値が JSON オブジェクトなら開きの '{' を消費して
// true を返す。null 等それ以外の値なら false を返す（値自体は消費済み）。
func enterObjectOrNull(dec *json.Decoder) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if d, ok := tok.(json.Delim); ok && d == '{' {
		return true, nil
	}
	return false, nil
}

// decodeObjectKey は JSON オブジェクトのキー1つを読む。dec.More() が true を
// 返した直後に呼ぶこと。
func decodeObjectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", &shapeError{msg: fmt.Sprintf("Redash の応答のキーが文字列ではありません: %v", tok)}
	}
	return key, nil
}

// expectDelim は次のトークンが d であることを確認し、消費する。
func expectDelim(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	got, ok := tok.(json.Delim)
	if !ok || got != d {
		return &shapeError{msg: fmt.Sprintf("Redash の応答の形が想定と違います (want %q, got %v)", d, tok)}
	}
	return nil
}

// discardValue は次の1つの JSON 値（キーに対応する値）を読み捨てる。
//
// json.RawMessage で受ける。any で受けると、ネストしたオブジェクト・配列を
// 使う道が無いのに map・slice・interface へ組み立ててしまう。
func discardValue(dec *json.Decoder) error {
	var v json.RawMessage
	return dec.Decode(&v)
}
