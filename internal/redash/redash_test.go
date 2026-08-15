package redash

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// テストは httptest のモックサーバだけを相手にする。実際の Redash には繋がない。

const testAPIKey = "test-api-key-0123456789"

// fakeRedash は3段構えの応答を組み立てるモックサーバ。
//
// 段ごとに応答を差し替えられるようにしてあるのは、失敗の再現がほぼ全て
// 「ある1段だけが普通と違う応答を返す」形になるため。
type fakeRedash struct {
	t *testing.T

	// submit は POST /api/query_results の応答。
	submit http.HandlerFunc
	// jobs は GET /api/jobs/{id} の応答を呼ばれた順に返す。
	// 尽きたら最後のものを返し続ける。
	jobs []http.HandlerFunc
	// result は GET /api/query_results/{id} の応答。
	result http.HandlerFunc

	mu sync.Mutex
	// requests は受け取ったリクエストの "METHOD /path" を順に記録する。
	requests []string
	// authHeaders は受け取った Authorization ヘッダ。
	authHeaders []string
	// submitBody は POST の本文。
	submitBody []byte
	// pollTimes はジョブのポーリングを受けた時刻。
	pollTimes []time.Time
}

func (f *fakeRedash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/query_results"):
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Errorf("リクエスト本文を読めません: %v", err)
		}
		f.mu.Lock()
		f.submitBody = body
		f.mu.Unlock()
		f.handler(f.submit)(w, r)

	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/jobs/"):
		f.mu.Lock()
		n := len(f.pollTimes)
		f.pollTimes = append(f.pollTimes, time.Now())
		f.mu.Unlock()
		if len(f.jobs) == 0 {
			f.t.Errorf("ジョブのポーリングを想定していません: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if n >= len(f.jobs) {
			n = len(f.jobs) - 1
		}
		f.jobs[n](w, r)

	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/query_results/"):
		f.handler(f.result)(w, r)

	default:
		f.t.Errorf("想定していないリクエスト: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// handler は未設定のハンドラを踏んだときにテストを落とす。
func (f *fakeRedash) handler(h http.HandlerFunc) http.HandlerFunc {
	if h != nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("応答を用意していないリクエスト: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (f *fakeRedash) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pollTimes)
}

func (f *fakeRedash) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeRedash) auth() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authHeaders...)
}

func (f *fakeRedash) postBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.submitBody...)
}

// hijackAndClose は応答を返さずに接続を切る HandlerFunc を作る。
//
// LB のコネクションリセットや DNS の一時的な失敗など、HTTP 応答が
// 返る前に接続そのものが失敗するケースを再現する。ハンドラはサーバ側の
// 別ゴルーチンで動くため、testing.T の Fatal 系は使わず panic で落とす。
func hijackAndClose() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("ResponseWriter は http.Hijacker を満たしません")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
	}
}

// respond は status と本文を返す HandlerFunc を作る。
func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// jobBody は Redash の serialize_job と同じ形の応答本文を組み立てる。
// resultID が空なら result / query_result_id は null になる。
func jobBody(status int, errMsg, resultID string) string {
	if resultID == "" {
		resultID = "null"
	}
	return fmt.Sprintf(
		`{"job":{"id":"job-1","updated_at":0,"status":%d,"error":%s,"result":%s,"query_result_id":%s}}`,
		status, strconv.Quote(errMsg), resultID, resultID)
}

// resultBody は GET /api/query_results/{id} の応答本文を組み立てる。
func resultBody(columns, rows string) string {
	return fmt.Sprintf(`{"query_result":{"id":42,"query_hash":"h","query":"SELECT 1",
		"data":{"columns":[%s],"rows":[%s]},"data_source_id":3,"runtime":0.1,
		"retrieved_at":"2026-08-14T00:00:00.000Z"}}`, columns, rows)
}

// col は columns の1要素を組み立てる。
func col(name, typ string) string {
	return fmt.Sprintf(`{"name":%s,"friendly_name":%s,"type":%s}`,
		strconv.Quote(name), strconv.Quote(name), strconv.Quote(typ))
}

// defaultFake は「投入 → 1回ポーリングして完了 → 結果取得」を返すモック。
func defaultFake(t *testing.T) *fakeRedash {
	t.Helper()
	return &fakeRedash{
		t:      t,
		submit: respond(http.StatusOK, jobBody(jobQueued, "", "")),
		jobs:   []http.HandlerFunc{respond(http.StatusOK, jobBody(jobFinished, "", "42"))},
		result: respond(http.StatusOK, resultBody(
			col("id", "integer")+","+col("email", "string"),
			`{"id":1,"email":"a@example.com"},{"id":2,"email":"b@example.com"}`)),
	}
}

// start は h を httptest で立て、それに向いた Client を返す。
//
// h は *fakeRedash に限らず任意の http.Handler を受け付ける。3段構えの
// モックが要らない単発 GET のテスト（data_sources_test.go 等）は
// http.HandlerFunc をそのまま渡せる。
func start(t *testing.T, h http.Handler, mutate func(*Options)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	opts := Options{
		Endpoint:     srv.URL,
		APIKey:       testAPIKey,
		Timeout:      5 * time.Second,
		PollInterval: time.Millisecond,
		HTTPClient:   srv.Client(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// testQuery はテストで使う標準的な入力。
func testQuery() Query {
	return Query{SQL: "SELECT id, email FROM users", DataSourceID: 3, AutoLimit: true}
}
