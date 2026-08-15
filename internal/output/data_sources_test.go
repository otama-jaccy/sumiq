package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/redash"
)

var testDataSources = []redash.DataSource{
	{ID: 1, Name: "analytics", Type: "pg", Paused: false},
	{ID: 2, Name: "legacy", Type: "mysql", Paused: true},
}

func TestRenderDataSourcesTable(t *testing.T) {
	var out bytes.Buffer
	if err := RenderDataSources(&out, Table, testDataSources, false); err != nil {
		t.Fatalf("RenderDataSources: %v", err)
	}
	got := out.String()
	for _, want := range []string{"id", "name", "type", "paused", "analytics", "legacy", "true", "false"} {
		if !strings.Contains(got, want) {
			t.Errorf("table 出力に %q が含まれていません: %q", want, got)
		}
	}
}

func TestRenderDataSourcesJSON(t *testing.T) {
	var out bytes.Buffer
	if err := RenderDataSources(&out, JSON, testDataSources, false); err != nil {
		t.Fatalf("RenderDataSources: %v", err)
	}

	var got []redash.DataSource
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("出力を JSON として読めません: %v (%q)", err, out.String())
	}
	if len(got) != 2 || got[0] != testDataSources[0] || got[1] != testDataSources[1] {
		t.Errorf("RenderDataSources JSON = %#v, want %#v", got, testDataSources)
	}
}

func TestRenderDataSourcesCSV(t *testing.T) {
	var out bytes.Buffer
	if err := RenderDataSources(&out, CSV, testDataSources, false); err != nil {
		t.Fatalf("RenderDataSources: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("CSV の行数 = %d, want 3 (ヘッダ+2件): %q", len(lines), out.String())
	}
	if lines[0] != "id,name,type,paused" {
		t.Errorf("CSV のヘッダ = %q", lines[0])
	}
	if lines[1] != "1,analytics,pg,false" {
		t.Errorf("CSV の1行目 = %q", lines[1])
	}
}

// TestRenderDataSourcesEmpty は空結果の扱いを見る（ADR-0004 §6）。
func TestRenderDataSourcesEmpty(t *testing.T) {
	var tableOut, jsonOut, csvOut bytes.Buffer

	if err := RenderDataSources(&tableOut, Table, nil, false); err != nil {
		t.Fatalf("RenderDataSources(table): %v", err)
	}
	if tableOut.Len() != 0 {
		t.Errorf("table: 0 件で stdout に何か書かれています: %q", tableOut.String())
	}

	if err := RenderDataSources(&jsonOut, JSON, nil, false); err != nil {
		t.Fatalf("RenderDataSources(json): %v", err)
	}
	if got := strings.TrimSpace(jsonOut.String()); got != "[]" {
		t.Errorf("json: 0 件の出力 = %q, want []", got)
	}

	if err := RenderDataSources(&csvOut, CSV, nil, false); err != nil {
		t.Fatalf("RenderDataSources(csv): %v", err)
	}
	if got := strings.TrimSpace(csvOut.String()); got != "id,name,type,paused" {
		t.Errorf("csv: 0 件の出力 = %q, want ヘッダ行のみ", got)
	}
}
