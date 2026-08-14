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
		name    string
		global  *bool
		ds      *bool
		maxRows int
		wantErr bool
	}{
		{name: "既定の組み合わせは境界として許容", global: boolPtr(true), maxRows: 1000, wantErr: false},
		{name: "auto_limit true で 1000 未満は許容", global: boolPtr(true), maxRows: 1, wantErr: false},
		{name: "auto_limit true で 1000 超過は設定エラー", global: boolPtr(true), maxRows: 1001, wantErr: true},
		{name: "auto_limit false なら max_rows がいくつでも許容", global: boolPtr(false), maxRows: 1_000_000, wantErr: false},
		{
			name:    "データソース単位で false に上書きしていれば許容",
			global:  boolPtr(true),
			ds:      boolPtr(false),
			maxRows: 1_000_000,
			wantErr: false,
		},
		{
			name:    "データソース単位で true に上書きしていれば検証が効く",
			global:  boolPtr(false),
			ds:      boolPtr(true),
			maxRows: 1001,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := config.Query{AutoLimit: tt.global, MaxRows: tt.maxRows}
			ds := config.DataSource{AutoLimit: tt.ds}
			err := ValidateQuery(q, ds)
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
	q := config.Query{AutoLimit: boolPtr(true)}
	ds := config.DataSource{}

	q.MaxRows = 1000
	if err := ValidateQuery(q, ds); err != nil {
		t.Fatalf("境界値 1000 は許容されるべきですが失敗しました: %v", err)
	}

	q.MaxRows = 1001
	if err := ValidateQuery(q, ds); err == nil {
		t.Fatal("auto_limit: true かつ max_rows: 1001 は Redash が 1000 で切るため到達不能であり、" +
			"設定エラーになるべきです")
	}
}

func testResult(n int) *redash.Result {
	rows := make([]redash.Row, n)
	for i := range rows {
		rows[i] = redash.Row{i}
	}
	return &redash.Result{
		Columns: []redash.Column{{Name: "id", Type: "integer"}},
		Rows:    rows,
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name        string
		rows        int
		nilResult   bool
		maxRows     int
		onExceed    config.OnExceed
		wantRows    int // 成功時に期待する行数
		wantErr     bool
		wantExceed  bool // ExceededError を期待する
		wantWarning bool // errW に切り詰めの警告を期待する
	}{
		{name: "上限以下はそのまま通す", rows: 3, maxRows: 3, onExceed: config.OnExceedError, wantRows: 3},
		{
			name:       "on_exceed: error は超過分を一切返さない",
			rows:       5,
			maxRows:    3,
			onExceed:   config.OnExceedError,
			wantErr:    true,
			wantExceed: true,
		},
		{
			// on_exceed 未指定（空文字列）でも error と同じ fail-closed になることを見る。
			// defaults() が埋めるはずの値だが、解決前の値を渡された場合の保険。
			name:       "on_exceed 未指定は error 扱い",
			rows:       5,
			maxRows:    3,
			wantErr:    true,
			wantExceed: true,
		},
		{
			name:        "on_exceed: truncate は max_rows 件に切り詰めて警告する",
			rows:        5,
			maxRows:     3,
			onExceed:    config.OnExceedTruncate,
			wantRows:    3,
			wantWarning: true,
		},
		{name: "結果が nil はエラー", nilResult: true, maxRows: 10, wantErr: true},
		{name: "max_rows 未指定はエラー", rows: 1, maxRows: 0, wantErr: true},
		{name: "扱えない on_exceed はエラー", rows: 2, maxRows: 1, onExceed: config.OnExceed("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errW bytes.Buffer
			var res *redash.Result
			if !tt.nilResult {
				res = testResult(tt.rows)
			}
			q := config.Query{MaxRows: tt.maxRows, OnExceed: tt.onExceed}

			got, err := Check(&errW, res, q)

			if tt.wantErr {
				if err == nil {
					t.Fatal("エラーを期待しましたが nil でした")
				}
				if got != nil {
					t.Errorf("エラー時の Result は nil であるべきですが %#v でした", got)
				}
				if tt.wantExceed {
					var exceeded *ExceededError
					if !errors.As(err, &exceeded) {
						t.Fatalf("ExceededError を期待しましたが %#v でした", err)
					}
					if exceeded.MaxRows != tt.maxRows || exceeded.Got != tt.rows {
						t.Errorf("ExceededError = %+v, want {MaxRows:%d Got:%d}", exceeded, tt.maxRows, tt.rows)
					}
				}
				if errW.Len() != 0 {
					t.Errorf("エラー時は errW に何も書かないはずですが %q が書かれました", errW.String())
				}
				return
			}

			if err != nil {
				t.Fatalf("Check() 予期しないエラー: %v", err)
			}
			if len(got.Rows) != tt.wantRows {
				t.Fatalf("Rows = %d 件, want %d", len(got.Rows), tt.wantRows)
			}
			for i, row := range got.Rows {
				if row[0] != i {
					t.Errorf("Rows[%d] = %v, 先頭からの %d 件であるべきです", i, row, tt.wantRows)
				}
			}
			if tt.wantWarning && !strings.Contains(errW.String(), "切り詰め") {
				t.Errorf("切り詰めた事実が errW に書かれていません: %q", errW.String())
			}
			if !tt.wantWarning && errW.Len() != 0 {
				t.Errorf("警告を期待していないのに errW に書き込みがあります: %q", errW.String())
			}
		})
	}
}
