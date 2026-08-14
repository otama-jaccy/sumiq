package cli

import (
	"context"

	"github.com/otama-jaccy/sumiq/internal/app"
	"github.com/otama-jaccy/sumiq/internal/output"
)

// QueryCmd は `sumiq query -d <name> --format <fmt> "<SQL>"` に対応する。
//
// -d/--data-source は必須で、名前のみを受け付ける（数値 ID は受け付けない）。
// --format に短縮形は割り当てない。-f は将来の --file、-o は --output のため予約する
// （ADR-0004 §1）。
type QueryCmd struct {
	DataSource string `short:"d" name:"data-source" required:"" help:"実行対象のデータソース名（設定で定義した名前のみ）"`
	Format     string `enum:"table,json,csv" default:"table" help:"出力形式 (table/json/csv)"`
	Config     string `name:"config" type:"path" help:"設定ファイルを明示指定する"`
	SQL        string `arg:"" help:"実行する SQL"`
}

// Run は引数を app.QueryParams に詰め替えるだけで、ロジックは持たない。
func (c *QueryCmd) Run(ctx context.Context, deps *app.Deps) error {
	return app.Query(ctx, *deps, app.QueryParams{
		DataSource: c.DataSource,
		Format:     output.Format(c.Format),
		ConfigPath: c.Config,
		SQL:        c.SQL,
	})
}
