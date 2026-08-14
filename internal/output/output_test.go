package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/mask"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// result はテスト用に列名と行から結果セットを組み立てる。
func result(cols []string, rows ...[]any) *redash.Result {
	res := &redash.Result{}
	for _, c := range cols {
		res.Columns = append(res.Columns, redash.Column{Name: c, Type: "string"})
	}
	for _, r := range rows {
		res.Rows = append(res.Rows, redash.Row(r))
	}
	return res
}

func TestRenderStdoutHasDataOnly(t *testing.T) {
	res := result([]string{"id", "email"}, []any{"1", "a@example.com"})
	sum := mask.Summary{Columns: []mask.ColumnMask{
		{Name: "id", Method: config.MaskNone},
		{Name: "email", Method: config.MaskPartial},
	}}

	for _, format := range []Format{Table, JSON, CSV} {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, format, res, sum, false); err != nil {
			t.Fatalf("%s: Render: %v", format, err)
		}
		for _, meta := range []string{"Masked:", "Dropped:", "Rows:"} {
			if strings.Contains(out.String(), meta) {
				t.Errorf("%s: stdout にメタ情報 %q が混ざっている: %q", format, meta, out.String())
			}
		}
		if errW.Len() == 0 {
			t.Errorf("%s: stderr にマスクサマリが書かれていない", format)
		}
	}
}

func TestWriteSummary(t *testing.T) {
	tests := map[string]struct {
		sum  mask.Summary
		rows int
		want string
	}{
		"masked and dropped": {
			sum: mask.Summary{Columns: []mask.ColumnMask{
				{Name: "email", Method: config.MaskPartial},
				{Name: "memo", Method: config.MaskRedact},
				{Name: "secret", Method: config.MaskDrop},
			}},
			rows: 342,
			want: "Masked: email (partial), memo (redact)\nDropped: secret\nRows: 342\n",
		},
		"nothing masked": {
			sum:  mask.Summary{Columns: []mask.ColumnMask{{Name: "id", Method: config.MaskNone}}},
			rows: 0,
			want: "Masked: --\nDropped: --\nRows: 0\n",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var errW bytes.Buffer
			if err := writeSummary(&errW, tt.sum, tt.rows); err != nil {
				t.Fatalf("writeSummary: %v", err)
			}
			if got := errW.String(); got != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderEmptyResult(t *testing.T) {
	empty := result([]string{"id", "email"})
	sum := mask.Summary{}

	t.Run("table writes nothing to stdout", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, Table, empty, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want empty", out.String())
		}
		if !strings.Contains(errW.String(), "Rows: 0") {
			t.Errorf("stderr = %q, want it to contain %q", errW.String(), "Rows: 0")
		}
	})

	t.Run("json writes empty array", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, JSON, empty, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "[]" {
			t.Errorf("stdout = %q, want %q", got, "[]")
		}
	})

	t.Run("csv writes header only", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, CSV, empty, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "id,email" {
			t.Errorf("stdout = %q, want %q", got, "id,email")
		}
	})
}

func TestRenderNilResult(t *testing.T) {
	var out, errW bytes.Buffer
	if err := Render(&out, &errW, Table, nil, mask.Summary{}, false); err == nil {
		t.Fatal("Render(nil): エラーを期待したが nil だった")
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	var out, errW bytes.Buffer
	res := result([]string{"id"}, []any{"1"})
	if err := Render(&out, &errW, Format("xml"), res, mask.Summary{}, false); err == nil {
		t.Fatal("Render(未知の format): エラーを期待したが nil だった")
	}
}

func TestJSONPreservesColumnOrderAndTypes(t *testing.T) {
	res := result([]string{"id", "email", "active"}, []any{json.Number("1"), "****@example.com", true})
	var out, errW bytes.Buffer
	if err := Render(&out, &errW, JSON, res, mask.Summary{}, false); err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `[{"id":1,"email":"****@example.com","active":true}]` + "\n"
	if got := out.String(); got != want {
		t.Errorf("json = %q, want %q", got, want)
	}
}

func TestTableDoesNotTruncateLongValues(t *testing.T) {
	long := strings.Repeat("x", 200)
	res := result([]string{"memo"}, []any{long})
	var out, errW bytes.Buffer
	if err := Render(&out, &errW, Table, res, mask.Summary{}, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out.String(), long) {
		t.Errorf("table output は長い値を切り詰めている: %q", out.String())
	}
}

func TestTableDropsDecorationWhenNotTTY(t *testing.T) {
	res := result([]string{"id", "email"}, []any{"1", "a@example.com"})

	var tty, notTTY bytes.Buffer
	if err := renderTable(&tty, res, true); err != nil {
		t.Fatalf("renderTable(tty): %v", err)
	}
	if err := renderTable(&notTTY, res, false); err != nil {
		t.Fatalf("renderTable(!tty): %v", err)
	}

	if !strings.Contains(tty.String(), "|") {
		t.Errorf("tty=true の table に罫線が無い: %q", tty.String())
	}
	if strings.Contains(notTTY.String(), "|") {
		t.Errorf("tty=false の table に罫線が残っている: %q", notTTY.String())
	}

	// 装飾を変えても、値そのもの（タブ区切りの内容）は変わらない。
	stripPipes := strings.ReplaceAll(tty.String(), "|", "")
	if strings.TrimSpace(stripPipes) != strings.TrimSpace(notTTY.String()) {
		// tabwriter のパディング量は Debug の有無で変わりうるため、
		// フィールドの並びだけを比較する。
		normalize := func(s string) []string {
			return strings.Fields(strings.ReplaceAll(s, "|", " "))
		}
		got, want := normalize(tty.String()), normalize(notTTY.String())
		if len(got) != len(want) {
			t.Fatalf("フィールド数が違う: tty=%v, !tty=%v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("フィールド[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	}
}

// TestMaskRepresentationPerFormat は ADR-0004 §3 の表を、実際の
// mask.Engine.Apply の出力を通して形式ごとに確認する。
func TestMaskRepresentationPerFormat(t *testing.T) {
	cfg := config.Config{
		DataSources: []config.DataSource{{Name: "analytics", ID: 1}},
		Masking: config.Masking{
			DefaultAction: config.ActionNone,
			Rules: []config.MaskRule{
				{Patterns: []string{"secret"}, Method: config.MaskDrop},
				{Patterns: []string{"email"}, Method: config.MaskRedact},
				{Patterns: []string{"memo"}, Method: config.MaskNull},
			},
		},
	}
	engine, err := mask.New(cfg, "analytics")
	if err != nil {
		t.Fatalf("mask.New: %v", err)
	}

	raw := result([]string{"id", "email", "memo", "secret"},
		[]any{json.Number("1"), "a@example.com", "note", "topsecret"})
	masked, sum, err := engine.Apply(raw)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	t.Run("table", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, Table, masked, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := out.String()
		if strings.Contains(got, "secret") {
			t.Errorf("drop された列名が table に残っている: %q", got)
		}
		if !strings.Contains(got, "****") {
			t.Errorf("redact の **** が table に無い: %q", got)
		}
		if !strings.Contains(got, "NULL") {
			t.Errorf("null 列が NULL と表示されていない: %q", got)
		}
	})

	t.Run("json", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, JSON, masked, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		want := `[{"id":1,"email":"****","memo":null}]` + "\n"
		if got := out.String(); got != want {
			t.Errorf("json = %q, want %q", got, want)
		}
	})

	t.Run("csv", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, CSV, masked, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		want := "id,email,memo\n1,****,\n"
		if got := out.String(); got != want {
			t.Errorf("csv = %q, want %q", got, want)
		}
	})

	t.Run("summary", func(t *testing.T) {
		var out, errW bytes.Buffer
		if err := Render(&out, &errW, JSON, masked, sum, false); err != nil {
			t.Fatalf("Render: %v", err)
		}
		want := "Masked: email (redact), memo (null)\nDropped: secret\nRows: 1\n"
		if got := errW.String(); got != want {
			t.Errorf("summary = %q, want %q", got, want)
		}
	})
}
