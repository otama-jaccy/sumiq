package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/output"
)

// fakeDataSourcesRedash は GET /api/data_sources だけに応答するモック。
func fakeDataSourcesRedash(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data_sources", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestListDataSources_Success(t *testing.T) {
	srv := fakeDataSourcesRedash(t, `[{"id":1,"name":"analytics","type":"pg","paused":false}]`)
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1000, autoLimit: true}))
	deps, out, _ := newTestDeps(dir)

	err := ListDataSources(context.Background(), deps, DataSourcesParams{Format: output.JSON})
	if err != nil {
		t.Fatalf("ListDataSources() 失敗: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `"name":"analytics"`) {
		t.Errorf("データソース一覧が出力されていません: %s", got)
	}
}

// TestListDataSources_MissingEndpoint は endpoint / API KEY が無い設定で
// エラーになることを確認する（config.Resolved.Validate() をそのまま使う経路）。
func TestListDataSources_MissingEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", `
version: 1
masking:
  default_action: none
`)
	deps, _, _ := newTestDeps(dir)
	deps.Environ = []string{} // API KEY も含めプロセスの環境を一切継がない

	err := ListDataSources(context.Background(), deps, DataSourcesParams{Format: output.Table})
	if err == nil {
		t.Fatal("endpoint が無いのにエラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "redash.endpoint") {
		t.Errorf("エラーが endpoint 不足を示していません: %v", err)
	}
}

// TestListDataSources_DoesNotRequireLocalDataSources は sumiq.yaml の
// data_sources: セクションを参照・必須にしないことを確認する。
func TestListDataSources_DoesNotRequireLocalDataSources(t *testing.T) {
	srv := fakeDataSourcesRedash(t, `[]`)
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", `
version: 1
redash:
  endpoint: `+srv.URL+`
  timeout: 5s
  poll_interval: 10ms
masking:
  default_action: none
`)
	deps, out, _ := newTestDeps(dir)

	err := ListDataSources(context.Background(), deps, DataSourcesParams{Format: output.JSON})
	if err != nil {
		t.Fatalf("data_sources: が無い設定でエラーになりました: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Errorf("空のデータソース一覧の出力 = %q, want []", got)
	}
}
