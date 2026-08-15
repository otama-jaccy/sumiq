package redash

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// startDataSources は h を httptest で立て、それに向いた Client を返す。
func startDataSources(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New(Options{
		Endpoint:   srv.URL,
		APIKey:     testAPIKey,
		Timeout:    5 * time.Second,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestListDataSources は正常系で []DataSource へデコードされることを見る。
func TestListDataSources(t *testing.T) {
	c := startDataSources(t, respond(http.StatusOK,
		`[{"id":1,"name":"analytics","type":"pg","paused":false},`+
			`{"id":2,"name":"legacy","type":"mysql","paused":true}]`))

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

// TestListDataSourcesEmpty は空配列を空スライスとして返すことを見る。
func TestListDataSourcesEmpty(t *testing.T) {
	c := startDataSources(t, respond(http.StatusOK, `[]`))

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
	c := startDataSources(t, respond(http.StatusForbidden, `{"message":"Forbidden"}`))

	_, err := c.ListDataSources(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("エラーの型 = %T, want *AuthError: %v", err, err)
	}
}
