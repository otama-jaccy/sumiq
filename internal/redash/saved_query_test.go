package redash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeQueries は /api/queries 系だけを受けるモック。prefix はパス付き endpoint の再現用。
type fakeQueries struct {
	t      *testing.T
	prefix string
	// list は GET /api/queries/my の応答をページ番号ごとに返す。無いページは空。
	list map[string]string
	// create は POST /api/queries の応答。
	create http.HandlerFunc
	// update は POST /api/queries/{id} の応答。
	update http.HandlerFunc

	mu         sync.Mutex
	requests   []string
	listQuery  []string
	createBody []byte
	updateBody []byte
}

func (f *fakeQueries) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.Path, f.prefix)
	if !ok {
		f.t.Errorf("endpoint のパスが付いていません: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+path)
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && path == "/api/queries/my":
		f.mu.Lock()
		f.listQuery = append(f.listQuery, r.URL.RawQuery)
		f.mu.Unlock()
		page, ok := f.list[r.URL.Query().Get("page")]
		if !ok {
			page = `{"count":0,"page":1,"page_size":250,"results":[]}`
		}
		respond(http.StatusOK, page)(w, r)
	case r.Method == http.MethodPost && path == "/api/queries":
		f.mu.Lock()
		f.createBody = body
		f.mu.Unlock()
		f.handlerOr(f.create)(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/queries/"):
		f.mu.Lock()
		f.updateBody = body
		f.mu.Unlock()
		f.handlerOr(f.update)(w, r)
	default:
		f.t.Errorf("想定していないリクエスト: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeQueries) handlerOr(h http.HandlerFunc) http.HandlerFunc {
	if h != nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("応答を用意していないリクエスト: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (f *fakeQueries) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func testSaveQuery() SaveQuery {
	return SaveQuery{Name: "sumiq: 0123456789ab", SQL: "SELECT id FROM users", DataSourceID: 3, Tag: "sumiq"}
}

// queryJSON は serialize_query と同じ形のクエリ1件を組み立てる。
func queryJSON(id int64, name, sql string, dsID int, isDraft bool) string {
	return queryJSONWithAutoLimit(id, name, sql, dsID, isDraft, false)
}

func queryJSONWithAutoLimit(id int64, name, sql string, dsID int, isDraft, autoLimit bool) string {
	b, _ := json.Marshal(map[string]any{
		"id": id, "name": name, "query": sql, "data_source_id": dsID,
		"is_draft": isDraft, "tags": []string{"sumiq"}, "version": 1,
		"options": map[string]any{"apply_auto_limit": autoLimit},
	})
	return string(b)
}

func pageJSON(count int, items ...string) string {
	return fmt.Sprintf(`{"count":%d,"page":1,"page_size":250,"results":[%s]}`, count, strings.Join(items, ","))
}

func TestSave_CreatesDraftWithTag(t *testing.T) {
	q := testSaveQuery()
	q.AutoLimit = true
	f := &fakeQueries{t: t, create: respond(http.StatusOK, queryJSON(123, q.Name, q.SQL, 3, true))}
	c := start(t, f, nil)

	got, err := c.Save(context.Background(), q)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(f.createBody, &body); err != nil {
		t.Fatalf("作成リクエストの本文が JSON ではありません: %v", err)
	}
	want := map[string]any{
		"name": q.Name, "query": q.SQL, "data_source_id": float64(3),
		"tags": []any{"sumiq"}, "is_draft": true,
		"options": map[string]any{"apply_auto_limit": true},
	}
	for k, v := range want {
		if fmt.Sprint(body[k]) != fmt.Sprint(v) {
			t.Errorf("作成リクエストの %s = %v, want %v", k, body[k], v)
		}
	}
	if got.ID != 123 || !got.IsDraft || got.Reused {
		t.Errorf("Save() = %+v", got)
	}
	if !strings.HasSuffix(got.URL, "/queries/123") {
		t.Errorf("URL = %s", got.URL)
	}
	// is_draft: true で返ったなら更新しない。
	for _, p := range f.paths() {
		if strings.HasPrefix(p, "POST /api/queries/") {
			t.Errorf("draft で作れたのに更新しています: %v", f.paths())
		}
	}
}

func TestSave_ListRequestFiltersByTag(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{t: t, create: respond(http.StatusOK, queryJSON(1, q.Name, q.SQL, 3, true))}
	c := start(t, f, nil)

	if _, err := c.Save(context.Background(), q); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(f.listQuery) != 1 {
		t.Fatalf("一覧の取得回数 = %d, want 1", len(f.listQuery))
	}
	if !strings.Contains(f.listQuery[0], "tags=sumiq") {
		t.Errorf("一覧をタグで絞っていません: %s", f.listQuery[0])
	}
	if strings.Contains(f.listQuery[0], "q=") {
		t.Errorf("全文検索に頼っています: %s", f.listQuery[0])
	}
}

func TestSave_MarksDraftWhenCreateIgnoresIt(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{
		t:      t,
		create: respond(http.StatusOK, queryJSON(123, q.Name, q.SQL, 3, false)),
		update: respond(http.StatusOK, queryJSON(123, q.Name, q.SQL, 3, true)),
	}
	c := start(t, f, nil)

	got, err := c.Save(context.Background(), q)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !got.IsDraft {
		t.Errorf("draft に更新されていません: %+v", got)
	}
	paths := f.paths()
	if paths[len(paths)-1] != "POST /api/queries/123" {
		t.Errorf("POST /api/queries/123 で更新していません: %v", paths)
	}
	if string(f.updateBody) != `{"is_draft":true}` {
		t.Errorf("更新の本文 = %s", f.updateBody)
	}
}

func TestSave_MarkDraftStillFalseIsError(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{
		t:      t,
		create: respond(http.StatusOK, queryJSON(123, q.Name, q.SQL, 3, false)),
		update: respond(http.StatusOK, queryJSON(123, q.Name, q.SQL, 3, false)),
	}
	c := start(t, f, nil)

	_, err := c.Save(context.Background(), q)
	if err == nil {
		t.Fatal("draft にできなかったのに成功扱いになりました")
	}
	if !strings.Contains(err.Error(), "/queries/123") {
		t.Errorf("作られた保存クエリの URL がエラーにありません: %v", err)
	}
}

func TestSave_ReusesExisting(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{t: t, list: map[string]string{
		"1": pageJSON(4,
			// 名前は同じだが人間が SQL を書き換えたもの、別データソースのものは再利用しない。
			queryJSON(10, q.Name, "SELECT 1", 3, true),
			queryJSON(11, q.Name, q.SQL, 4, true),
			// apply_auto_limit が違うと、画面で Execute したときに別の LIMIT で走る。
			queryJSONWithAutoLimit(13, q.Name, q.SQL, 3, true, true),
			queryJSON(12, q.Name, q.SQL, 3, false),
		),
	}}
	c := start(t, f, nil)

	got, err := c.Save(context.Background(), q)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got.ID != 12 || !got.Reused || got.IsDraft {
		t.Errorf("Save() = %+v, want ID 12 を再利用（公開済み）", got)
	}
	for _, p := range f.paths() {
		if strings.HasPrefix(p, "POST ") {
			t.Errorf("再利用したのに書き込んでいます: %v", f.paths())
		}
	}
}

func TestSave_PagesUntilCount(t *testing.T) {
	q := testSaveQuery()
	other := queryJSON(1, "sumiq: ffffffffffff", "SELECT 2", 3, true)
	first := make([]string, savedQueryPageSize)
	for i := range first {
		first[i] = other
	}
	f := &fakeQueries{t: t, list: map[string]string{
		"1": pageJSON(savedQueryPageSize+1, first...),
		"2": pageJSON(savedQueryPageSize+1, queryJSON(77, q.Name, q.SQL, 3, true)),
	}}
	c := start(t, f, nil)

	got, err := c.Save(context.Background(), q)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got.ID != 77 || !got.Reused {
		t.Errorf("2ページ目の既存クエリを再利用していません: %+v", got)
	}
	if len(f.listQuery) != 2 {
		t.Errorf("一覧の取得回数 = %d, want 2（count を超えて読まない）", len(f.listQuery))
	}
}

func TestSave_EndpointWithPath(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{t: t, prefix: "/redash", create: respond(http.StatusOK, queryJSON(123, q.Name, q.SQL, 3, true))}
	c := startWithPath(t, f, "/redash/")

	got, err := c.Save(context.Background(), q)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.HasSuffix(got.URL, "/redash/queries/123") {
		t.Errorf("URL = %s, want .../redash/queries/123", got.URL)
	}
}

func TestSave_ForbiddenIsAuthError(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{t: t, create: respond(http.StatusForbidden, `{"message":"You don't have permission"}`)}
	c := start(t, f, nil)

	_, err := c.Save(context.Background(), q)
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.StatusCode != http.StatusForbidden {
		t.Fatalf("403 が AuthError になっていません: %v", err)
	}
}

func TestSave_ServerErrorIsAPIError(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{t: t, create: respond(http.StatusInternalServerError, `{"message":"boom"}`)}
	c := start(t, f, nil)

	_, err := c.Save(context.Background(), q)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("500 が APIError になっていません: %v", err)
	}
}

func TestSave_RejectsMissingID(t *testing.T) {
	q := testSaveQuery()
	f := &fakeQueries{t: t, create: respond(http.StatusOK, `{"name":"x","is_draft":true}`)}
	c := start(t, f, nil)

	if _, err := c.Save(context.Background(), q); err == nil {
		t.Fatal("ID の無い応答を受け入れました")
	}
}

// startWithPath は endpoint にパスを付けた Client を返す。
func startWithPath(t *testing.T, h http.Handler, path string) *Client {
	t.Helper()
	return start(t, h, func(o *Options) { o.Endpoint += path })
}
