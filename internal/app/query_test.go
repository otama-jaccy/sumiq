package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/output"
)

const testAPIKey = "test-api-key-0123456789"

// fakeRedash はジョブ投入と同時に完了を返す最小限のモック。
// query_result_id を先取りで返すため、ジョブのポーリングは発生しない。
func fakeRedash(t *testing.T, columns, rows string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query_results", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"job":{"id":"job-1","status":3,"error":"","query_result_id":1}}`)
	})
	mux.HandleFunc("/api/query_results/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"query_result":{"id":1,"data":{"columns":[%s],"rows":[%s]}}}`, columns, rows)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func col(name, typ string) string {
	return fmt.Sprintf(`{"name":%q,"type":%q}`, name, typ)
}

// writeConfig は dir に name（sumiq.yaml / sumiq.local.yaml）を書く。dir は
// git リポジトリの外（t.TempDir()）である前提で、探索がプロジェクト本体の
// 設定を拾わないようにする。
func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("%s を書き込めません: %v", name, err)
	}
}

// baseConfigOpts は baseConfig でテンプレート化する設定項目。
type baseConfigOpts struct {
	endpoint  string
	maxRows   int
	autoLimit bool
}

func baseConfig(o baseConfigOpts) string {
	return fmt.Sprintf(`
version: 1
redash:
  endpoint: %s
  timeout: 5s
  poll_interval: 10ms
data_sources:
  - name: analytics
    id: 3
query:
  auto_limit: %t
  max_rows: %d
  on_exceed: error
masking:
  default_action: none
  rules:
    - patterns: ["email"]
      method: redact
output:
  format: table
`, o.endpoint, o.autoLimit, o.maxRows)
}

// newTestDeps は dir を設定探索の起点にした Deps と、その Out / Err の
// バッファを返す。Environ は空スライスで固定し、プロセスの実環境変数
// （HOME 等）が結果に混ざらないようにする。
func newTestDeps(dir string) (deps Deps, out, errW *bytes.Buffer) {
	out, errW = &bytes.Buffer{}, &bytes.Buffer{}
	return Deps{
		Out:     out,
		Err:     errW,
		Dir:     dir,
		Environ: []string{"SUMIQ_REDASH_API_KEY=" + testAPIKey},
	}, out, errW
}

func TestQuery_Success(t *testing.T) {
	srv := fakeRedash(t, col("id", "integer")+","+col("email", "string"),
		`{"id":1,"email":"a@example.com"}`)
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1000, autoLimit: true}))
	deps, out, errW := newTestDeps(dir)

	err := Query(context.Background(), deps, QueryParams{
		DataSource: "analytics",
		Format:     output.JSON,
		SQL:        "SELECT id, email FROM users",
	})
	if err != nil {
		t.Fatalf("Query() 失敗: %v", err)
	}

	if got := out.String(); !strings.Contains(got, `"email":"****"`) {
		t.Errorf("email がマスクされていません: %s", got)
	}
	if got := out.String(); !strings.Contains(got, `"id":1`) {
		t.Errorf("id が出力されていません: %s", got)
	}
	if got := errW.String(); !strings.Contains(got, "Masked: email (redact)") {
		t.Errorf("マスクサマリが出ていません: %s", got)
	}
	if got := errW.String(); strings.Contains(got, "Warning:") {
		t.Errorf("共有定義のデータソースで警告が出るべきではありません: %s", got)
	}
}

func TestQuery_LocalDataSourceWarns(t *testing.T) {
	srv := fakeRedash(t, col("id", "integer"), `{"id":1}`)
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1000, autoLimit: true}))
	writeConfig(t, dir, "sumiq.local.yaml", `
version: 1
data_sources:
  - name: my-sandbox
    id: 4
`)
	deps, _, errW := newTestDeps(dir)

	err := Query(context.Background(), deps, QueryParams{
		DataSource: "my-sandbox",
		Format:     output.JSON,
		SQL:        "SELECT id FROM users",
	})
	if err != nil {
		t.Fatalf("Query() 失敗: %v", err)
	}

	const want = "Warning: my-sandbox はローカル定義です。マスク方針はレビューされていません。\n"
	if got := errW.String(); !strings.Contains(got, want) {
		t.Errorf("ローカル定義の警告が出ていません: got %q, want substring %q", got, want)
	}
}

func TestQuery_UnknownDataSource(t *testing.T) {
	srv := fakeRedash(t, col("id", "integer"), `{"id":1}`)
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1000, autoLimit: true}))
	deps, _, _ := newTestDeps(dir)

	err := Query(context.Background(), deps, QueryParams{
		DataSource: "does-not-exist",
		Format:     output.JSON,
		SQL:        "SELECT 1",
	})
	if err == nil {
		t.Fatal("未定義のデータソースでエラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("エラーにデータソース名が含まれていません: %v", err)
	}
}

func TestQuery_RowsExceeded_NoPartialOutput(t *testing.T) {
	srv := fakeRedash(t, col("id", "integer"), `{"id":1},{"id":2}`)
	dir := t.TempDir()
	// auto_limit: true のままだと max_rows が 1000 を超えられない検証に
	// 掛かるため、超過を再現するにはここで auto_limit を落とす。
	writeConfig(t, dir, "sumiq.yaml", baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1, autoLimit: false}))
	deps, out, _ := newTestDeps(dir)

	err := Query(context.Background(), deps, QueryParams{
		DataSource: "analytics",
		Format:     output.JSON,
		SQL:        "SELECT id FROM users",
	})
	if err == nil {
		t.Fatal("行数超過でエラーになりませんでした")
	}
	if got := out.String(); got != "" {
		t.Errorf("エラー時に stdout へ部分出力が書かれています: %q", got)
	}
}

func TestQuery_InvalidMaskRuleFailsBeforeNetworkCall(t *testing.T) {
	// mask.New の検証はネットワークに依存しないため、Redash へのクエリ実行
	// より前に済ませる（/code-review high の指摘）。このテストはリクエストが
	// 来たら即座に失敗するサーバを立て、実際に叩かれていないことを確認する。
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("マスク設定の検証より前に Redash へリクエストが送られました: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg := baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1000, autoLimit: true})
	// method: none に regex: を書くと mask.compileRule が弾く
	// （internal/mask/mask.go の allowlist の穴の説明を参照）。
	cfg = strings.Replace(cfg, `patterns: ["email"]
      method: redact`, `patterns: ["regex:email"]
      method: none`, 1)
	writeConfig(t, dir, "sumiq.yaml", cfg)
	deps, _, _ := newTestDeps(dir)

	err := Query(context.Background(), deps, QueryParams{
		DataSource: "analytics",
		Format:     output.JSON,
		SQL:        "SELECT id, email FROM users",
	})
	if err == nil {
		t.Fatal("不正なマスクルールでエラーになりませんでした")
	}
}
