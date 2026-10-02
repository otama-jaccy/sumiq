package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/output"
)

// saveFake は ad-hoc 実行と保存クエリの API を受けるモック。
type saveFake struct {
	// jobBody は POST /api/query_results の応答。空なら即完了を返す。
	jobBody string
	// existing は GET /api/queries/my が返すクエリ1件。空なら一覧も空。
	existing string
	// createStatus は POST /api/queries のステータス。0 なら 200。
	createStatus int

	mu         sync.Mutex
	saveCalls  []string
	createBody []byte
}

func (f *saveFake) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query_results", func(w http.ResponseWriter, r *http.Request) {
		body := f.jobBody
		if body == "" {
			body = `{"job":{"id":"job-1","status":3,"error":"","query_result_id":1}}`
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("/api/query_results/1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"query_result":{"id":1,"data":{"columns":[%s],"rows":[{"id":1,"email":"a@example.com"}]}}}`,
			col("id", "integer")+","+col("email", "string"))
	})
	mux.HandleFunc("/api/queries/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.URL.Path != "/api/queries/my" {
			t.Errorf("想定していない保存系リクエスト: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		count := 0
		if f.existing != "" {
			count = 1
		}
		fmt.Fprintf(w, `{"count":%d,"page":1,"page_size":250,"results":[%s]}`, count, f.existing)
	})
	mux.HandleFunc("/api/queries", func(w http.ResponseWriter, r *http.Request) {
		body := f.record(r)
		f.mu.Lock()
		f.createBody = body
		f.mu.Unlock()
		if f.createStatus != 0 {
			w.WriteHeader(f.createStatus)
			fmt.Fprint(w, `{"message":"You don't have permission to edit this data source"}`)
			return
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		fmt.Fprint(w, savedQueryJSON(55, fmt.Sprint(req["name"]), fmt.Sprint(req["query"])))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *saveFake) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalls = append(f.saveCalls, r.Method+" "+r.URL.Path)
	return body
}

func (f *saveFake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.saveCalls...)
}

func savedQueryJSON(id int, name, sql string) string {
	b, _ := json.Marshal(map[string]any{
		"id": id, "name": name, "query": sql, "data_source_id": 3, "is_draft": true, "tags": []string{"sumiq"},
		// runSaveQuery の設定は auto_limit: true。
		"options": map[string]any{"apply_auto_limit": true},
	})
	return string(b)
}

// wantSavedName は受け入れ条件どおりのキーを、実装とは独立に組み立てる。
func wantSavedName(dsID int, sql string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\n%s", dsID, sql))
	return "sumiq: " + hex.EncodeToString(sum[:])[:12]
}

const saveSQL = "SELECT id, email FROM users"

func runSaveQuery(t *testing.T, f *saveFake, save bool, sql string) (srv *httptest.Server, out, errW string, err error) {
	t.Helper()
	srv = f.start(t)
	dir := t.TempDir()
	writeConfig(t, dir, "sumiq.yaml", baseConfig(baseConfigOpts{endpoint: srv.URL, maxRows: 1000, autoLimit: true}))
	deps, outBuf, errBuf := newTestDeps(dir)
	err = Query(context.Background(), deps, QueryParams{
		DataSource: "analytics",
		Format:     output.JSON,
		SQL:        sql,
		Save:       save,
	})
	return srv, outBuf.String(), errBuf.String(), err
}

func TestQuery_WithoutSaveSendsNoSaveRequest(t *testing.T) {
	f := &saveFake{}
	_, _, errW, err := runSaveQuery(t, f, false, saveSQL)
	if err != nil {
		t.Fatalf("Query() 失敗: %v", err)
	}
	if got := f.calls(); len(got) != 0 {
		t.Errorf("--save 無しで保存系リクエストが飛びました: %v", got)
	}
	if strings.Contains(errW, "Saved:") {
		t.Errorf("--save 無しで Saved 行が出ています: %s", errW)
	}
}

func TestQuery_SaveCreatesAndPrintsURLOnStderr(t *testing.T) {
	f := &saveFake{}
	srv, out, errW, err := runSaveQuery(t, f, true, saveSQL)
	if err != nil {
		t.Fatalf("Query() 失敗: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(f.createBody, &req); err != nil {
		t.Fatalf("作成リクエストが JSON ではありません: %v", err)
	}
	if got, want := req["name"], wantSavedName(3, saveSQL); got != want {
		t.Errorf("保存クエリの名前 = %v, want %v", got, want)
	}
	if got := fmt.Sprint(req["options"]); got != "map[apply_auto_limit:true]" {
		t.Errorf("ad-hoc 実行と同じ apply_auto_limit を渡していません: %s", got)
	}

	url := srv.URL + "/queries/55"
	if !strings.Contains(errW, "Saved: "+url+" (draft)\n") {
		t.Errorf("stderr に URL が出ていません: %s", errW)
	}
	if strings.Contains(out, "/queries/") {
		t.Errorf("stdout に URL が混ざっています: %s", out)
	}
	if !strings.Contains(out, `"email":"****"`) {
		t.Errorf("結果がマスクされて出ていません: %s", out)
	}
}

func TestQuery_SaveReusesExisting(t *testing.T) {
	f := &saveFake{existing: savedQueryJSON(9, wantSavedName(3, saveSQL), saveSQL)}
	srv, _, errW, err := runSaveQuery(t, f, true, saveSQL)
	if err != nil {
		t.Fatalf("Query() 失敗: %v", err)
	}
	if !strings.Contains(errW, "Saved: "+srv.URL+"/queries/9 (draft, 既存を再利用)\n") {
		t.Errorf("再利用の旨が出ていません: %s", errW)
	}
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "POST ") {
			t.Errorf("再利用したのに作成しています: %v", f.calls())
		}
	}
}

func TestQuery_SaveSkippedWhenExecuteFails(t *testing.T) {
	f := &saveFake{jobBody: `{"job":{"id":"job-1","status":4,"error":"syntax error","query_result_id":null}}`}
	_, _, _, err := runSaveQuery(t, f, true, saveSQL)
	if err == nil {
		t.Fatal("実行に失敗したのにエラーになりませんでした")
	}
	if got := f.calls(); len(got) != 0 {
		t.Errorf("実行に失敗した SQL を保存しようとしました: %v", got)
	}
}

func TestQuery_SaveSkippedWhenAliasGuardStops(t *testing.T) {
	f := &saveFake{}
	_, _, _, err := runSaveQuery(t, f, true, "SHOW TABLES")
	if err == nil {
		t.Fatal("alias_guard: strict で止まりませんでした")
	}
	if got := f.calls(); len(got) != 0 {
		t.Errorf("alias_guard で止まった SQL を保存しようとしました: %v", got)
	}
}

func TestQuery_SaveFailureStillOutputsAndFails(t *testing.T) {
	f := &saveFake{createStatus: http.StatusForbidden}
	_, out, errW, err := runSaveQuery(t, f, true, saveSQL)
	if err == nil {
		t.Fatal("保存に失敗したのに成功扱いになりました（終了コードが 0 になる）")
	}
	if !strings.Contains(err.Error(), "保存クエリを作れませんでした") {
		t.Errorf("保存の失敗だと分かる文言になっていません: %v", err)
	}
	if !strings.Contains(out, `"email":"****"`) {
		t.Errorf("保存に失敗してもマスク済みの結果は出力されるべきです: %q", out)
	}
	if !strings.Contains(errW, "Rows: 1") {
		t.Errorf("マスクサマリが出ていません: %s", errW)
	}
	if strings.Contains(errW, "Saved:") {
		t.Errorf("保存に失敗したのに Saved 行が出ています: %s", errW)
	}
}
