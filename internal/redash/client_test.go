package redash

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNewEndpoint はエンドポイントの検証を見る。
func TestNewEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  string
	}{
		{name: "https", endpoint: "https://redash.example.com"},
		{name: "http", endpoint: "http://localhost:5000"},
		{name: "末尾スラッシュ", endpoint: "https://redash.example.com/"},
		{name: "パス付き", endpoint: "https://example.com/redash"},
		{name: "パス付き末尾スラッシュ", endpoint: "https://example.com/redash/"},

		{name: "空", endpoint: "", wantErr: "空"},
		{name: "空白のみ", endpoint: "   ", wantErr: "空"},
		{name: "スキームなし", endpoint: "redash.example.com", wantErr: "http"},
		{name: "ホストなし", endpoint: "https://", wantErr: "ホスト"},
		{name: "別スキーム", endpoint: "ftp://redash.example.com", wantErr: "http"},
		// URL に認証情報を書けると、エラーやログに秘密が乗る経路ができる。
		{name: "認証情報付き", endpoint: "https://user:pass@example.com", wantErr: "user:password"},
		// api_key=... をクエリで渡す逃げ道を塞ぐ。URL はログに残る。
		{name: "クエリ付き", endpoint: "https://example.com?api_key=x", wantErr: "クエリ"},
		{name: "フラグメント付き", endpoint: "https://example.com#x", wantErr: "クエリ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(Options{Endpoint: tt.endpoint})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if c == nil {
					t.Fatal("Client が nil です")
				}
				return
			}
			if err == nil {
				t.Fatalf("エラーになりませんでした: %q", tt.endpoint)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("エラー文に %q が含まれていません: %v", tt.wantErr, err)
			}
		})
	}
}

// TestNewDefaults は未指定の項目に既定値が入ることを見る。
func TestNewDefaults(t *testing.T) {
	c, err := New(Options{Endpoint: "https://redash.example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", c.timeout, DefaultTimeout)
	}
	if c.pollInterval != DefaultPollInterval {
		t.Errorf("pollInterval = %v, want %v", c.pollInterval, DefaultPollInterval)
	}
	if c.httpClient == nil {
		t.Error("httpClient が nil です")
	}
}

// TestNewKeepsGivenValues は指定した値がそのまま使われることを見る。
func TestNewKeepsGivenValues(t *testing.T) {
	c, err := New(Options{
		Endpoint:     "https://redash.example.com",
		Timeout:      42 * time.Second,
		PollInterval: 7 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != 42*time.Second {
		t.Errorf("timeout = %v, want 42s", c.timeout)
	}
	if c.pollInterval != 7*time.Second {
		t.Errorf("pollInterval = %v, want 7s", c.pollInterval)
	}
}

// TestResolve は URL の組み立てを見る。
func TestResolve(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{endpoint: "https://redash.example.com", want: "https://redash.example.com/api/jobs/job-1"},
		{endpoint: "https://redash.example.com/", want: "https://redash.example.com/api/jobs/job-1"},
		{endpoint: "https://example.com/redash", want: "https://example.com/redash/api/jobs/job-1"},
		{endpoint: "https://example.com/redash/", want: "https://example.com/redash/api/jobs/job-1"},
		// パスのエスケープを保つ。%2F を本物の区切りに戻すと、
		// 別のパスへリクエストを投げることになる。
		{endpoint: "https://example.com/re%2Fdash", want: "https://example.com/re%2Fdash/api/jobs/job-1"},
		{endpoint: "https://example.com/a%20b", want: "https://example.com/a%20b/api/jobs/job-1"},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			c, err := New(Options{Endpoint: tt.endpoint})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := c.resolve("api", "jobs", "job-1"); got != tt.want {
				t.Errorf("resolve = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckJobID は URL のパス要素として使えない ID を弾くことを見る。
func TestCheckJobID(t *testing.T) {
	ok := []string{
		"job-1",
		"01234567-89ab-cdef-0123-456789abcdef", // rq の job_id は uuid4
		"a.b",
	}
	for _, id := range ok {
		if err := checkJobID(id); err != nil {
			t.Errorf("checkJobID(%q) = %v, want nil", id, err)
		}
	}

	ng := []string{
		"",
		".",
		"..",
		"a/b",
		"/etc/passwd",
		"a%2Fb", // JoinPath は % をエスケープしないので、サーバ側で / に戻る
		"a\nb",
	}
	for _, id := range ng {
		if err := checkJobID(id); err == nil {
			t.Errorf("checkJobID(%q) がエラーになりませんでした", id)
		}
	}
}

// TestScrub は API KEY の伏せ字を見る。
func TestScrub(t *testing.T) {
	c := &Client{apiKey: "0123456789abcdef"}
	got := c.scrub("failed with key 0123456789abcdef in it")
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("API KEY が残っています: %q", got)
	}
	if !strings.Contains(got, "****") {
		t.Errorf("伏せ字になっていません: %q", got)
	}

	// 短い値を伏せると無関係な文字列まで壊れる。API KEY として意味を持つ
	// 長さではないので何もしない。
	short := &Client{apiKey: "ab"}
	if got := short.scrub("a table"); got != "a table" {
		t.Errorf("短い KEY で書き換えが起きました: %q", got)
	}

	// KEY が空のときに全ての空文字列を置換してしまわないこと。
	empty := &Client{}
	if got := empty.scrub("abc"); got != "abc" {
		t.Errorf("KEY 未設定で書き換えが起きました: %q", got)
	}
}

// TestTimeoutErrorUnwrap は締切超過を errors.Is で拾えることを見る。
//
// 終了コードを分けたい internal/cli が型アサーションを強いられないようにする。
func TestTimeoutErrorUnwrap(t *testing.T) {
	err := error(&TimeoutError{Phase: PhaseWait, JobID: "job-1", Timeout: time.Minute})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("errors.Is(err, context.DeadlineExceeded) が false です")
	}
	if errors.Is(err, context.Canceled) {
		t.Error("キャンセルとして拾われています")
	}

	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.Phase != PhaseWait {
		t.Error("errors.As で段を取り出せません")
	}
}

// TestErrorMessages はエラーの文言に必要な情報が入ることを見る。
func TestErrorMessages(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		wantIn []string
	}{
		{
			name:   "AuthError",
			err:    &AuthError{StatusCode: 404, Message: "Couldn't find resource."},
			wantIn: []string{"404", "Couldn't find resource.", "API KEY"},
		},
		{
			name:   "AuthError_文言なし",
			err:    &AuthError{StatusCode: 403},
			wantIn: []string{"403", "API KEY"},
		},
		{
			name:   "APIError",
			err:    &APIError{StatusCode: 500, Message: "boom"},
			wantIn: []string{"500", "boom"},
		},
		{
			name:   "APIError_文言なし",
			err:    &APIError{StatusCode: 503},
			wantIn: []string{"503"},
		},
		{
			name:   "JobError",
			err:    &JobError{JobID: "job-1", Status: 4, Message: "syntax error"},
			wantIn: []string{"job-1", "syntax error"},
		},
		{
			name:   "JobError_文言なし",
			err:    &JobError{JobID: "job-1", Status: 4},
			wantIn: []string{"job-1", "4"},
		},
		{
			name:   "TimeoutError_完了待ち",
			err:    &TimeoutError{Phase: PhaseWait, JobID: "job-1", Timeout: 5 * time.Minute},
			wantIn: []string{"job-1", "5m0s", "実行され続けます"},
		},
		{
			name:   "TimeoutError_投入",
			err:    &TimeoutError{Phase: PhaseSubmit, Timeout: 5 * time.Minute},
			wantIn: []string{"5m0s", "分かりません", "endpoint"},
		},
		{
			name:   "TimeoutError_取得",
			err:    &TimeoutError{Phase: PhaseFetch, JobID: "job-1", Timeout: 5 * time.Minute},
			wantIn: []string{"job-1", "5m0s", "結果の取得"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, want := range tt.wantIn {
				if !strings.Contains(msg, want) {
					t.Errorf("エラー文に %q が含まれていません: %s", want, msg)
				}
			}
		})
	}
}
