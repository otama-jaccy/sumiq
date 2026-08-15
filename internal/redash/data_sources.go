package redash

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// DataSource は Redash 上のデータソース1件分。
type DataSource struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Paused bool   `json:"paused"`
}

// UnmarshalJSON は paused の型ゆれを吸収する。
//
// paused は redash/models/__init__.py の DataSource.paused プロパティが
// redis_connection.exists(...) をそのまま返す実装で、redis-py の
// Redis.exists() は bool ではなく int（0 または 1）を返す
// （redis-py の commands/core.py の型注釈）。そのため Redash の応答は
// "paused": true/false ではなく "paused": 0/1 という数値になる。
func (d *DataSource) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID     int         `json:"id"`
		Name   string      `json:"name"`
		Type   string      `json:"type"`
		Paused pausedField `json:"paused"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	d.ID = raw.ID
	d.Name = raw.Name
	d.Type = raw.Type
	d.Paused = bool(raw.Paused)
	return nil
}

// pausedField は JSON の bool（true/false）と 0/1 の数値のどちらでも
// 読める paused 専用の型。
type pausedField bool

func (p *pausedField) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "true", "1":
		*p = true
	case "false", "0":
		*p = false
	default:
		return fmt.Errorf("paused の値を bool として読めません: %s", clipMessage(string(b)))
	}
	return nil
}

// ListDataSources は GET /api/data_sources を叩き、Redash 上のデータソース
// 一覧を返す。
//
// 応答は {"job": ...} のような envelope ではなく素の JSON 配列であり、
// job 投入・ポーリングを伴う Execute とは別経路（同期・単発の GET）。
// c.timeout だけを上限にする。
func (c *Client) ListDataSources(ctx context.Context) ([]DataSource, error) {
	execCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var ds []DataSource
	if err := c.do(execCtx, http.MethodGet, c.resolve("api", "data_sources"), nil, &ds); err != nil {
		return nil, c.classifyContextErr(ctx, execCtx, PhaseListDataSources, "", err)
	}
	return ds, nil
}
