package redash

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// TestListDataSources は実際の Redash の応答形（paused が 0/1 の数値）で
// []DataSource へデコードされることを見る。
//
// paused は redash/models/__init__.py の DataSource.paused プロパティが
// redis_connection.exists(...)（redis-py の exists() は int を返す）を
// そのまま返す実装で、JSON では bool ではなく 0/1 の数値になる。
func TestListDataSources(t *testing.T) {
	c := start(t, respond(http.StatusOK,
		`[{"id":1,"name":"analytics","type":"pg","paused":0},`+
			`{"id":2,"name":"legacy","type":"mysql","paused":1}]`), nil)

	got, err := c.ListDataSources(context.Background())
	if err != nil {
		t.Fatalf("ListDataSources: %v", err)
	}

	want := []DataSource{
		{ID: 1, Name: "analytics", Type: "pg", Paused: false},
		{ID: 2, Name: "legacy", Type: "mysql", Paused: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListDataSources() = %#v, want %#v", got, want)
	}
}

// TestListDataSourcesPausedAsBool は paused が bool リテラルで来ても読めることを見る。
//
// 実際の Redash は 0/1 の数値しか返さない（DataSource.UnmarshalJSON のコメント
// 参照）が、bool を送ってくる実装差分があっても壊れないようにしてある。
func TestListDataSourcesPausedAsBool(t *testing.T) {
	c := start(t, respond(http.StatusOK,
		`[{"id":1,"name":"analytics","type":"pg","paused":false},`+
			`{"id":2,"name":"legacy","type":"mysql","paused":true}]`), nil)

	got, err := c.ListDataSources(context.Background())
	if err != nil {
		t.Fatalf("ListDataSources: %v", err)
	}

	want := []DataSource{
		{ID: 1, Name: "analytics", Type: "pg", Paused: false},
		{ID: 2, Name: "legacy", Type: "mysql", Paused: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListDataSources() = %#v, want %#v", got, want)
	}
}

// TestListDataSourcesPausedInvalid は paused が bool でも 0/1 でもない
// 値のとき、推測せずエラーにすることを見る。
func TestListDataSourcesPausedInvalid(t *testing.T) {
	c := start(t, respond(http.StatusOK,
		`[{"id":1,"name":"analytics","type":"pg","paused":"maybe"}]`), nil)

	if _, err := c.ListDataSources(context.Background()); err == nil {
		t.Fatal("paused が不正な値なのにエラーになりませんでした")
	}
}

// TestListDataSourcesEmpty は空配列を空スライスとして返すことを見る。
func TestListDataSourcesEmpty(t *testing.T) {
	c := start(t, respond(http.StatusOK, `[]`), nil)

	got, err := c.ListDataSources(context.Background())
	if err != nil {
		t.Fatalf("ListDataSources: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListDataSources() = %#v, want 空", got)
	}
}

// TestListDataSourcesForbidden は権限エラーを既存の AuthError で扱うことを見る。
//
// list_data_sources permission が無いユーザの API KEY では、endpoint と
// API KEY 自体が正しくてもこのエンドポイントだけ 403 になりうる
// （Issue #33 「追加で必要になりそうな情報・確認事項」）。
func TestListDataSourcesForbidden(t *testing.T) {
	c := start(t, respond(http.StatusForbidden, `{"message":"Forbidden"}`), nil)

	_, err := c.ListDataSources(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("エラーの型 = %T, want *AuthError: %v", err, err)
	}
}

// TestListDataSourcesTimeout は timeout 超過を *TimeoutError として報告することを見る。
//
// classifyContextErr を通さないと、context の締切超過が do の connectError に
// 包まれたまま返り、「Redash への接続に失敗しました」という誤った文言になる
// （/code-review medium の指摘）。
func TestListDataSourcesTimeout(t *testing.T) {
	block := make(chan struct{})

	c := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}), func(o *Options) { o.Timeout = 80 * time.Millisecond })

	// LIFO: start が登録した srv.Close より後に登録し、先に走らせる。
	// 逆順だと Close がハンドラの終了を待ち、ハンドラは block を待ったまま
	// テストごと止まる（.claude/rules/go-architecture.md 「応答を止める
	// テストサーバは、締切と競争させない」）。
	t.Cleanup(func() { close(block) })

	begin := time.Now()
	_, err := c.ListDataSources(context.Background())
	elapsed := time.Since(begin)

	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("エラーの型 = %T, want *TimeoutError: %v", err, err)
	}
	if timeoutErr.Phase != PhaseListDataSources {
		t.Errorf("Phase = %q, want %q", timeoutErr.Phase, PhaseListDataSources)
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout を超えても待ち続けています: %v", elapsed)
	}
}
