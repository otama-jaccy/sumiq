package cli

import (
	"context"

	"github.com/otama-jaccy/sumiq/internal/app"
	"github.com/otama-jaccy/sumiq/internal/output"
)

// DataSourcesCmd は `sumiq data-sources <subcommand>` のコマンドグループ。
//
// 将来 `sumiq data-sources show <id>` 等を足す余地を残すため、最初から
// ネストしたコマンドグループとして作る。
type DataSourcesCmd struct {
	List ListCmd `cmd:"" name:"list" help:"Redash 上のデータソース一覧を表示する"`
}

// ListCmd は `sumiq data-sources list --format <fmt>` に対応する。
//
// 位置引数・-d/--data-source のような対象指定は無い。常に全件を取得する。
type ListCmd struct {
	Format string `enum:"table,json,csv" default:"table" help:"出力形式 (table/json/csv)"`
	Config string `name:"config" type:"path" help:"設定ファイルを明示指定する"`
}

// Run は引数を app.DataSourcesParams に詰め替えるだけで、ロジックは持たない。
func (c *ListCmd) Run(ctx context.Context, deps *app.Deps) error {
	return app.ListDataSources(ctx, *deps, app.DataSourcesParams{
		Format:     output.Format(c.Format),
		ConfigPath: c.Config,
	})
}
