package redash

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルはセルフレビューで見つかった穴に対する検査を置く。

// TestJobErrorDoesNotLeakAPIKey はジョブの error 経由でも API KEY が出ないことを見る。
//
// ジョブの失敗は HTTP 200 で返るため、HTTP エラーとは別の経路を通る。
// finished を Client のメソッドにして cleanMessage を通すのをやめると落ちる。
func TestJobErrorDoesNotLeakAPIKey(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusOK,
		jobBody(jobFailed, "query failed with key "+testAPIKey, ""))}
	f.result = nil
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("エラー文に API KEY が含まれています: %v", err)
	}
	if !strings.Contains(err.Error(), "****") {
		t.Errorf("伏せ字になっていません: %v", err)
	}
}

// TestJobErrorIsTruncated はジョブの error が長くても切り詰められることを見る。
//
// Redash はクエリランナーの例外をそのまま error に入れる。数 KB の
// traceback を端末にそのまま吐かせない。
func TestJobErrorIsTruncated(t *testing.T) {
	long := "Traceback (most recent call last):\n" + strings.Repeat("  at some_frame()\n", 500)

	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(jobFailed, long, ""))}
	f.result = nil
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}

	var jobErr *JobError
	if !errors.As(err, &jobErr) {
		t.Fatalf("エラーの型 = %T, want *JobError", err)
	}
	if n := len([]rune(jobErr.Message)); n > maxErrorMessageRunes+3 {
		t.Errorf("Message の長さ = %d, 切り詰められていません", n)
	}
	// 改行を残すと端末の出力が崩れる。
	if strings.ContainsAny(jobErr.Message, "\n\r") {
		t.Errorf("Message に改行が残っています: %q", jobErr.Message)
	}
	// 何が起きたかは残す。
	if !strings.Contains(jobErr.Message, "Traceback") {
		t.Errorf("Message の先頭が失われています: %q", jobErr.Message)
	}
}

// TestTimeoutBeforeSubmit は投入前の打ち切りで誤った説明をしないことを見る。
//
// ジョブが作られていないのに「ジョブは実行され続けます」と伝えると、
// 接続の問題をクエリの重さと取り違える。
func TestTimeoutBeforeSubmit(t *testing.T) {
	// 応答を返さないサーバ。
	block := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	// Cleanup は後入れ先出しで走る。block を閉じるのを後に登録して先に走らせないと、
	// srv.Close() がハンドラの終了を待ち、ハンドラは block を待って止まる。
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })

	c, err := New(Options{
		Endpoint:     srv.URL,
		APIKey:       testAPIKey,
		Timeout:      80 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Execute(context.Background(), testQuery())
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("エラーの型 = %T, want *TimeoutError: %v", err, err)
	}
	if timeoutErr.Phase != PhaseSubmit {
		t.Errorf("Phase = %q, want %q", timeoutErr.Phase, PhaseSubmit)
	}
	if timeoutErr.JobID != "" {
		t.Errorf("JobID = %q, want 空", timeoutErr.JobID)
	}
	if strings.Contains(err.Error(), "実行され続けます") {
		t.Errorf("ジョブが動いていると言い切っています: %v", err)
	}
	// POST が届いてジョブが積まれた可能性は残る。「投入されていません」と
	// 言い切ると、実際には走っているクエリを放置してよいと誤解させる。
	if strings.Contains(err.Error(), "投入されていません") {
		t.Errorf("投入されていないと言い切っています: %v", err)
	}
	if !strings.Contains(err.Error(), "分かりません") {
		t.Errorf("投入されたか不明であることが伝わりません: %v", err)
	}
}

// TestTimeoutDuringFetch は結果取得中の打ち切りで誤った助言をしないことを見る。
//
// この段まで来たジョブは完了している。「ジョブは実行され続けます」
// 「クエリを軽くしてください」はどちらも的外れになる。
func TestTimeoutDuringFetch(t *testing.T) {
	block := make(chan struct{})
	reachedFetch := make(chan struct{})
	var once sync.Once

	f := defaultFake(t)
	f.result = func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(reachedFetch) })
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}

	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })

	// 締切を壁時計で競争させない。投入と1回のポーリングはローカルの
	// httptest 相手で 1ms 程度、取得は永久に返らないので、締切は必ず
	// 取得の段で切れる。実行環境が詰まっていても前の段には落ちない。
	c, err := New(Options{
		Endpoint:     srv.URL,
		APIKey:       testAPIKey,
		Timeout:      time.Second,
		PollInterval: time.Millisecond,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Execute(context.Background(), testQuery())

	// 取得の段に届いていなければ、このテストは狙った経路を見ていない。
	select {
	case <-reachedFetch:
	default:
		t.Fatal("取得の段に到達しませんでした")
	}

	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("エラーの型 = %T, want *TimeoutError: %v", err, err)
	}
	if timeoutErr.Phase != PhaseFetch {
		t.Errorf("Phase = %q, want %q", timeoutErr.Phase, PhaseFetch)
	}
	if timeoutErr.JobID != "job-1" {
		t.Errorf("JobID = %q, want job-1", timeoutErr.JobID)
	}
	if strings.Contains(err.Error(), "実行され続けます") {
		t.Errorf("完了したジョブが動いていると伝えています: %v", err)
	}
	if strings.Contains(err.Error(), "クエリを軽く") {
		t.Errorf("転送が遅いのにクエリを軽くしろと言っています: %v", err)
	}
	if !strings.Contains(err.Error(), "結果の取得") {
		t.Errorf("結果取得の段だと伝わりません: %v", err)
	}
}

// TestClipsLongJobID は長いジョブ ID をそのまま吐かないことを見る。
func TestClipsLongJobID(t *testing.T) {
	long := strings.Repeat("a", 5000) + "/evil"

	f := defaultFake(t)
	f.submit = respond(http.StatusOK, fmt.Sprintf(
		`{"job":{"id":%s,"status":1,"error":"","query_result_id":null}}`, strconv.Quote(long)))
	f.jobs = nil
	f.result = nil
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if n := len([]rune(err.Error())); n > maxErrorMessageRunes+200 {
		t.Errorf("エラー文の長さ = %d, 切り詰められていません", n)
	}
}

// TestRefusesRedirect はリダイレクトを追わないことを見る。
//
// 追うと2つ起きる。Go は 301/302/303 で POST を GET に落とすため、
// ジョブ投入が黙って GET になる。さらに Authorization の転送判定は
// ホスト名の比較だけなので、https → http のリダイレクトで API KEY が
// 平文で流れる。
func TestRefusesRedirect(t *testing.T) {
	// リダイレクト先。ここに Authorization が届いてはいけない。
	var leaked string
	var gotMethod string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(jobBody(jobQueued, "", "")))
	}))
	t.Cleanup(target.Close)

	for _, code := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			leaked, gotMethod = "", ""

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+r.URL.Path, code)
			}))
			t.Cleanup(srv.Close)

			c, err := New(Options{
				Endpoint:   srv.URL,
				APIKey:     testAPIKey,
				Timeout:    2 * time.Second,
				HTTPClient: srv.Client(),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, err = c.Execute(context.Background(), testQuery())
			if err == nil {
				t.Fatal("リダイレクトを追ってしまいました")
			}
			if leaked != "" {
				t.Errorf("リダイレクト先に Authorization が渡りました: %q (method=%s)", leaked, gotMethod)
			}
			if !strings.Contains(err.Error(), "リダイレクト") {
				t.Errorf("リダイレクトが原因だと分かりません: %v", err)
			}
			// リダイレクト先は伝える。endpoint を直すのに要る。
			if !strings.Contains(err.Error(), target.URL) {
				t.Errorf("リダイレクト先が示されていません: %v", err)
			}
		})
	}
}

// TestRedirectErrorDropsQuery はリダイレクト先のクエリをエラーに載せないことを見る。
//
// SSO のリダイレクト先には認証トークンが載る。
func TestRedirectErrorDropsQuery(t *testing.T) {
	const secret = "sso-token-do-not-log"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("https://sso.example.com/login?token=%s", secret),
			http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{Endpoint: srv.URL, APIKey: testAPIKey, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("リダイレクト先のクエリがエラーに載っています: %v", err)
	}
	if !strings.Contains(err.Error(), "sso.example.com/login") {
		t.Errorf("リダイレクト先が示されていません: %v", err)
	}
}

// TestNewDoesNotMutateGivenHTTPClient は渡された http.Client を書き換えないことを見る。
func TestNewDoesNotMutateGivenHTTPClient(t *testing.T) {
	given := &http.Client{}
	c, err := New(Options{Endpoint: "https://redash.example.com", HTTPClient: given})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if given.CheckRedirect != nil {
		t.Error("渡された http.Client の CheckRedirect が書き換えられました")
	}
	if c.httpClient == given {
		t.Error("渡された http.Client をそのまま使っています")
	}
	if c.httpClient.CheckRedirect == nil {
		t.Error("CheckRedirect が設定されていません")
	}
}
