package redash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Query は実行する ad-hoc クエリ1つ分。
type Query struct {
	// SQL は実行するクエリ本文。
	SQL string
	// DataSourceID は Redash の data_source_id。名前からの解決は設定側が行う。
	DataSourceID int
	// AutoLimit は apply_auto_limit に渡す値。
	//
	// 効けば転送量が減るだけの最適化であり、安全装置ではない（ADR-0003 §10）。
	// CTE を含むクエリや非 SQL のデータソースでは黙って効かない。
	AutoLimit bool
}

// queryRequest は POST /api/query_results のリクエスト本文。
type queryRequest struct {
	Query          string `json:"query"`
	DataSourceID   int    `json:"data_source_id"`
	MaxAge         int    `json:"max_age"`
	ApplyAutoLimit bool   `json:"apply_auto_limit"`
}

// maxAgeAlwaysExecute は max_age に渡す値。
//
// 0 は「キャッシュを使わず必ず実行する」。Redash は max_age を省略または -1
// にすると期限を問わずキャッシュを返すため（query_results.py の docstring）、
// 明示的に 0 を送る。マスクの対象を決めるのは実行時の列であり、
// いつのものか分からない結果を返されると、それが今のスキーマに対応するのか
// 保証できない。
const maxAgeAlwaysExecute = 0

// errConnectFailed は do がリクエストを送る前後で接続そのものに失敗したことを表す。
// submit・wait・fetch のどの呼び出しでも起こりうる、do 共通の分類。
//
// 現状これを再試行の判定に使っているのは wait（isRetryableWaitErr）だけだが、
// それは「投入・取得にも同じ方針を適用するか」を本 Issue のスコープ外とした
// ためであり、この分類自体が wait 専用というわけではない（ADR-0012）。
var errConnectFailed = errors.New("Redash への接続に失敗しました")

// Execute はクエリを投入し、完了を待ち、結果を返す。
//
// ctx のキャンセルで中断できる。加えて Client.timeout を上限として、
// 超えたら TimeoutError を返す。中断してもジョブは Redash 側で動き続ける。
func (c *Client) Execute(ctx context.Context, q Query) (*Result, error) {
	if strings.TrimSpace(q.SQL) == "" {
		return nil, errors.New("実行するクエリが空です")
	}
	if q.DataSourceID <= 0 {
		return nil, fmt.Errorf("data_source_id は 1 以上で指定してください: %d", q.DataSourceID)
	}

	// timeout は Execute 1回全体にかける。利用者から見た「待ち時間」は
	// 投入・ポーリング・取得の合計であり、段ごとに上限を分けると
	// 設定した値より長く待たされる。
	execCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	first, err := c.submit(execCtx, q)
	if err != nil {
		return nil, c.classifyContextErr(ctx, execCtx, PhaseSubmit, "", err)
	}
	// 以降 URL に使う ID はここで検証したものだけ。ポーリングの応答が返す
	// id は使わない（checkJobID のコメントを参照）。
	jobID := first.ID
	if err := checkJobID(jobID); err != nil {
		return nil, err
	}

	resultID, err := c.wait(execCtx, jobID, first)
	if err != nil {
		return nil, c.classifyContextErr(ctx, execCtx, PhaseWait, jobID, err)
	}

	// ここから先、ジョブは完了している。打ち切ったときに「まだ動いている」と
	// 伝えないよう、段を分けて渡す。
	res, err := c.fetch(execCtx, resultID)
	if err != nil {
		return nil, c.classifyContextErr(ctx, execCtx, PhaseFetch, jobID, err)
	}
	return res, nil
}

// submit はジョブを投入する。
func (c *Client) submit(ctx context.Context, q Query) (*job, error) {
	body, err := json.Marshal(queryRequest{
		Query:          q.SQL,
		DataSourceID:   q.DataSourceID,
		MaxAge:         maxAgeAlwaysExecute,
		ApplyAutoLimit: q.AutoLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("リクエストを組み立てられませんでした: %w", err)
	}

	var env jobEnvelope
	if err := c.do(ctx, http.MethodPost, c.resolve("api", "query_results"), body, &env); err != nil {
		return nil, err
	}
	if env.Job == nil {
		// max_age: 0 で投げている限り Redash は必ずジョブを返す
		// （query_results.py の run_query は max_age == 0 でキャッシュを見ない）。
		// job が無いのは想定していない応答なので、推測で進めずに落とす。
		return nil, errors.New("Redash の応答に job がありません")
	}
	return env.Job, nil
}

// wait はジョブが完了するまでポーリングし、結果の ID を返す。
//
// GET /api/jobs/{id} が一時的な障害（接続失敗・5xx）で失敗しても、それだけで
// ジョブを諦めない。GET は冪等でジョブ ID は検証済み、かつジョブは Redash 側で
// 走り続けるため、同じ間隔で叩き直すのが安全（ADR-0012）。リトライに専用の
// 上限は設けず、Execute が execCtx に設定した timeout まで続ける。
// 認証・権限の失敗やその他の 4xx、ジョブ自体の失敗（finished が返すもの）は
// 再試行しても状況が変わらないため対象にしない。
func (c *Client) wait(ctx context.Context, jobID string, first *job) (int64, error) {
	cur := first
	for {
		done, err := c.finished(cur)
		if err != nil {
			return 0, err
		}
		if done {
			if cur.QueryResultID == nil || *cur.QueryResultID <= 0 {
				return 0, fmt.Errorf("Redash はジョブ %s の完了を返しましたが、"+
					"取得すべき query_result_id がありません", jobID)
			}
			return *cur.QueryResultID, nil
		}

		cur, err = c.pollNext(ctx, jobID)
		if err != nil {
			return 0, err
		}
	}
}

// pollNext は poll_interval だけ待ってから GET /api/jobs/{id} を叩く。
//
// 一時的な障害（接続失敗・5xx）で失敗した場合は、cur を書き戻さずに
// 同じ間隔でもう一度叩き直す。これにより wait 側は変化していない
// 直前のジョブ状態を無駄に finished で再評価せずに済む。
func (c *Client) pollNext(ctx context.Context, jobID string) (*job, error) {
	for {
		if err := sleep(ctx, c.pollInterval); err != nil {
			return nil, err
		}
		next, err := c.jobStatus(ctx, jobID)
		if err == nil {
			return next, nil
		}
		if !isRetryableWaitErr(err) {
			return nil, err
		}
	}
}

// isRetryableWaitErr は wait 内の GET /api/jobs/{id} の失敗を、ジョブを
// 諦めずに叩き直してよい一時的な障害として扱うかを判定する。
//
// 対象は接続そのものの失敗（DNS・タイムアウト・コネクションリセット等、
// errConnectFailed で包まれる）と 5xx の APIError に限る。AuthError（401/403/
// 404 の認証扱い）や 5xx 未満の APIError は再試行しても直らず、対象にすると
// 本当の失敗の検知が遅れるだけになる。
func isRetryableWaitErr(err error) bool {
	if errors.Is(err, errConnectFailed) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	return false
}

// jobStatus は GET /api/jobs/{job_id} を1回叩く。
func (c *Client) jobStatus(ctx context.Context, jobID string) (*job, error) {
	var env jobEnvelope
	if err := c.do(ctx, http.MethodGet, c.resolve("api", "jobs", jobID), nil, &env); err != nil {
		return nil, err
	}
	if env.Job == nil {
		return nil, fmt.Errorf("Redash の応答に job がありません (ジョブ %s)", jobID)
	}
	// エラー文言に使う ID は応答のものではなく、こちらが投げた ID に固定する。
	env.Job.ID = jobID
	return env.Job, nil
}

// fetch は結果を取得して Result に落とす。
func (c *Client) fetch(ctx context.Context, resultID int64) (*Result, error) {
	var env queryResultEnvelope
	url := c.resolve("api", "query_results", strconv.FormatInt(resultID, 10))
	if err := c.do(ctx, http.MethodGet, url, nil, &env); err != nil {
		return nil, err
	}
	if env.QueryResult == nil {
		return nil, fmt.Errorf("Redash の応答に query_result がありません (結果 %d)", resultID)
	}
	return env.QueryResult.toResult()
}

// sleep は d 待つ。待っている間に ctx が終わったらその理由を返す。
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// classifyContextErr は打ち切りの理由を呼び出し側に分かる形に直す。
//
// context.WithTimeout で作った ctx が期限切れになると、原因が
// 「利用者が Ctrl-C を押した」なのか「redash.timeout を超えた」なのかが
// DeadlineExceeded 一つに潰れる。親を先に見て区別する。
func (c *Client) classifyContextErr(parent, exec context.Context, phase Phase, jobID string, err error) error {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if parent.Err() != nil {
		return fmt.Errorf("Redash のクエリ実行を中断しました: %w", parent.Err())
	}
	if errors.Is(exec.Err(), context.DeadlineExceeded) {
		return &TimeoutError{Phase: phase, JobID: jobID, Timeout: c.timeout}
	}
	return err
}

// do は HTTP リクエストを1回投げ、応答の JSON を dst に読む。
func (c *Client) do(ctx context.Context, method, url string, body []byte, dst any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return fmt.Errorf("リクエストを作れませんでした: %w", err)
	}
	// Redash は Authorization ヘッダの先頭の "Key " だけを剥がして API KEY として
	// 読む（redash/authentication/__init__.py の get_api_key_from_request）。
	// Bearer ではない。
	//
	// api_key クエリパラメータでも渡せるが使わない。URL はリダイレクト先や
	// プロキシのアクセスログに残る。
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Key "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// リダイレクトで止めた場合は url.Error の包みを捨てる。
		// 包みの URL フィールドにはリダイレクト先がクエリごと入っており、
		// SSO のトークンを載せたまま返すことになる。
		var redirErr *redirectError
		if errors.As(err, &redirErr) {
			return redirErr
		}
		// それ以外の err は *url.Error でメソッドと URL を含む。URL は検証済みの
		// endpoint から組み立てたもので、クエリも認証情報も持たない。
		// API KEY はヘッダにあるので出ない。リクエストのダンプも載せない。
		//
		// errConnectFailed で包む。呼び出し元（今は wait だけ）はこれを見て
		// 一時的な障害かどうかを判定できる（LB の瞬断や DNS の一時的な失敗を、
		// 走り続けているジョブごと捨てないため。ADR-0012）。
		return fmt.Errorf("%w: %w", errConnectFailed, err)
	}
	defer func() {
		// 本文を読み切らずに閉じると接続が再利用されない。エラー時は
		// errorMessage が先頭だけ読んでいるため、残りをここで捨てる。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.httpError(resp)
	}

	dec := json.NewDecoder(resp.Body)
	// UseNumber は Row の型の前提。外すと 2^53 を超える整数が静かに丸まる。
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("Redash の応答を JSON として読めませんでした: %w", err)
	}
	return nil
}

const (
	// maxErrorBodyBytes はエラー応答から読む本文の上限。
	// プロキシが返す HTML を丸ごと抱えないため。
	maxErrorBodyBytes = 8 << 10
	// maxErrorMessageRunes はエラーに載せる文言の長さの上限。
	maxErrorMessageRunes = 300
	// maxDrainBytes は接続を再利用するために読み捨てる本文の上限。
	maxDrainBytes = 64 << 10
)

// loginRequiredMessage は Redash が未認証のときに /api/ 配下で返す文言。
//
// redash/authentication/__init__.py の redirect_to_login にハードコードされた
// 英語の文字列で、翻訳されない。
const loginRequiredMessage = "Couldn't find resource"

// httpError は 2xx 以外の応答をエラーに変える。
func (c *Client) httpError(resp *http.Response) error {
	msg := c.errorMessage(resp)

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return &AuthError{StatusCode: resp.StatusCode, Message: msg}
	case resp.StatusCode == http.StatusNotFound && strings.Contains(msg, loginRequiredMessage):
		// Redash は API KEY が違うとき 401 ではなく 404 を返す。
		// そのまま「見つかりません」と伝えると原因に辿り着けない。
		return &AuthError{StatusCode: resp.StatusCode, Message: msg}
	default:
		return &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
}

// errorMessage はエラー応答から利用者に見せる文言を取り出す。
func (c *Client) errorMessage(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return ""
	}

	// Redash のエラー本文は2種類ある。
	//   {"job": {"status": 4, "error": "..."}}  query_results.py の error_response
	//   {"message": "..."}                      flask_restful の abort と未認証時
	var parsed struct {
		Message string `json:"message"`
		Job     *struct {
			Error string `json:"error"`
		} `json:"job"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if parsed.Job != nil && parsed.Job.Error != "" {
			return c.cleanMessage(parsed.Job.Error)
		}
		if parsed.Message != "" {
			return c.cleanMessage(parsed.Message)
		}
	}
	// JSON でなければ本文の先頭を見せる。プロキシやロードバランサが
	// 返した HTML でも、何が応答したのかの手がかりにはなる。
	return c.cleanMessage(string(raw))
}

// cleanMessage は応答由来の文字列をエラーに載せられる形に整える。
//
// 秘密が混ざりうる経路（HTTP の本文、ジョブの error）はこちらを通す。
func (c *Client) cleanMessage(s string) string {
	return clipMessage(c.scrub(s))
}

// clipMessage は改行を潰し、長さに上限をかける。
//
// Client を持たない場所（ジョブ ID・列名の検証）から使う。これらは
// API KEY が混ざる経路ではないが、長さは Redash 次第であり、
// 数 KB の文字列をそのまま端末に吐かせない。
func clipMessage(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > maxErrorMessageRunes {
		return string(r[:maxErrorMessageRunes]) + "..."
	}
	return string(r)
}
