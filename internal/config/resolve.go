package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Options は Resolve の入力。ゼロ値で使える。
//
// プロセスの状態（カレントディレクトリ、環境変数、ホームディレクトリ）を
// すべてフィールドで差し替えられるようにしてある。設定の解決は分岐が多く、
// 実際に環境を書き換えるテストではレイヤの組み合わせを網羅できない。
type Options struct {
	// Dir は sumiq.yaml / sumiq.local.yaml の探索起点。空ならカレントディレクトリ。
	Dir string
	// ConfigPath は --config で明示指定されたパス。
	// 指定するとユーザ設定とリポジトリ設定の探索をまとめてスキップする。
	ConfigPath string
	// Environ は os.Environ() 形式の環境。nil ならプロセスの環境を使う。
	Environ []string
	// UserConfigPath は ~/.config/sumiq/config.yaml のパス。
	// 空なら XDG_CONFIG_HOME / HOME から決める。
	UserConfigPath string
	// Tracked はファイルが git の管理下にあるかを判定する。nil なら git ls-files を使う。
	Tracked func(path string) (bool, error)
}

func (o Options) lookupEnv(name string) (string, bool) {
	if o.Environ == nil {
		return os.LookupEnv(name)
	}
	return environLookup(o.Environ)(name)
}

func (o Options) tracked(path string) (bool, error) {
	if o.Tracked != nil {
		return o.Tracked(path)
	}
	return gitTracked(path)
}

// userConfigPath はユーザ設定の位置を決める。決められなければ空を返す。
//
// os.UserConfigDir は macOS で ~/Library/Application Support を返すため使わない。
// ADR-0003 が定めているのは ~/.config/sumiq/config.yaml であり、
// sumiq は開発者が macOS と Linux で同じ場所に置けることを優先する。
func (o Options) userConfigPath() (string, error) {
	if o.UserConfigPath != "" {
		return o.UserConfigPath, nil
	}
	if dir, ok := o.lookupEnv("XDG_CONFIG_HOME"); ok && dir != "" {
		return filepath.Join(dir, "sumiq", "config.yaml"), nil
	}
	home, ok := o.lookupEnv("HOME")
	if !ok || home == "" {
		// HOME が無い環境（一部の CI やコンテナ）でも、リポジトリ設定だけで動くべき。
		return "", nil
	}
	return filepath.Join(home, ".config", "sumiq", "config.yaml"), nil
}

// defaults は埋め込みデフォルト（レイヤ1）を返す。
//
// 値の根拠は ADR-0003 §8・§10 と ADR-0004 §2。
// masking.default_action を none にしているのはグローバルを緩く保つ意図的な
// 判断であり（ADR-0003 §8）、上書きは厳しくする方向にしか効かないため、
// ここを最も緩い値にしておかないと利用者が redact 以外を選べなくなる。
func defaults() *Config {
	autoLimit := true
	return &Config{
		Version: SchemaVersion,
		Redash: Redash{
			Timeout:      Duration(300 * time.Second),
			PollInterval: Duration(time.Second),
		},
		Query: Query{
			AutoLimit: &autoLimit,
			MaxRows:   1000,
			OnExceed:  OnExceedError,
		},
		Masking: Masking{DefaultAction: ActionNone},
		Output:  Output{Format: FormatTable},
	}
}

// Resolve は設定ファイルを探索し、レイヤ順にマージして API KEY を解決する。
//
// 読む順序と勝ち負けは Layer に、項目ごとのマージ規則は merge.go の
// 先頭のコメントにまとめてある。
//
// コマンドライン引数（レイヤ6）はここでは扱わない。internal/cli が
// 戻り値に対して上書きする。
func Resolve(opts Options) (*Resolved, error) {
	files, err := discover(opts)
	if err != nil {
		return nil, err
	}

	layers := []layered{{layer: LayerDefault, cfg: defaults()}}
	for _, d := range files {
		cfg, err := LoadFile(d.path)
		if err != nil {
			return nil, err
		}
		layers = append(layers, layered{layer: d.layer, path: d.path, cfg: cfg})
	}

	envCfg, err := envConfig(opts.lookupEnv)
	if err != nil {
		return nil, err
	}
	if envCfg != nil {
		layers = append(layers, layered{layer: LayerEnv, cfg: envCfg})
	}

	res, err := merge(layers)
	if err != nil {
		return nil, err
	}

	// 検査は解決より先。負けたレイヤの api_key も対象なので、
	// 「使われる値かどうか」を待つ理由が無い。
	if err := checkAPIKeyFiles(res.apiKeyFiles, opts); err != nil {
		return nil, err
	}

	key, err := res.keySource.resolve(opts)
	if err != nil {
		return nil, err
	}
	res.APIKey = key
	return res, nil
}

// Validate は接続に必要な値が揃っているかを見る。
//
// Load（1枚分の検証）が必須にできなかった項目をここで見る。endpoint のように
// どのレイヤで埋まっても構わないものは、全部を畳んだ後でなければ判定できない。
//
// Resolve と分けてあるのは、設定の中身を見せるだけのコマンド（設定のダンプ等）が
// endpoint も API KEY も無い状態で動けるべきだから。Resolve の時点で落とすとそれができない。
//
// auto_limit と max_rows の整合検証はここではなく行数ガード側（#6）で行う。
func (r *Resolved) Validate() error {
	if r.Config.Redash.Endpoint == "" {
		return fmt.Errorf("redash.endpoint が指定されていません。%s に書いてください", SharedFileName)
	}
	if r.APIKey == "" {
		return fmt.Errorf("Redash の API KEY がありません。環境変数 %s、%s の redash.api_key、"+
			"redash.api_key_command のいずれかで渡してください", EnvAPIKey, LocalFileName)
	}
	return nil
}
