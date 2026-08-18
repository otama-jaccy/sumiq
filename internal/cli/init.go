package cli

import (
	"github.com/otama-jaccy/sumiq/internal/app"
)

// InitCmd は `sumiq init` に対応する。
//
// カレントディレクトリに sumiq.yaml / sumiq.local.yaml の雛形を作成する。
// 位置引数でディレクトリを取る機能は無い。書き込み先は常にカレントディレクトリ固定。
type InitCmd struct {
	Force bool `help:"既存の sumiq.yaml / sumiq.local.yaml を上書きする（両方に効く）"`
}

// Run は引数を app.InitParams に詰め替えるだけで、ロジックは持たない。
//
// ctx context.Context は受け取らない。ファイル I/O のみで外部通信が無いため。
func (c *InitCmd) Run(deps *app.Deps) error {
	return app.Init(*deps, app.InitParams{
		Force: c.Force,
	})
}
