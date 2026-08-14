// Package cli はコマンド定義を持つ。kong を知る唯一の層。
//
// 各コマンドはタグ付き構造体 + Run() で表す。Run() は引数を詰め替えて
// internal/app を呼ぶだけに保ち、ロジックを持たない（ADR-0002）。
package cli

import (
	"context"

	"github.com/alecthomas/kong"

	"github.com/otama-jaccy/sumiq/internal/app"
)

// CLI はコマンドツリーのルート。
type CLI struct {
	Query QueryCmd `cmd:"" help:"SQL を実行して結果を出力する"`
}

// Execute は args をパースし、選択されたコマンドの Run() に ctx と deps を注入して実行する。
//
// ctx はここでは生成しない。プロセスの実行寿命（シグナルでの中断など）は
// main が決めるものであり、internal/cli はそれをそのまま下流に渡すだけに保つ。
func Execute(ctx context.Context, args []string, deps *app.Deps) error {
	var c CLI
	// kong はバインドされた値を具象型で引くため、context.Context のような
	// インタフェース型のパラメータには BindFor で明示的にインタフェース型を
	// 登録する必要がある（Bind だけでは実装型でしか引けない）。
	parser, err := kong.New(&c, kong.BindFor(ctx))
	if err != nil {
		return err
	}
	kongCtx, err := parser.Parse(args)
	if err != nil {
		return err
	}
	return kongCtx.Run(deps)
}
