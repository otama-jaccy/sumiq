package redash

import (
	"fmt"
	"strings"
)

// ジョブ状態。redash/serializers/__init__.py の serialize_job が rq の
// JobStatus をこの数値に畳んでいる。
const (
	jobQueued    = 1
	jobStarted   = 2
	jobFinished  = 3
	jobFailed    = 4
	jobCanceled  = 5
	jobDeferred  = 6
	jobScheduled = 7
)

// jobEnvelope は POST /api/query_results と GET /api/jobs/{id} の応答。
//
// どちらも {"job": {...}} で返る。エラー応答（HTTP 4xx）も同じ形を取るため
// （query_results.py の error_response）、本文の形では成否を判定できない。
type jobEnvelope struct {
	Job *job `json:"job"`
}

// job は Redash のジョブ1つ分。
type job struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Error  string `json:"error"`
	// QueryResultID は完了時に取得すべき結果の ID。未完了なら null。
	QueryResultID *int64 `json:"query_result_id"`
}

// finished はジョブが完了したかを返す。失敗していればエラーを返す。
//
// 判定は error を先に見る。Redash はジョブの失敗を HTTP 200 で返すうえ、
// serialize_job は error が入る経路で status も 4 に落とすが、こちらが
// 依存しているのは「エラー文言があれば失敗」という一段強い条件の方。
// status の対応表が変わっても失敗を取りこぼさない。
//
// Client のメソッドにしてあるのは、Redash の文言をそのまま JobError に
// 移さないため。ジョブの error にはクエリランナーの例外がそのまま入り、
// 数 KB の traceback になることがある。HTTP 由来の文言と同じく
// cleanMessage を通し、API KEY の伏せ字と長さの上限を効かせる。
func (c *Client) finished(j *job) (bool, error) {
	if j.Error != "" {
		return false, &JobError{JobID: j.ID, Status: j.Status, Message: c.cleanMessage(j.Error)}
	}

	switch j.Status {
	case jobFinished:
		return true, nil
	case jobFailed, jobCanceled:
		// error が空のまま失敗して返ってくることがありうる。文言が無くても
		// 成功として扱ってはいけない。
		return false, &JobError{JobID: j.ID, Status: j.Status}
	case jobQueued, jobStarted, jobDeferred, jobScheduled:
		return false, nil
	default:
		// 知らない状態を「まだ動いている」に倒すと、timeout まで無駄に待った末に
		// タイムアウトとして報告することになる。何が起きたか分かる形で落とす。
		return false, fmt.Errorf("Redash が知らないジョブ状態 %d を返しました (ジョブ %s)", j.Status, j.ID)
	}
}

// checkJobID は job_id を URL のパス要素として使ってよいかを確かめる。
//
// url.JoinPath は要素の中の "/" と "%" をエスケープせず、".." を辿って詰める
// （実測: JoinPath("api","jobs","../../evil") => "/evil"）。job_id は Redash の
// 応答から来る値なので、こちらが検証しない限り、応答次第で GET 先を
// 同じホストの別のパスに移せてしまう。rq の job_id は uuid4 であり、
// これらの文字が入ることはない。
func checkJobID(id string) error {
	if id == "" {
		return fmt.Errorf("Redash の応答にジョブ ID がありません")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, "/%") {
		return fmt.Errorf("Redash が URL に使えないジョブ ID を返しました: %q", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("Redash が URL に使えないジョブ ID を返しました: %q", id)
		}
	}
	return nil
}
