// Package app はコマンドのドメインロジックを持つ。CLI フレームワークを import しない。
package app

import "io"

// Deps はコマンド実行に要るプロセスの状態。main が組み立てて注入する。
type Deps struct {
	// Out はデータの出力先。
	Out io.Writer
	// Err は警告・マスクサマリの出力先。
	Err io.Writer
	// In は標準入力。
	In io.Reader
	// TTY は Out が対話端末かどうか。table 出力の装飾の有無だけを左右する。
	TTY bool
	// Dir は設定ファイル探索の起点。空ならカレントディレクトリ。
	Dir string
	// Environ は os.Environ() 形式の環境。nil ならプロセスの環境を使う。
	Environ []string
}
