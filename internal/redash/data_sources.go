package redash

import (
	"context"
	"net/http"
)

// DataSource は Redash 上のデータソース1件分。
type DataSource struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Paused bool   `json:"paused"`
}

// ListDataSources は GET /api/data_sources を叩き、Redash 上のデータソース
// 一覧を返す。
//
// 応答は {"job": ...} のような envelope ではなく素の JSON 配列であり、
// job 投入・ポーリングを伴う Execute とは別経路（同期・単発の GET）。
// c.timeout だけを上限にする。
func (c *Client) ListDataSources(ctx context.Context) ([]DataSource, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var ds []DataSource
	if err := c.do(ctx, http.MethodGet, c.resolve("api", "data_sources"), nil, &ds); err != nil {
		return nil, err
	}
	return ds, nil
}
