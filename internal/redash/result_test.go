package redash

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestResultPreservesColumnOrder は列順が columns の順になることを見る。
//
// rows は列名をキーにしたオブジェクトで返る。JSON のオブジェクトは順序を
// 持たないため、本文に書かれた順や map の反復順に引きずられてはいけない。
// 本文はわざと columns と逆順に並べてある。
func TestResultPreservesColumnOrder(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(
		strings.Join([]string{
			col("id", "integer"),
			col("email", "string"),
			col("name", "string"),
			col("created_at", "datetime"),
		}, ","),
		`{"created_at":"2026-08-14","name":"a","email":"a@example.com","id":1},`+
			`{"name":"b","id":2,"created_at":"2026-08-13","email":"b@example.com"}`))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantCols := []Column{
		{Name: "id", Type: "integer"},
		{Name: "email", Type: "string"},
		{Name: "name", Type: "string"},
		{Name: "created_at", Type: "datetime"},
	}
	if !reflect.DeepEqual(res.Columns, wantCols) {
		t.Fatalf("Columns = %#v, want %#v", res.Columns, wantCols)
	}

	want := [][]string{
		{"1", "a@example.com", "a", "2026-08-14"},
		{"2", "b@example.com", "b", "2026-08-13"},
	}
	for i, wantRow := range want {
		if len(res.Rows[i]) != len(wantCols) {
			t.Fatalf("Rows[%d] の長さ = %d, want %d", i, len(res.Rows[i]), len(wantCols))
		}
		for j, wantVal := range wantRow {
			if got := fmt.Sprint(res.Rows[i][j]); got != wantVal {
				t.Errorf("Rows[%d][%d] (%s) = %v, want %v", i, j, wantCols[j].Name, got, wantVal)
			}
		}
	}
}

// TestResultKeepsLargeIntegers は大きい整数が丸まらないことを見る。
//
// json.Decoder.UseNumber を外すとこのテストは落ちる。any に入る数値が
// float64 になり、2^53 を超える ID の下の桁が静かに変わる。
func TestResultKeepsLargeIntegers(t *testing.T) {
	const bigID = "9007199254740993" // 2^53 + 1。float64 では表現できない

	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(
		col("user_id", "integer"),
		`{"user_id":`+bigID+`}`))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	v := res.Rows[0][0]
	num, ok := v.(json.Number)
	if !ok {
		t.Fatalf("値の型 = %T (%v), want json.Number", v, v)
	}
	if num.String() != bigID {
		t.Errorf("値 = %s, want %s (桁が落ちています)", num, bigID)
	}
}

// TestResultValueTypes は各型の値がそのまま取れることを見る。
func TestResultValueTypes(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(
		strings.Join([]string{
			col("n", "integer"),
			col("f", "float"),
			col("s", "string"),
			col("b", "boolean"),
			col("nul", "string"),
		}, ","),
		`{"n":42,"f":1.5,"s":"x","b":true,"nul":null}`))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	row := res.Rows[0]
	if got := fmt.Sprint(row[0]); got != "42" {
		t.Errorf("integer = %v, want 42", got)
	}
	if got := fmt.Sprint(row[1]); got != "1.5" {
		t.Errorf("float = %v, want 1.5", got)
	}
	if row[2] != "x" {
		t.Errorf("string = %v, want x", row[2])
	}
	if row[3] != true {
		t.Errorf("boolean = %v, want true", row[3])
	}
	if row[4] != nil {
		t.Errorf("null = %v, want nil", row[4])
	}
}

// TestResultDropsUndeclaredColumns は columns に無いキーを出力に混ぜないことを見る。
//
// マスク（#4）は列名で対象を決める。宣言されていない列がそのまま流れると、
// どのルールにも照合されないまま出力に出る。落とす側に倒す。
func TestResultDropsUndeclaredColumns(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(
		col("id", "integer"),
		`{"id":1,"secret_token":"leaked"}`))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Columns) != 1 {
		t.Fatalf("Columns の数 = %d, want 1", len(res.Columns))
	}
	if len(res.Rows[0]) != 1 {
		t.Fatalf("Rows[0] の長さ = %d, want 1", len(res.Rows[0]))
	}
	for _, v := range res.Rows[0] {
		if v == "leaked" {
			t.Error("columns に無い列の値が出力に含まれています")
		}
	}
}

// TestResultMissingValueIsNull は行に欠けている列を NULL 扱いにすることを見る。
func TestResultMissingValueIsNull(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(
		col("id", "integer")+","+col("email", "string"),
		`{"id":1}`))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows[0]) != 2 {
		t.Fatalf("Rows[0] の長さ = %d, want 2", len(res.Rows[0]))
	}
	if res.Rows[0][1] != nil {
		t.Errorf("欠けている列 = %v, want nil", res.Rows[0][1])
	}
}

// TestResultEmpty は0件の結果が空の Result になることを見る。
func TestResultEmpty(t *testing.T) {
	f := defaultFake(t)
	f.result = respond(http.StatusOK, resultBody(col("id", "integer"), ""))
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Columns) != 1 {
		t.Errorf("Columns の数 = %d, want 1", len(res.Columns))
	}
	if len(res.Rows) != 0 {
		t.Errorf("Rows の数 = %d, want 0", len(res.Rows))
	}
}

// TestResultRejectsBadShapes は列順を確定できない応答を落とすことを見る。
func TestResultRejectsBadShapes(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantIn string
	}{
		{
			// 射影すると全ての値が消える。空の結果として返すと、行が
			// 無かったのか捨てたのか区別できない。
			name:   "columns が無いのに rows がある",
			body:   resultBody("", `{"id":1}`),
			wantIn: "columns",
		},
		{
			// Redash はサーバ側で列名を振り直すため通常は起きない。
			// 起きたら同じ値を2列に複製することになる。
			name:   "列名が重複している",
			body:   resultBody(col("id", "integer")+","+col("id", "string"), `{"id":1}`),
			wantIn: "同じ列名",
		},
		{
			name:   "列名が空",
			body:   resultBody(col("", "integer"), `{"x":1}`),
			wantIn: "name",
		},
		{
			name:   "data が無い",
			body:   `{"query_result":{"id":42}}`,
			wantIn: "data",
		},
		{
			name:   "query_result が無い",
			body:   `{"job":{"id":"job-1"}}`,
			wantIn: "query_result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := defaultFake(t)
			f.result = respond(http.StatusOK, tt.body)
			c := start(t, f, nil)

			_, err := c.Execute(context.Background(), testQuery())
			if err == nil {
				t.Fatal("エラーになりませんでした")
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("エラー文に %q が含まれていません: %v", tt.wantIn, err)
			}
		})
	}
}
