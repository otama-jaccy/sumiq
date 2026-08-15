package redash

import (
	"context"
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
// どのエラーにも API KEY を載せない。Redash の応答を転記するときは、
// 秘密が混ざりうる経路（HTTP の本文とジョブの error）を Client.cleanMessage に、
// それ以外（ジョブ ID・列名）を clipMessage に通す。前者は伏せ字と
// 長さの上限の両方、後者は長さの上限だけを効かせる。

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

// Phase は3段構えのどこで打ち切ったか。
//
// 段によってジョブの状態も、利用者が取るべき手も違う。1つの文言に
// まとめると、どれかの段で必ず嘘になる。
type Phase string

const (
	// PhaseSubmit はジョブ投入（POST /api/query_results）。
	PhaseSubmit Phase = "submit"
	// PhaseWait はジョブの完了待ち（GET /api/jobs/{id}）。
	PhaseWait Phase = "wait"
	// PhaseFetch は結果の取得（GET /api/query_results/{id}）。
	PhaseFetch Phase = "fetch"
	// PhaseListDataSources は GET /api/data_sources（ListDataSources）。
	// submit/wait/fetch の3段構えとは別の、ジョブを介さない単発 GET。
	PhaseListDataSources Phase = "list_data_sources"
)

// TimeoutError は timeout 以内に処理が終わらなかったことを表す。
type TimeoutError struct {
	// Phase は打ち切った段。
	Phase Phase
	// JobID は PhaseSubmit では空。まだジョブ ID を受け取っていない。
	JobID   string
	Timeout time.Duration
}

// Unwrap は context.DeadlineExceeded を返す。
//
// 打ち切りの理由は締切超過そのものなので、errors.Is で拾えるようにしておく。
// これが無いと、終了コードを分けたい internal/cli が型アサーションを
// 強いられる。段ごとの説明が要るときだけ errors.As で TimeoutError を取る。
func (e *TimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func (e *TimeoutError) Error() string {
	switch e.Phase {
	case PhaseSubmit:
		// 応答が返らなかっただけで、POST が Redash に届いてジョブが
		// 積まれた可能性は残る。「投入されていません」と言い切ると、
		// 実際には走っているクエリを止めなくてよいと誤解させる。
		return fmt.Sprintf("Redash が %s 以内に応答しませんでした。"+
			"クエリが投入されたかどうかは分かりません。redash.endpoint と接続を確認してください",
			e.Timeout)
	case PhaseFetch:
		// ここまで来たジョブは完了している。クエリを軽くしろという助言は
		// 的外れで、時間がかかっているのは結果の転送。
		return fmt.Sprintf("Redash のクエリは完了しましたが、結果の取得が %s 以内に"+
			"終わりませんでした (ジョブ %s)。redash.timeout を延ばすか、"+
			"取得する行数を減らしてください", e.Timeout, e.JobID)
	case PhaseListDataSources:
		// ジョブを介さない単発 GET なので、ジョブ ID もクエリを軽くする助言も無い。
		return fmt.Sprintf("Redash が %s 以内にデータソース一覧を返しませんでした。"+
			"redash.timeout を延ばすか、接続を確認してください", e.Timeout)
	default:
		return fmt.Sprintf("Redash のクエリが %s 以内に終わりませんでした (ジョブ %s)。"+
			"ジョブは Redash 側で実行され続けます。redash.timeout を延ばすか、クエリを軽くしてください",
			e.Timeout, e.JobID)
	}
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
