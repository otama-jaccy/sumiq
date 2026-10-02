package redash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// SaveQuery は保存クエリ1つ分の入力。
type SaveQuery struct {
	// Name は保存クエリの名前。重複検出のキーを兼ねる。
	Name string
	// SQL は保存するクエリ本文。
	SQL string
	// DataSourceID は Redash の data_source_id。
	DataSourceID int
	// Tag は保存クエリに付けるタグ。重複検出はこのタグの付いた自分のクエリだけを見る。
	Tag string
	// AutoLimit は ad-hoc 実行に渡した apply_auto_limit。保存クエリの options に載せる。
	//
	// Query.query_hash は options.apply_auto_limit を適用した SQL から作られ
	// （redash/models/__init__.py の update_query_hash）、作成時に同じハッシュの
	// 最新結果が紐付く（QueryListResource.post の update_latest_result_by_query_hash）。
	// 揃えておけば、人間が画面で Execute したときも agent と同じ LIMIT で走る。
	AutoLimit bool
}

// SavedQuery は保存クエリの作成・再利用の結果。
type SavedQuery struct {
	ID int64
	// URL は人間がブラウザで開く画面の URL（<endpoint>/queries/<id>）。
	URL string
	// IsDraft は応答の is_draft。再利用したクエリは人間が公開済みにしていることがある。
	IsDraft bool
	// Reused は既存の保存クエリを返したかどうか。
	Reused bool
}

// savedQuery は /api/queries 系の応答に含まれるクエリ1件分。
//
// is_draft は Query.is_draft（db.Boolean のカラム）を serialize_query が
// そのまま載せる値で、DataSource.paused のような int への化けは無い
// （redash/models/__init__.py、redash/serializers/__init__.py）。
type savedQuery struct {
	ID           int64        `json:"id"`
	Name         string       `json:"name"`
	Query        string       `json:"query"`
	DataSourceID int          `json:"data_source_id"`
	IsDraft      bool         `json:"is_draft"`
	Tags         []string     `json:"tags"`
	Options      queryOptions `json:"options"`
}

// queryOptions は保存クエリの options のうち sumiq が書くもの。
type queryOptions struct {
	ApplyAutoLimit bool `json:"apply_auto_limit"`
}

// savedQueryPage は GET /api/queries/my の応答（redash/handlers/base.py の paginate）。
type savedQueryPage struct {
	Count   int          `json:"count"`
	Results []savedQuery `json:"results"`
}

const (
	// savedQueryPageSize は paginate が受け付ける page_size の上限。
	savedQueryPageSize = 250
	// maxSavedQueryPages は重複検出で読むページ数の上限。新しい順に読むため、
	// 再利用すべきクエリは通常先頭に近い。上限まで見つからなければ作り直す
	// （重複は増えるが、別の SQL を返す誤りにはならない）。
	maxSavedQueryPages = 20
)

// Save は q と同じ保存クエリが自分のものとして既にあればそれを返し、
// 無ければ draft で作る。
//
// 重複検出に全文検索（?q=）は使わない。search_vector は tsvector で、
// 名前に入れたハッシュが1語として索引される保証が無く、組織設定
// multi_byte_search で照合方法自体が ilike に変わる。タグで絞った一覧を
// 名前・SQL・データソース・apply_auto_limit の完全一致で照合する。SQL まで見るのは、
// 人間が画面で SQL を書き換えた保存クエリを、別の SQL の URL として返さないため。
func (c *Client) Save(ctx context.Context, q SaveQuery) (*SavedQuery, error) {
	if q.Name == "" || q.Tag == "" {
		return nil, errors.New("保存クエリの名前とタグは必須です")
	}
	if q.DataSourceID <= 0 {
		return nil, fmt.Errorf("data_source_id は 1 以上で指定してください: %d", q.DataSourceID)
	}

	execCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	saved, err := c.save(execCtx, q)
	if err != nil {
		return nil, c.classifyContextErr(ctx, execCtx, PhaseSaveQuery, "", err)
	}
	return saved, nil
}

func (c *Client) save(ctx context.Context, q SaveQuery) (*SavedQuery, error) {
	existing, err := c.findSavedQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return c.toSavedQuery(existing, true)
	}

	created, err := c.createSavedQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	saved, err := c.toSavedQuery(created, false)
	if err != nil {
		return nil, err
	}
	if created.IsDraft {
		return saved, nil
	}

	// 作成時の is_draft を無視して公開状態で作る Redash がある
	// （https://discuss.redash.io/t/api-for-importing-queries-doesnt-respect-is-draft-and-seems-to-lack-update-ability/1808）。
	// 現行の QueryListResource.post は is_draft を True に固定するが、古い版のために更新し直す。
	updated, err := c.markDraft(ctx, created.ID)
	if err != nil {
		return nil, fmt.Errorf("保存クエリ %s を作成しましたが、draft にできませんでした: %w", saved.URL, err)
	}
	saved.IsDraft = updated.IsDraft
	return saved, nil
}

// findSavedQuery は q と一致する自分の保存クエリを探す。無ければ nil。
//
// /api/queries/my は Query.by_user で、アーカイブ済みを含まない。人間が
// アーカイブしたクエリは再利用せず作り直す。
func (c *Client) findSavedQuery(ctx context.Context, q SaveQuery) (*savedQuery, error) {
	for page := 1; page <= maxSavedQueryPages; page++ {
		params := url.Values{}
		params.Set("tags", q.Tag)
		params.Set("order", "-created_at")
		params.Set("page", strconv.Itoa(page))
		params.Set("page_size", strconv.Itoa(savedQueryPageSize))

		var p savedQueryPage
		if err := c.do(ctx, http.MethodGet, c.resolve("api", "queries", "my")+"?"+params.Encode(), nil, &p); err != nil {
			return nil, err
		}
		for i := range p.Results {
			r := &p.Results[i]
			if r.Name == q.Name && r.Query == q.SQL && r.DataSourceID == q.DataSourceID &&
				r.Options.ApplyAutoLimit == q.AutoLimit && slices.Contains(r.Tags, q.Tag) {
				return r, nil
			}
		}
		// paginate は範囲外のページに 400 を返すため、count を超えて読まない。
		if len(p.Results) == 0 || page*savedQueryPageSize >= p.Count {
			return nil, nil
		}
	}
	return nil, nil
}

// createSavedQuery は POST /api/queries で保存クエリを作る。
func (c *Client) createSavedQuery(ctx context.Context, q SaveQuery) (*savedQuery, error) {
	body, err := json.Marshal(struct {
		Name         string       `json:"name"`
		Query        string       `json:"query"`
		DataSourceID int          `json:"data_source_id"`
		Tags         []string     `json:"tags"`
		IsDraft      bool         `json:"is_draft"`
		Options      queryOptions `json:"options"`
	}{
		Name:         q.Name,
		Query:        q.SQL,
		DataSourceID: q.DataSourceID,
		Tags:         []string{q.Tag},
		IsDraft:      true,
		Options:      queryOptions{ApplyAutoLimit: q.AutoLimit},
	})
	if err != nil {
		return nil, fmt.Errorf("リクエストを組み立てられませんでした: %w", err)
	}

	var created savedQuery
	if err := c.do(ctx, http.MethodPost, c.resolve("api", "queries"), body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// markDraft は POST /api/queries/{id} で is_draft を true にする。
func (c *Client) markDraft(ctx context.Context, id int64) (*savedQuery, error) {
	var updated savedQuery
	if err := c.do(ctx, http.MethodPost, c.resolve("api", "queries", strconv.FormatInt(id, 10)),
		[]byte(`{"is_draft":true}`), &updated); err != nil {
		return nil, err
	}
	if !updated.IsDraft {
		return nil, errors.New("Redash は is_draft: false のまま応答しました")
	}
	return &updated, nil
}

// toSavedQuery は応答を SavedQuery にする。ID は数値として検証してから URL に使う。
func (c *Client) toSavedQuery(q *savedQuery, reused bool) (*SavedQuery, error) {
	if q.ID <= 0 {
		return nil, fmt.Errorf("Redash の応答に保存クエリの ID がありません: %d", q.ID)
	}
	return &SavedQuery{
		ID:      q.ID,
		URL:     c.resolve("queries", strconv.FormatInt(q.ID, 10)),
		IsDraft: q.IsDraft,
		Reused:  reused,
	}, nil
}
