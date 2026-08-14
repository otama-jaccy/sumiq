package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

// Load は設定ファイル1枚を読み込む。
//
// 未知のキーはエラーにする。patern: のようなタイポを黙って無視すると
// マスクルールが1つ消えたまま動くため、この領域では未知キーを必ず落とす。
func Load(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("設定が空です")
		}
		return nil, err
	}

	// 2つ目以降のドキュメントは黙って捨てず落とす。--- で区切った設定が
	// 読まれないまま動くのは、未知キーを無視するのと同じ種類の事故になる。
	var rest yaml.Node
	err := dec.Decode(&rest)
	switch {
	case err == nil:
		return nil, errors.New("複数の YAML ドキュメントは扱えません。--- で区切らず1つにまとめてください")
	case !errors.Is(err, io.EOF):
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadFile は path の設定ファイルを読み込む。
func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg, err := Load(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// validate はファイル1枚だけで判定できる検証を行う。
//
// endpoint のようにレイヤをまたいで充足されうる項目は必須にしない。
// ローカルファイルは api_key とマスクルールだけを持つのが正しい姿であり、
// 1枚に閉じた検証で必須にすると、正しい設定を落としてしまう。
// 検証するのはリスト項目のように1枚の中で完結しているものに限る。
func (c *Config) validate() error {
	if c.Version == 0 {
		return fmt.Errorf("version が指定されていません。version: %d を書いてください", SchemaVersion)
	}
	if c.Version != SchemaVersion {
		return fmt.Errorf("version: %d は扱えません。対応しているのは %d だけです", c.Version, SchemaVersion)
	}

	for i, ds := range c.DataSources {
		if ds.Name == "" {
			return fmt.Errorf("data_sources[%d]: name が指定されていません", i)
		}
		if ds.ID <= 0 {
			return fmt.Errorf("data_sources[%d] (%s): id は 1 以上で指定してください", i, ds.Name)
		}
	}

	for i, r := range c.Masking.Rules {
		if len(r.Patterns) == 0 {
			return fmt.Errorf("masking.rules[%d]: patterns が空です。マッチしないルールは書けません", i)
		}
		if r.Method == "" {
			return fmt.Errorf("masking.rules[%d] %v: method が指定されていません。"+
				"YAML では引用符なしの null は空値として解釈されるため、"+
				"マスク方法の null を指定する場合は method: \"null\" と引用してください", i, r.Patterns)
		}
	}

	return nil
}
