package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EnvAPIKey は API KEY を渡す環境変数の名前。
const EnvAPIKey = "SUMIQ_REDASH_API_KEY"

// envVars は環境変数名と、それが設定する項目の対応。
//
// 対応表を明示的に持ち、リフレクションでキー名を機械的に組み立てることはしない。
// 環境変数はシェルの履歴や CI の設定に散らばって残るため、名前は構造体の
// リネームで勝手に変わってよいものではない。
//
// リスト（data_sources / masking.rules）は環境変数から設定できない。
// 1つの文字列に押し込む記法を用意すると、マスクルールをシェル変数から
// 書けることになり、レビューを迂回する経路になる。
func envVars() []envVar {
	return []envVar{
		{name: "SUMIQ_REDASH_ENDPOINT", apply: func(c *Config, v string) error {
			c.Redash.Endpoint = v
			return nil
		}},
		{name: EnvAPIKey, apply: func(c *Config, v string) error {
			c.Redash.APIKey = v
			return nil
		}},
		{name: "SUMIQ_REDASH_TIMEOUT", apply: func(c *Config, v string) error {
			d, err := parseEnvDuration(v)
			c.Redash.Timeout = d
			return err
		}},
		{name: "SUMIQ_REDASH_POLL_INTERVAL", apply: func(c *Config, v string) error {
			d, err := parseEnvDuration(v)
			c.Redash.PollInterval = d
			return err
		}},
		{name: "SUMIQ_QUERY_AUTO_LIMIT", apply: func(c *Config, v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("true / false で指定してください: %q", v)
			}
			c.Query.AutoLimit = &b
			return nil
		}},
		{name: "SUMIQ_QUERY_MAX_ROWS", apply: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("整数で指定してください: %q", v)
			}
			c.Query.MaxRows = n
			return nil
		}},
		{name: "SUMIQ_QUERY_ON_EXCEED", apply: func(c *Config, v string) error {
			return assignEnum(v, onExceeds(), &c.Query.OnExceed)
		}},
		{name: "SUMIQ_MASKING_DEFAULT_ACTION", apply: func(c *Config, v string) error {
			return assignEnum(v, actions(), &c.Masking.DefaultAction)
		}},
		{name: "SUMIQ_OUTPUT_FORMAT", apply: func(c *Config, v string) error {
			return assignEnum(v, formats(), &c.Output.Format)
		}},
	}
}

type envVar struct {
	name  string
	apply func(*Config, string) error
}

// envConfig は環境変数レイヤを Config 1枚に組み立てる。
//
// ファイルと同じ形にしておくことで、マージ規則（default_action は厳しくする
// 方向のみ、等）が環境変数にもそのまま効く。環境変数だけ別扱いにすると、
// SUMIQ_MASKING_DEFAULT_ACTION=none でマスクを緩められる穴が開く。
func envConfig(lookup func(string) (string, bool)) (*Config, error) {
	var cfg Config
	var set bool
	for _, ev := range envVars() {
		v, ok := lookup(ev.name)
		if !ok {
			continue
		}
		// 空文字は「変数を消し忘れた」と区別できないため未設定として扱う。
		if v == "" {
			continue
		}
		if err := ev.apply(&cfg, v); err != nil {
			return nil, fmt.Errorf("%s: %w", ev.name, err)
		}
		set = true
	}
	if !set {
		return nil, nil
	}
	cfg.Version = SchemaVersion
	return &cfg, nil
}

func parseEnvDuration(v string) (Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("duration として読めません %q。\"300s\" のように単位を付けてください", v)
	}
	if d < 0 {
		return 0, fmt.Errorf("負の値は指定できません %q", v)
	}
	return Duration(d), nil
}

func assignEnum[T ~string](v string, allowed []T, dst *T) error {
	for _, a := range allowed {
		if T(v) == a {
			*dst = a
			return nil
		}
	}
	return fmt.Errorf("不正な値 %q。指定できるのは %s", v, quotedList(allowed))
}

// environLookup は os.Environ() 形式のスライスを引ける形にする。
//
// 環境をスライスで受け取れるようにしているのは、テストからプロセスの環境を
// 触らずに済ませるため。os.Setenv はテスト間で状態が漏れる。
func environLookup(environ []string) func(string) (string, bool) {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		// 同じ名前が複数あれば後勝ち。exec が渡す環境と同じ扱いにする。
		m[k] = v
	}
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}
