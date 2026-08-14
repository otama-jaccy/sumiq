package rowguard

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

func boolPtr(b bool) *bool { return &b }

func TestEffectiveAutoLimit(t *testing.T) {
	tests := []struct {
		name   string
		global *bool
		ds     *bool
		want   bool
	}{
		{name: "未指定は既定 true", global: nil, ds: nil, want: true},
		{name: "グローバルのみ false", global: boolPtr(false), ds: nil, want: false},
		{name: "グローバルのみ true", global: boolPtr(true), ds: nil, want: true},
		{name: "データソースの false がグローバルの true を上書き", global: boolPtr(true), ds: boolPtr(false), want: false},
		{name: "データソースの true がグローバルの false を上書き", global: boolPtr(false), ds: boolPtr(true), want: true},
		{name: "データソースの false が未指定のグローバルを上書き", global: nil, ds: boolPtr(false), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := config.Query{AutoLimit: tt.global}
			ds := config.DataSource{AutoLimit: tt.ds}
			if got := EffectiveAutoLimit(q, ds); got != tt.want {
				t.Errorf("EffectiveAutoLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateQuery(t *testing.T) {
	tests := []struct {
		name      string
		autoLimit bool
		maxRows   int
		wantErr   bool
	}{
		{name: "既定の組み合わせは境界として許容", autoLimit: true, maxRows: 1000, wantErr: false},
		{name: "auto_limit true で 1000 未満は許容", autoLimit: true, maxRows: 1, wantErr: false},
		{name: "auto_limit true で 1000 超過は設定エラー", autoLimit: true, maxRows: 1001, wantErr: true},
		{name: "auto_limit false なら max_rows がいくつでも許容", autoLimit: false, maxRows: 1_000_000, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateQuery(tt.autoLimit, tt.maxRows)
			if tt.wantErr && err == nil {
				t.Fatal("ValidateQuery() エラーを期待しましたが nil でした")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateQuery() 予期しないエラー: %v", err)
			}
		})
	}
}

// TestValidateQueryCatchesRegression は検査そのものを壊すと落ちることを確かめる
// （.claude/rules/go-architecture.md「安全側の検査には、壊して落ちることを
// 確かめたテストを置く」）。しきい値を実装と揃えて書くのではなく、
// ADR-0003 §10 が定める具体的な境界（1000）を直書きして固定する。
func TestValidateQueryCatchesRegression(t *testing.T) {
	if err := ValidateQuery(true, 1000); err != nil {
		t.Fatalf("境界値 1000 は許容されるべきですが失敗しました: %v", err)
	}
	if err := ValidateQuery(true, 1001); err == nil {
		t.Fatal("auto_limit: true かつ max_rows: 1001 は Redash が 1000 で切るため到達不能であり、" +
			"設定エラーになるべきです")
	}
}

func rows(n int) []redash.Row {
	out := make([]redash.Row, n)
	for i := range out {
		out[i] = redash.Row{i}
	}
	return out
}

func testResult(n int) *redash.Result {
	return &redash.Result{
		Columns: []redash.Column{{Name: "id", Type: "integer"}},
		Rows:    rows(n),
	}
}

func TestCheck_WithinLimit(t *testing.T) {
	var errW bytes.Buffer
	res := testResult(3)
	q := config.Query{MaxRows: 3, OnExceed: config.OnExceedError}

	got, err := Check(&errW, res, q)
	if err != nil {
		t.Fatalf("Check() 予期しないエラー: %v", err)
	}
	if len(got.Rows) != 3 {
		t.Errorf("Rows = %d 件, want 3", len(got.Rows))
	}
	if errW.Len() != 0 {
		t.Errorf("上限以下なのに errW に書き込みがあります: %q", errW.String())
	}
}

// TestCheck_ExceedError は on_exceed: error のとき、超過した結果を一切
// 返さないことを見る。呼び出し側はこの nil を output.Render に渡せば
// 安全に倒れる。stdout に部分出力を書かない、という受け入れ条件の核心。
func TestCheck_ExceedError(t *testing.T) {
	var errW bytes.Buffer
	res := testResult(5)
	q := config.Query{MaxRows: 3, OnExceed: config.OnExceedError}

	got, err := Check(&errW, res, q)
	if got != nil {
		t.Errorf("エラー時の Result は nil であるべきですが %#v でした", got)
	}
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("ExceededError を期待しましたが %#v でした", err)
	}
	if exceeded.MaxRows != 3 || exceeded.Got != 5 {
		t.Errorf("ExceededError = %+v, want {MaxRows:3 Got:5}", exceeded)
	}
	if errW.Len() != 0 {
		t.Errorf("on_exceed: error は errW に何も書かないはずですが %q が書かれました", errW.String())
	}
}

// TestCheck_OnExceedDefaultIsError は on_exceed 未指定（空文字列）でも
// error と同じ挙動になることを見る。defaults() が埋めるはずの値だが、
// 呼び出し側が解決前の値を渡した場合の fail-closed を保証する。
func TestCheck_OnExceedDefaultIsError(t *testing.T) {
	var errW bytes.Buffer
	res := testResult(5)
	q := config.Query{MaxRows: 3}

	got, err := Check(&errW, res, q)
	if got != nil {
		t.Errorf("Result は nil であるべきですが %#v でした", got)
	}
	var exceeded *ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("ExceededError を期待しましたが %#v でした", err)
	}
}

func TestCheck_Truncate(t *testing.T) {
	var errW bytes.Buffer
	res := testResult(5)
	q := config.Query{MaxRows: 3, OnExceed: config.OnExceedTruncate}

	got, err := Check(&errW, res, q)
	if err != nil {
		t.Fatalf("Check() 予期しないエラー: %v", err)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("Rows = %d 件, want 3", len(got.Rows))
	}
	for i, row := range got.Rows {
		if row[0] != i {
			t.Errorf("Rows[%d] = %v, 先頭からの3件であるべきです", i, row)
		}
	}
	if !strings.Contains(errW.String(), "切り詰め") {
		t.Errorf("truncate した事実が errW に書かれていません: %q", errW.String())
	}
}

func TestCheck_NilResult(t *testing.T) {
	var errW bytes.Buffer
	if _, err := Check(&errW, nil, config.Query{MaxRows: 10}); err == nil {
		t.Fatal("結果が nil のときエラーを期待しましたが nil でした")
	}
}

func TestCheck_MaxRowsNotSet(t *testing.T) {
	var errW bytes.Buffer
	if _, err := Check(&errW, testResult(1), config.Query{MaxRows: 0}); err == nil {
		t.Fatal("max_rows が未指定のときエラーを期待しましたが nil でした")
	}
}

func TestCheck_UnknownOnExceed(t *testing.T) {
	var errW bytes.Buffer
	q := config.Query{MaxRows: 1, OnExceed: config.OnExceed("bogus")}
	if _, err := Check(&errW, testResult(2), q); err == nil {
		t.Fatal("扱えない on_exceed のときエラーを期待しましたが nil でした")
	}
}
