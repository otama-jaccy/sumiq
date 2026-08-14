package redash

import (
	"fmt"
	"strings"
	"time"
)

// エラーは呼び出し側が errors.As で仕分けられるよう型で分ける。
//
//	AuthError    認証・権限。API KEY が違う / データソースへの権限が無い
//	APIError     それ以外の HTTP エラー。4xx / 5xx とプロキシからの応答
//	JobError     ジョブが失敗した。SQL のエラーもここに入る
//	TimeoutError timeout 以内にジョブが終わらなかった
//
// どのエラーにも API KEY を載せない。Redash の応答を転記する箇所は
// すべて Client.scrub を通す。

// AuthError は認証・権限の失敗。
//
// Redash は不正な API KEY に対して 401 を返さない。/api/ 配下では
// flask_login の unauthorized_handler が 404 と
// {"message": "Couldn't find resource. Please login and try again."} を返す
// （redash/authentication/__init__.py の redirect_to_login）。
// そのため 404 のうちこの形のものは認証の失敗として扱う。
//
// 逆に 401 は認証の失敗とは限らない。POST /api/query_results は
// 「データソースが指定されていない」「データソースが利用できない」にも
// 401 を返す（redash/handlers/query_results.py の error_messages）。
// 文言でしか区別できないため、ここでは Redash のメッセージをそのまま見せる。
type AuthError struct {
	StatusCode int
	// Message は Redash が返した説明。空のこともある。
	Message string
}

func (e *AuthError) Error() string {
	const hint = "API KEY と、そのユーザがデータソースを実行できるかを確認してください"
	if e.Message == "" {
		return fmt.Sprintf("Redash の認証・権限に失敗しました (HTTP %d)。%s", e.StatusCode, hint)
	}
	return fmt.Sprintf("Redash の認証・権限に失敗しました (HTTP %d): %s。%s", e.StatusCode, e.Message, hint)
}

// APIError は認証・権限以外の HTTP エラー。
type APIError struct {
	StatusCode int
	// Message は応答から取れた説明。JSON でなければ本文の先頭を切り詰めたもの。
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Redash が HTTP %d を返しました", e.StatusCode)
	}
	return fmt.Sprintf("Redash が HTTP %d を返しました: %s", e.StatusCode, e.Message)
}

// JobError はジョブの失敗。
//
// SQL のエラーもここに入る。Redash はクエリランナーが返したエラーも、
// ワーカー側の例外も、同じ {"status": 4, "error": "..."} に畳んで返すため
// （redash/serializers/__init__.py の serialize_job）、応答からは区別できない。
// 区別できる材料は Message だけなので、Redash の文言をそのまま見せる。
//
// このエラーは HTTP 200 で返ってくる。ステータスコードで判定してはならない。
type JobError struct {
	JobID string
	// Status は Redash のジョブ状態（4 = FAILED、5 = CANCELED）。
	Status int
	// Message は Redash が返したエラー文言。
	Message string
}

func (e *JobError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Redash のクエリ実行に失敗しました (ジョブ %s, status %d)。"+
			"Redash はエラーの内容を返しませんでした", e.JobID, e.Status)
	}
	return fmt.Sprintf("Redash のクエリ実行に失敗しました (ジョブ %s): %s", e.JobID, e.Message)
}

// TimeoutError は timeout 以内に処理が終わらなかったことを表す。
//
// JobID が空でなければジョブは Redash 側で動き続けている。sumiq は待つのを
// やめるだけで、クエリを止めるわけではない。
type TimeoutError struct {
	// JobID はジョブ投入前に打ち切った場合は空。
	JobID   string
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	// ジョブ投入前に打ち切った場合、Redash には何も残っていない。
	// 「ジョブは動き続けます」と伝えると原因を見誤る。応答が無いのは
	// 接続の問題であって、クエリの重さではない。
	if e.JobID == "" {
		return fmt.Sprintf("Redash が %s 以内に応答しませんでした。"+
			"クエリは投入されていません。redash.endpoint と接続を確認してください", e.Timeout)
	}
	return fmt.Sprintf("Redash のクエリが %s 以内に終わりませんでした (ジョブ %s)。"+
		"ジョブは Redash 側で実行され続けます。redash.timeout を延ばすか、クエリを軽くしてください",
		e.Timeout, e.JobID)
}

// scrub は文字列から API KEY を取り除く。
//
// エラーに載る文字列は Redash の応答由来であり、通常 API KEY を含まない。
// それでも通すのは、含まれないことを一つ一つの経路で確かめ続けるより、
// 出口を1か所にして落とす方が確実だからで、事故（KEY を貼った端末ログを
// そのまま共有する等）を止めるのはこちらの側。
func (c *Client) scrub(s string) string {
	// 短い KEY を伏せると無関係な文字列まで壊れる。API KEY として意味を持つ
	// 長さではないので、その場合は何もしない。
	if len(c.apiKey) < 8 {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "****")
}
