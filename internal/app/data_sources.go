package app

import (
	"context"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/output"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// DataSourcesParams は ListDataSources 1回分の呼び出しパラメータ。
type DataSourcesParams struct {
	// Format は --format で指定された出力形式。
	Format output.Format
	// ConfigPath は --config で明示指定された設定ファイルのパス。
	ConfigPath string
}

// ListDataSources は設定を解決し、Redash 上のデータソース一覧を取得して出力する。
//
// sumiq.yaml の data_sources:（ローカルの許可リスト）は参照しない。このコマンドは
// Redash 側の一覧を取得するものであり、ローカル設定に定義済みかどうかとは無関係。
func ListDataSources(ctx context.Context, deps Deps, p DataSourcesParams) error {
	resolved, err := config.Resolve(config.Options{
		Dir:        deps.Dir,
		Environ:    deps.Environ,
		ConfigPath: p.ConfigPath,
	})
	if err != nil {
		return err
	}
	if err := resolved.Validate(); err != nil {
		return err
	}

	apiKey, err := resolved.APIKey()
	if err != nil {
		return err
	}
	client, err := redash.New(redash.Options{
		Endpoint: resolved.Config.Redash.Endpoint,
		APIKey:   apiKey,
		Timeout:  resolved.Config.Redash.Timeout.Duration(),
	})
	if err != nil {
		return err
	}

	ds, err := client.ListDataSources(ctx)
	if err != nil {
		return err
	}

	return output.RenderDataSources(deps.Out, p.Format, ds, deps.TTY)
}
