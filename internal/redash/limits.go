package redash

import (
	"io"
	"net/http"
)

// DefaultMaxResponseBytes は Options.MaxResponseBytes の既定値。
//
// Query.RowLimit は rows 配列の要素数だけを絞る（Issue #16）。1行あたりの
// 値が極端に大きい応答や、rows 以外のフィールドが肥大化した応答までは
// 防げないため、行数と無関係にかける最後の防御線をここに置く。
//
// 具体的な値について「これだけあれば十分」という根拠は無い。実運用で
// この上限に当たる正当な結果が出た時点で、config に露出するか含めて
// 見直す。
const DefaultMaxResponseBytes = 512 << 20 // 512MiB

// limitBody は rc からの読み取りを limit バイトで打ち切る io.ReadCloser にする。
//
// net/http.MaxBytesReader をそのまま使う。「ちょうど上限」と「上限を
// 超えた」を区別する手筋（limit+1 バイトまで読んでから判定する）を自前で
// 持たない。第1引数の http.ResponseWriter はサーバ側の接続クローズ通知
// にのみ使われ、nil なら型アサーションが失敗するだけで安全に読み飛ばされる
// （net/http の maxBytesReader.Read 実装より）。クライアント側での
// nil 利用は同じ実装内のコメントが明示している
// ("The server code and client code both use maxBytesReader.")。
//
// 超えて読もうとした Read は *http.MaxBytesError を返す。net/http の
// ドキュメントが明記するとおり、Body を最後まで読み切らずに Close すると
// その接続は keep-alive として再利用されない
// （https://pkg.go.dev/net/http#Response の Body フィールドの説明）。
// 上限に達した応答はここで打ち切ることになるが、それは意図した挙動。
func limitBody(rc io.ReadCloser, limit int64) io.ReadCloser {
	return http.MaxBytesReader(nil, rc, limit)
}
