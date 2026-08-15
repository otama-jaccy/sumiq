package redash

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// このファイルは Issue #16（fetch が結果を全部メモリに載せる前に打ち切れる
// ようにする）に対する検査を置く。
//
// bad shapes・列順・大きい整数の保持といった decodeQueryResult の一般的な
// 正しさは result_test.go がカバーする。ここには RowLimit 固有の振る舞い
// （キャップ・境界・バイト上限との組み合わせ）だけを置く。

// manyRows は n 行分の {"id":i} を "," で連結した rows の本文を作る。
func manyRows(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"id":%d}`, i)
	}
	return strings.Join(parts, ",")
}

// queryWithRowLimit は RowLimit を指定した testQuery() を返す。
func queryWithRowLimit(rowLimit int) Query {
	q := testQuery()
	q.RowLimit = rowLimit
	return q
}

// TestFetchRowLimitCapsRows は rowLimit を超える結果でも rowLimit+1 件しか
// 保持しないことを見る。
//
// +1 件保持するのは、呼び出し側（rowguard.Check）が既存の
// len(Result.Rows) > max_rows という比較のまま超過を判定できるようにするため。
func TestFetchRowLimitCapsRows(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), manyRows(50)))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), queryWithRowLimit(3))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 4 {
		t.Fatalf("Rows の件数 = %d, want 4 (rowLimit+1)", len(res.Rows))
	}
	// 保持したのは先頭からの4件であること。
	for i, row := range res.Rows {
		if got := fmt.Sprint(row[0]); got != strconv.Itoa(i) {
			t.Errorf("Rows[%d][0] = %v, want %d", i, row[0], i)
		}
	}
}

// TestFetchRowLimitUnderActualCountKeepsAll は行数が rowLimit 以下なら
// 全件保持することを見る。
func TestFetchRowLimitUnderActualCountKeepsAll(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), manyRows(3)))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), queryWithRowLimit(10))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Errorf("Rows の件数 = %d, want 3", len(res.Rows))
	}
}

// TestFetchRowLimitExactBoundary は実際の行数が rowLimit ちょうどのとき
// 切り詰めが起きない（超過扱いにならない）ことを見る。
func TestFetchRowLimitExactBoundary(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), manyRows(3)))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), queryWithRowLimit(3))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Errorf("Rows の件数 = %d, want 3 (境界ちょうどは切り詰めない)", len(res.Rows))
	}
}

// TestFetchRowLimitZeroIsUnbounded は RowLimit を指定しない（0）呼び出しが
// 今までどおり全件を返すことを見る（後方互換）。
func TestFetchRowLimitZeroIsUnbounded(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), manyRows(20)))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery()) // RowLimit は既定の 0
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 20 {
		t.Errorf("Rows の件数 = %d, want 20", len(res.Rows))
	}
}

// TestFetchRowLimitColumnsOrderIndependent は rows が columns より先に
// 現れても正しく読めることを見る。
//
// JSON オブジェクトのキー順は仕様上保証されない。実装がこの順に依存すると、
// 逆順の応答（あるいは順序を変えるプロキシ）で columns に辿り着けないまま
// 「columns が無い」と誤って落ちる。
func TestFetchRowLimitColumnsOrderIndependent(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, fmt.Sprintf(
		`{"query_result":{"id":42,"data":{"rows":[%s],"columns":[%s]}}}`,
		`{"id":1},{"id":2},{"id":3}`, col("id", "integer")))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), queryWithRowLimit(1))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Columns) != 1 || res.Columns[0].Name != "id" {
		t.Fatalf("Columns = %#v, want [id]", res.Columns)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("Rows の件数 = %d, want 2 (rowLimit+1)", len(res.Rows))
	}
}

// TestFetchShapeErrorsAreNotMislabeledAsJSON は、JSON としては正しいが
// 期待した形と違う応答が「JSON として読めませんでした」に化けないことを見る
// （/code-review の指摘）。
//
// classifyDecodeErr は元々 dec.Decode 1回分の失敗だけを想定していた。
// decodeQueryResult の構造チェック（query_result が無い等）もそこを通すと、
// 構文としては読めているのに読めなかったと言う自己矛盾した文言になり、
// 加えて元々あった (結果 %d) の文脈も失われる。
func TestFetchShapeErrorsAreNotMislabeledAsJSON(t *testing.T) {
	f := defaultFake(t)
	// 有効な JSON だが query_result が無い。
	f.result = respond(http.StatusOK, `{"job":{"id":"job-1"}}`)
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if strings.Contains(err.Error(), "JSON として読めませんでした") {
		t.Errorf("構文としては正しい応答なのに JSON エラーだと言っています: %v", err)
	}
	if !strings.Contains(err.Error(), "結果 42") {
		t.Errorf("結果 ID の文脈が失われています: %v", err)
	}
}

// TestResponseTooLargeIsDetected は maxResponseBytes を超える応答が
// *http.MaxBytesError になることを見る。
func TestResponseTooLargeIsDetected(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), manyRows(1000)))
	c := start(t, f, func(o *Options) { o.MaxResponseBytes = 64 })

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("エラーの型 = %T, want *http.MaxBytesError: %v", err, err)
	}
	if tooLarge.Limit != 64 {
		t.Errorf("Limit = %d, want 64", tooLarge.Limit)
	}
}

// TestResponseTooLargeAlsoAppliesToRowLimitPath は RowLimit>0 と組み合わせても
// maxResponseBytes が効くことを見る。
func TestResponseTooLargeAlsoAppliesToRowLimitPath(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), manyRows(1000)))
	c := start(t, f, func(o *Options) { o.MaxResponseBytes = 64 })

	_, err := c.Execute(context.Background(), queryWithRowLimit(10))
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("エラーの型 = %T, want *http.MaxBytesError: %v", err, err)
	}
}
