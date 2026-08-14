package redash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestExecuteFlow は3段構えが順に呼ばれ、結果が返ることを見る。
func TestExecuteFlow(t *testing.T) {
	f := defaultFake(t)
	c := start(t, f, nil)

	res, err := c.Execute(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := []string{
		"POST /api/query_results",
		"GET /api/jobs/job-1",
		"GET /api/query_results/42",
	}
	if got := f.paths(); !reflect.DeepEqual(got, want) {
		t.Errorf("リクエストの順序が違います\n got: %v\nwant: %v", got, want)
	}

	wantCols := []Column{{Name: "id", Type: "integer"}, {Name: "email", Type: "string"}}
	if !reflect.DeepEqual(res.Columns, wantCols) {
		t.Errorf("Columns = %#v, want %#v", res.Columns, wantCols)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("Rows の件数 = %d, want 2", len(res.Rows))
	}
	if got := fmt.Sprint(res.Rows[0][0]); got != "1" {
		t.Errorf("Rows[0][0] = %v, want 1", got)
	}
	if got := res.Rows[0][1]; got != "a@example.com" {
		t.Errorf("Rows[0][1] = %v, want a@example.com", got)
	}
}

// TestExecuteSendsRequiredParams は POST の本文と認証ヘッダを見る。
func TestExecuteSendsRequiredParams(t *testing.T) {
	f := defaultFake(t)
	c := start(t, f, nil)

	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var got map[string]any
	dec := json.NewDecoder(strings.NewReader(string(f.postBody())))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("POST 本文を読めません: %v", err)
	}

	// max_age は 0 を明示的に送る。省略や -1 だとキャッシュが返る。
	for key, want := range map[string]string{
		"query":            "SELECT id, email FROM users",
		"data_source_id":   "3",
		"max_age":          "0",
		"apply_auto_limit": "true",
	} {
		v, ok := got[key]
		if !ok {
			t.Errorf("POST 本文に %s がありません: %v", key, got)
			continue
		}
		if fmt.Sprint(v) != want {
			t.Errorf("POST 本文の %s = %v, want %s", key, v, want)
		}
	}

	// Redash は Authorization の先頭の "Key " だけを剥がす。Bearer ではない。
	for i, h := range f.auth() {
		if h != "Key "+testAPIKey {
			t.Errorf("リクエスト %d の Authorization = %q, want %q", i, h, "Key "+testAPIKey)
		}
	}
}

// TestExecuteAutoLimitFalse は apply_auto_limit が false でも送られることを見る。
//
// Oracle / SQL Server では auto_limit がクエリを壊すため、データソース単位で
// false にできることが要る（ADR-0003 §10）。omitempty で消えると指定が効かない。
func TestExecuteAutoLimitFalse(t *testing.T) {
	f := defaultFake(t)
	c := start(t, f, nil)

	q := testQuery()
	q.AutoLimit = false
	if _, err := c.Execute(context.Background(), q); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(f.postBody(), &got); err != nil {
		t.Fatalf("POST 本文を読めません: %v", err)
	}
	if v, ok := got["apply_auto_limit"]; !ok || v != false {
		t.Errorf("apply_auto_limit = %v (ok=%v), want false", v, ok)
	}
}

// TestExecutePollsUntilFinished は完了するまでポーリングを続けることを見る。
func TestExecutePollsUntilFinished(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{
		respond(http.StatusOK, jobBody(jobQueued, "", "")),
		respond(http.StatusOK, jobBody(jobStarted, "", "")),
		respond(http.StatusOK, jobBody(jobFinished, "", "42")),
	}
	c := start(t, f, nil)

	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := f.pollCount(); got != 3 {
		t.Errorf("ポーリング回数 = %d, want 3", got)
	}
}

// TestExecuteRespectsPollInterval は poll_interval だけ待ってから次を叩くことを見る。
func TestExecuteRespectsPollInterval(t *testing.T) {
	const interval = 30 * time.Millisecond

	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{
		respond(http.StatusOK, jobBody(jobQueued, "", "")),
		respond(http.StatusOK, jobBody(jobFinished, "", "42")),
	}
	c := start(t, f, func(o *Options) { o.PollInterval = interval })

	begin := time.Now()
	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(begin)

	// 下限だけを見る。上限は実行環境の負荷で揺れるため主張しない。
	// ポーリングは投入後と2回目の前の2回入る。
	if want := 2 * interval; elapsed < want {
		t.Errorf("所要時間 = %v, poll_interval を待っていない (>= %v のはず)", elapsed, want)
	}
}

// TestExecuteWaitRetriesOnTransientAPIError は 5xx を一時的な障害として
// 再試行し、次のポーリングで成功すればジョブを諦めないことを見る（ADR-0012）。
func TestExecuteWaitRetriesOnTransientAPIError(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{
		respond(http.StatusBadGateway, `{"message":"Bad Gateway"}`),
		respond(http.StatusOK, jobBody(jobFinished, "", "42")),
	}
	c := start(t, f, nil)

	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := f.pollCount(); got != 2 {
		t.Errorf("ポーリング回数 = %d, want 2 (1回失敗 + 1回成功)", got)
	}
}

// TestExecuteWaitRetriesOnConnectFailure は接続そのものの失敗（LB の瞬断等）
// を再試行することを見る。
//
// KeepAlive を無効にしないと、net/http の Transport が「再利用した接続で
// ネットワークエラーが起きた冪等リクエスト」を自前で1回だけ黙って
// 再送してしまい、こちらの isRetryableWaitErr を通さずにテストが通ってしまう。
func TestExecuteWaitRetriesOnConnectFailure(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{
		hijackAndClose(),
		respond(http.StatusOK, jobBody(jobFinished, "", "42")),
	}
	c := start(t, f, func(o *Options) {
		o.HTTPClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	})

	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := f.pollCount(); got != 2 {
		t.Errorf("ポーリング回数 = %d, want 2 (1回失敗 + 1回成功)", got)
	}
}

// TestExecuteWaitDoesNotRetryAuthError は認証・権限の失敗を再試行しないことを見る。
//
// API KEY が誤っている場合、再試行しても直らない。即座に失敗しないと
// timeout まで無駄に待つことになる。
func TestExecuteWaitDoesNotRetryAuthError(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusUnauthorized, `{"message":"boom"}`)}
	c := start(t, f, func(o *Options) { o.Timeout = 2 * time.Second })

	begin := time.Now()
	_, err := c.Execute(context.Background(), testQuery())
	elapsed := time.Since(begin)

	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("エラーの型 = %T, want *AuthError: %v", err, err)
	}
	if got := f.pollCount(); got != 1 {
		t.Errorf("ポーリング回数 = %d, want 1 (再試行してはいけない)", got)
	}
	if elapsed > time.Second {
		t.Errorf("timeout 近くまで待っています。再試行していないか確認してください: %v", elapsed)
	}
}

// TestExecuteWaitDoesNotRetryClientError は 5xx 未満の APIError を再試行しないことを見る。
func TestExecuteWaitDoesNotRetryClientError(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusBadRequest, `{"message":"boom"}`)}
	c := start(t, f, func(o *Options) { o.Timeout = 2 * time.Second })

	_, err := c.Execute(context.Background(), testQuery())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("エラーの型 = %T, want *APIError: %v", err, err)
	}
	if got := f.pollCount(); got != 1 {
		t.Errorf("ポーリング回数 = %d, want 1 (再試行してはいけない)", got)
	}
}

// TestExecuteWaitRetryBoundedByTimeout は再試行が続いても timeout で
// 打ち切られることを見る。専用のリトライ上限を設けていないため、
// これが無いと持続的な障害で無限に再試行し続ける。
func TestExecuteWaitRetryBoundedByTimeout(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusBadGateway, `{"message":"Bad Gateway"}`)}
	c := start(t, f, func(o *Options) {
		o.Timeout = 80 * time.Millisecond
		o.PollInterval = 5 * time.Millisecond
	})

	begin := time.Now()
	_, err := c.Execute(context.Background(), testQuery())
	elapsed := time.Since(begin)

	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("エラーの型 = %T, want *TimeoutError: %v", err, err)
	}
	if timeoutErr.Phase != PhaseWait {
		t.Errorf("Phase = %q, want %q", timeoutErr.Phase, PhaseWait)
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout を超えても再試行し続けています: %v", elapsed)
	}
}

// TestExecuteJobFailedWith200 はジョブの失敗が HTTP 200 で返ることを見る。
//
// Redash は SQL のエラーも 200 の本文に載せる。ステータスコードだけで
// 判定すると、エラーを成功として扱ってしまう。
func TestExecuteJobFailedWith200(t *testing.T) {
	const sqlErr = `Error running query: syntax error at or near "FROMM" LINE 1`

	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(jobFailed, sqlErr, ""))}
	f.result = nil // ここに来てはいけない
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	var jobErr *JobError
	if !errors.As(err, &jobErr) {
		t.Fatalf("エラーの型 = %T, want *JobError: %v", err, err)
	}
	if jobErr.Message != sqlErr {
		t.Errorf("Message = %q, want %q", jobErr.Message, sqlErr)
	}
	// SQL のエラーはこの文言でしか区別できないため、必ず利用者まで届ける。
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("エラー文に Redash の説明が含まれていません: %v", err)
	}
}

// TestExecuteJobErrorBeatsStatus は status が完了でも error があれば失敗として扱うことを見る。
func TestExecuteJobErrorBeatsStatus(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(jobFinished, "boom", "42"))}
	f.result = nil
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	var jobErr *JobError
	if !errors.As(err, &jobErr) {
		t.Fatalf("エラーの型 = %T, want *JobError: %v", err, err)
	}
}

// TestExecuteJobStatuses はジョブ状態ごとの扱いを見る。
func TestExecuteJobStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		errMsg string
		wantAs any
	}{
		{name: "失敗", status: jobFailed, errMsg: "failed", wantAs: new(*JobError)},
		{name: "失敗_文言なし", status: jobFailed, wantAs: new(*JobError)},
		{name: "キャンセル", status: jobCanceled, errMsg: "Query cancelled by user.", wantAs: new(*JobError)},
		{name: "キャンセル_文言なし", status: jobCanceled, wantAs: new(*JobError)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := defaultFake(t)
			f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(tt.status, tt.errMsg, ""))}
			f.result = nil
			c := start(t, f, nil)

			_, err := c.Execute(context.Background(), testQuery())
			if err == nil {
				t.Fatal("エラーになりませんでした")
			}
			var jobErr *JobError
			if !errors.As(err, &jobErr) {
				t.Fatalf("エラーの型 = %T, want *JobError: %v", err, err)
			}
			if jobErr.Status != tt.status {
				t.Errorf("Status = %d, want %d", jobErr.Status, tt.status)
			}
		})
	}
}

// TestExecuteUnknownJobStatus は知らない状態でエラーにすることを見る。
//
// 「まだ動いている」に倒すと timeout まで待った末にタイムアウトとして
// 報告することになり、原因に辿り着けない。
func TestExecuteUnknownJobStatus(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(99, "", ""))}
	f.result = nil
	c := start(t, f, func(o *Options) { o.Timeout = 500 * time.Millisecond })

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		t.Fatalf("タイムアウトとして報告されました。知らない状態は即座に落とすべきです: %v", err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("エラー文に状態の値が含まれていません: %v", err)
	}
}

// TestExecuteFinishedWithoutResultID は完了したのに結果 ID が無い応答を落とすことを見る。
func TestExecuteFinishedWithoutResultID(t *testing.T) {
	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(jobFinished, "", ""))}
	f.result = nil
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "query_result_id") {
		t.Errorf("エラー文が原因を示していません: %v", err)
	}
}

// TestExecuteHTTPErrors は HTTP エラーの仕分けを見る。
func TestExecuteHTTPErrors(t *testing.T) {
	// Redash のエラー本文は2種類ある。error_response は job を、
	// flask_restful の abort と未認証時は message を返す。
	jobShaped := func(msg string) string {
		return fmt.Sprintf(`{"job":{"status":4,"error":%s}}`, strconv.Quote(msg))
	}
	msgShaped := func(msg string) string {
		return fmt.Sprintf(`{"message":%s}`, strconv.Quote(msg))
	}

	tests := []struct {
		name     string
		status   int
		body     string
		wantAuth bool
		wantIn   string
	}{
		{
			name:     "403_権限なし",
			status:   http.StatusForbidden,
			body:     jobShaped("You do not have permission to run queries with this data source."),
			wantAuth: true,
			wantIn:   "permission",
		},
		{
			// 401 は認証の失敗とは限らない。データソース未指定・利用不可でも返る。
			name:     "401_データソース利用不可",
			status:   http.StatusUnauthorized,
			body:     jobShaped("Target data source not available."),
			wantAuth: true,
			wantIn:   "data source not available",
		},
		{
			// API KEY が違うとき Redash が返すのはこれ。401 ではない。
			name:     "404_未認証",
			status:   http.StatusNotFound,
			body:     msgShaped("Couldn't find resource. Please login and try again."),
			wantAuth: true,
			wantIn:   "login",
		},
		{
			name:   "404_本当に存在しない",
			status: http.StatusNotFound,
			body:   msgShaped("No cached result found for this query."),
			wantIn: "No cached result",
		},
		{
			name:   "400",
			status: http.StatusBadRequest,
			body:   jobShaped("Please select data source to run this query."),
			wantIn: "select data source",
		},
		{
			name:   "500",
			status: http.StatusInternalServerError,
			body:   `{"message":"Internal Server Error"}`,
			wantIn: "Internal Server Error",
		},
		{
			// プロキシやロードバランサが JSON 以外を返すこともある。
			name:   "502_HTML",
			status: http.StatusBadGateway,
			body:   "<html><head><title>502 Bad Gateway</title></head></html>",
			wantIn: "502 Bad Gateway",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := defaultFake(t)
			f.submit = respond(tt.status, tt.body)
			f.jobs = nil
			f.result = nil
			c := start(t, f, nil)

			_, err := c.Execute(context.Background(), testQuery())
			if err == nil {
				t.Fatal("エラーになりませんでした")
			}

			var authErr *AuthError
			var apiErr *APIError
			switch {
			case tt.wantAuth && !errors.As(err, &authErr):
				t.Fatalf("エラーの型 = %T, want *AuthError: %v", err, err)
			case !tt.wantAuth && !errors.As(err, &apiErr):
				t.Fatalf("エラーの型 = %T, want *APIError: %v", err, err)
			}

			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("エラー文に %q が含まれていません: %v", tt.wantIn, err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(tt.status)) {
				t.Errorf("エラー文に HTTP ステータスが含まれていません: %v", err)
			}
		})
	}
}

// TestExecuteDoesNotLeakAPIKey は API KEY がエラーに出ないことを見る。
//
// Client.scrub を外すとこのテストは落ちる。
func TestExecuteDoesNotLeakAPIKey(t *testing.T) {
	// Redash 側が何らかの理由で受け取った KEY を本文に含めた場合を作る。
	f := defaultFake(t)
	f.submit = respond(http.StatusForbidden,
		fmt.Sprintf(`{"message":%s}`, strconv.Quote("bad key: "+testAPIKey)))
	f.jobs = nil
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

// TestExecuteTimeout は timeout を超えたら中断することを見る。
func TestExecuteTimeout(t *testing.T) {
	f := defaultFake(t)
	// いつまでも終わらないジョブ。
	f.jobs = []http.HandlerFunc{respond(http.StatusOK, jobBody(jobStarted, "", ""))}
	f.result = nil
	c := start(t, f, func(o *Options) {
		o.Timeout = 80 * time.Millisecond
		o.PollInterval = 5 * time.Millisecond
	})

	begin := time.Now()
	_, err := c.Execute(context.Background(), testQuery())
	elapsed := time.Since(begin)

	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("エラーの型 = %T, want *TimeoutError: %v", err, err)
	}
	if timeoutErr.JobID != "job-1" {
		t.Errorf("JobID = %q, want job-1", timeoutErr.JobID)
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout を超えても待ち続けています: %v", elapsed)
	}
	// ジョブは Redash 側で動き続けることを利用者に伝える。
	if !strings.Contains(err.Error(), "実行され続けます") {
		t.Errorf("中断後もジョブが残ることが伝わりません: %v", err)
	}
}

// TestExecuteContextCanceled は呼び出し側のキャンセルを timeout と区別することを見る。
func TestExecuteContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	f := defaultFake(t)
	f.jobs = []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) {
			// 1回目のポーリングを受けた時点で利用者が Ctrl-C を押した状況。
			cancel()
			respond(http.StatusOK, jobBody(jobStarted, "", ""))(w, r)
		},
	}
	f.result = nil
	c := start(t, f, func(o *Options) {
		o.Timeout = 10 * time.Second
		o.PollInterval = 5 * time.Millisecond
	})

	_, err := c.Execute(ctx, testQuery())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("エラー = %v, want context.Canceled", err)
	}
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		t.Errorf("キャンセルがタイムアウトとして報告されています: %v", err)
	}
}

// TestExecuteRejectsBadJobID はジョブ ID でパスを乗っ取れないことを見る。
//
// url.JoinPath は要素の "/" と "%" をエスケープせず ".." を辿るため、
// 検証しないと応答次第で GET 先が変わる。
func TestExecuteRejectsBadJobID(t *testing.T) {
	for _, id := range []string{"", "..", "../../evil", "a/b", "a%2Fb"} {
		t.Run(strconv.Quote(id), func(t *testing.T) {
			f := defaultFake(t)
			f.submit = respond(http.StatusOK, fmt.Sprintf(
				`{"job":{"id":%s,"status":1,"error":"","query_result_id":null}}`, strconv.Quote(id)))
			f.jobs = nil
			f.result = nil
			c := start(t, f, nil)

			if _, err := c.Execute(context.Background(), testQuery()); err == nil {
				t.Fatalf("ジョブ ID %q を受け入れました", id)
			}
		})
	}
}

// TestExecuteMissingJob は job の無い応答を落とすことを見る。
func TestExecuteMissingJob(t *testing.T) {
	f := defaultFake(t)
	f.submit = respond(http.StatusOK, `{"query_result":{"id":42}}`)
	f.jobs = nil
	f.result = nil
	c := start(t, f, nil)

	_, err := c.Execute(context.Background(), testQuery())
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "job") {
		t.Errorf("エラー文が原因を示していません: %v", err)
	}
}

// TestExecuteBrokenJSON は JSON として壊れた応答を落とすことを見る。
func TestExecuteBrokenJSON(t *testing.T) {
	f := defaultFake(t)
	f.submit = respond(http.StatusOK, `{"job":`)
	f.jobs = nil
	f.result = nil
	c := start(t, f, nil)

	if _, err := c.Execute(context.Background(), testQuery()); err == nil {
		t.Fatal("エラーになりませんでした")
	}
}

// TestExecuteValidatesInput は呼び出し側の指定を検証することを見る。
func TestExecuteValidatesInput(t *testing.T) {
	tests := []struct {
		name string
		q    Query
	}{
		{name: "SQL が空", q: Query{SQL: "", DataSourceID: 3}},
		{name: "SQL が空白のみ", q: Query{SQL: "  \n\t ", DataSourceID: 3}},
		{name: "data_source_id が 0", q: Query{SQL: "SELECT 1", DataSourceID: 0}},
		{name: "data_source_id が負", q: Query{SQL: "SELECT 1", DataSourceID: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := defaultFake(t)
			// 検証を通ってしまうとモックが「想定していないリクエスト」で落ちる。
			f.submit = nil
			f.jobs = nil
			f.result = nil
			c := start(t, f, nil)

			if _, err := c.Execute(context.Background(), tt.q); err == nil {
				t.Fatal("エラーになりませんでした")
			}
		})
	}
}

// TestExecuteWithBasePath はパス付きのエンドポイントで動くことを見る。
func TestExecuteWithBasePath(t *testing.T) {
	f := defaultFake(t)
	c := start(t, f, func(o *Options) { o.Endpoint = o.Endpoint + "/redash/" })

	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{
		"POST /redash/api/query_results",
		"GET /redash/api/jobs/job-1",
		"GET /redash/api/query_results/42",
	}
	if got := f.paths(); !reflect.DeepEqual(got, want) {
		t.Errorf("リクエスト先が違います\n got: %v\nwant: %v", got, want)
	}
}

// TestExecuteWithoutAPIKey は API KEY 無しでは Authorization を付けないことを見る。
func TestExecuteWithoutAPIKey(t *testing.T) {
	f := defaultFake(t)
	c := start(t, f, func(o *Options) { o.APIKey = "" })

	if _, err := c.Execute(context.Background(), testQuery()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for i, h := range f.auth() {
		if h != "" {
			t.Errorf("リクエスト %d の Authorization = %q, want 空", i, h)
		}
	}
}
