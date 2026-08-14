// Package redash は Redash の ad-hoc クエリ API を叩く。
//
// ad-hoc クエリは3段構えになっている（ADR-0003「Redash API の制約」）。
//
//  1. POST /api/query_results        ジョブを投入する
//  2. GET  /api/jobs/{job_id}        完了するまでポーリングする
//  3. GET  /api/query_results/{id}   結果を取得する
//
// このパッケージは internal/config を import しない。設定の解決結果から
// 必要な値だけを Options に詰め替えて渡す。設定のレイヤ構造は Redash の
// 都合とは無関係であり、ここに持ち込むと API クライアントを設定ファイル
// 抜きにテストできなくなる。
package redash

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options は New の入力。
type Options struct {
	// Endpoint は Redash のベース URL。パス付き（https://host/redash）でもよい。
	Endpoint string
	// APIKey は Authorization ヘッダに載せる API KEY。
	APIKey string
	// Timeout は Execute 1回の待ち上限。0 なら DefaultTimeout。
	Timeout time.Duration
	// PollInterval は /api/jobs/{id} を叩く間隔。0 なら DefaultPollInterval。
	PollInterval time.Duration
	// HTTPClient は nil なら既定のものを作る。テストから差し替える。
	HTTPClient *http.Client
}

const (
	// DefaultTimeout は Options.Timeout の既定値。ADR-0003 §3 の redash.timeout に合わせる。
	DefaultTimeout = 300 * time.Second
	// DefaultPollInterval は Options.PollInterval の既定値。
	DefaultPollInterval = time.Second
)

// Client は Redash の API クライアント。New で作る。
//
// フィールドは生成後に変更しない。並行して Execute を呼べる。
type Client struct {
	endpoint     *url.URL
	apiKey       string
	timeout      time.Duration
	pollInterval time.Duration
	httpClient   *http.Client
}

// New は Client を作る。
//
// APIKey が空でも作れる。認証が要るかどうかは Redash 側の設定であり、
// ここで必須にすると API KEY 無しで動く構成を弾いてしまう。値が必要かの
// 判定は設定側（config.Resolved.Validate）が持つ。
func New(opts Options) (*Client, error) {
	u, err := parseEndpoint(opts.Endpoint)
	if err != nil {
		return nil, err
	}

	c := &Client{
		endpoint:     u,
		apiKey:       opts.APIKey,
		timeout:      opts.Timeout,
		pollInterval: opts.PollInterval,
		httpClient:   opts.HTTPClient,
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.pollInterval <= 0 {
		c.pollInterval = DefaultPollInterval
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	return c, nil
}

// parseEndpoint はベース URL を検証する。
func parseEndpoint(endpoint string) (*url.URL, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("redash.endpoint が空です")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		// err には endpoint がそのまま入る。API KEY ではないので出してよい。
		return nil, fmt.Errorf("redash.endpoint を URL として読めません: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("redash.endpoint は http:// か https:// で始めてください: %q", endpoint)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("redash.endpoint にホストがありません: %q", endpoint)
	}
	// URL に埋め込まれた認証情報は受け付けない。net/http は url.Error を組み立てる
	// ときにパスワードを伏せるが、こちらが自前でエラーに URL を載せる箇所まで
	// 面倒を見てくれるわけではない。秘密を持ちうる値は最初から入れさせない。
	if u.User != nil {
		return nil, fmt.Errorf("redash.endpoint に user:password を含めることはできません。" +
			"API KEY は redash.api_key か環境変数で渡してください")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		// クエリや fragment を付けられると、api_key=... を書く逃げ道になる。
		// その値はリダイレクトやログに乗るため、ヘッダ経由に一本化する。
		return nil, fmt.Errorf("redash.endpoint にクエリや # を含めることはできません: %q", endpoint)
	}

	// 以降 JoinPath で組み立てるため、末尾のスラッシュだけ落として正規化する。
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// resolve はベース URL にパス要素を繋いだ URL を返す。
//
// url.JoinPath は要素の中の "/" と ".." をエスケープせず、パスとして解釈して
// 詰める（実測: JoinPath("api","jobs","../../evil") => /evil）。job_id は
// Redash の応答から来る値であり、こちらが検証しない限り任意のパスを指せる。
// 呼び出し側で要素を検証してから渡すこと。
func (c *Client) resolve(elem ...string) string {
	return c.endpoint.JoinPath(elem...).String()
}
