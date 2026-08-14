package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

// sharedYAML は ADR-0003 の共有ファイル例に output を足したもの。
// スキーマ側の取りこぼしは、まずこの例が通らないことで露見する。
const sharedYAML = `
version: 1

redash:
  endpoint: https://redash.example.com
  timeout: 300s
  poll_interval: 1s

data_sources:
  - name: analytics
    id: 3
    description: "本番リードレプリカ"
    default_action: redact
  - name: sandbox
    id: 7
    auto_limit: false

query:
  auto_limit: true
  max_rows: 1000
  on_exceed: error

masking:
  default_action: none
  rules:
    - patterns: ["*email*"]
      method: partial
      keep: domain
      note: "ドメインのみ残す。流入元の切り分けに使うため"

    - patterns: ["*phone*", "*tel"]
      method: redact

    - patterns: ["user_id", "customer_id"]
      method: hash

    - patterns: ["regex:^(first|last)_name$"]
      method: drop

    - patterns: ["memo", "note"]
      method: redact
      data_sources: [analytics]

output:
  format: json
`

// localYAML は ADR-0003 のローカルファイル例。
// endpoint も version 以外の必須項目も持たないが、1枚では有効でなければならない。
const localYAML = `
version: 1

redash:
  api_key: ${env:REDASH_API_KEY}
  api_key_command: ["op", "read", "op://Private/redash/credential"]

data_sources:
  - name: my-sandbox
    id: 99

masking:
  rules:
    - patterns: ["internal_memo"]
      method: redact
`

func TestLoad_ADRExamples(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want *Config
	}{
		{
			name: "共有ファイル",
			yaml: sharedYAML,
			want: &Config{
				Version: 1,
				Redash: Redash{
					Endpoint:     "https://redash.example.com",
					Timeout:      Duration(300 * time.Second),
					PollInterval: Duration(time.Second),
				},
				DataSources: []DataSource{
					{Name: "analytics", ID: 3, Description: "本番リードレプリカ", DefaultAction: ActionRedact},
					{Name: "sandbox", ID: 7, AutoLimit: ptr(false)},
				},
				Query: Query{AutoLimit: ptr(true), MaxRows: 1000, OnExceed: OnExceedError},
				Masking: Masking{
					DefaultAction: ActionNone,
					Rules: []MaskRule{
						{
							Patterns: []string{"*email*"},
							Method:   MaskPartial,
							Keep:     "domain",
							Note:     "ドメインのみ残す。流入元の切り分けに使うため",
						},
						{Patterns: []string{"*phone*", "*tel"}, Method: MaskRedact},
						{Patterns: []string{"user_id", "customer_id"}, Method: MaskHash},
						{Patterns: []string{"regex:^(first|last)_name$"}, Method: MaskDrop},
						{Patterns: []string{"memo", "note"}, Method: MaskRedact, DataSources: []string{"analytics"}},
					},
				},
				Output: Output{Format: FormatJSON},
			},
		},
		{
			name: "ローカルファイル",
			yaml: localYAML,
			want: &Config{
				Version: 1,
				Redash: Redash{
					// ${env:VAR} の展開はレイヤードマージ側で行うため、ここでは素の文字列。
					APIKey:        "${env:REDASH_API_KEY}",
					APIKeyCommand: []string{"op", "read", "op://Private/redash/credential"},
				},
				DataSources: []DataSource{{Name: "my-sandbox", ID: 99}},
				Masking: Masking{
					Rules: []MaskRule{{Patterns: []string{"internal_memo"}, Method: MaskRedact}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(strings.NewReader(tt.yaml))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// 未指定と明示的な false を取り違えないこと。
// ここを bool で受けると auto_limit: false がマージで消える。
func TestLoad_AutoLimitUnsetVsFalse(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want *bool
	}{
		{name: "未指定", yaml: "version: 1\nquery: {max_rows: 10}\n", want: nil},
		{name: "false", yaml: "version: 1\nquery: {auto_limit: false}\n", want: ptr(false)},
		{name: "true", yaml: "version: 1\nquery: {auto_limit: true}\n", want: ptr(true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(strings.NewReader(tt.yaml))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			switch {
			case tt.want == nil && got.Query.AutoLimit != nil:
				t.Errorf("auto_limit = %v, want 未指定", *got.Query.AutoLimit)
			case tt.want != nil && got.Query.AutoLimit == nil:
				t.Errorf("auto_limit = 未指定, want %v", *tt.want)
			case tt.want != nil && *got.Query.AutoLimit != *tt.want:
				t.Errorf("auto_limit = %v, want %v", *got.Query.AutoLimit, *tt.want)
			}
		})
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantMsg string // エラーメッセージに含まれるべき文字列
	}{
		// 未知キー。設定ミスが黙って通ることを許さない領域なので、どの階層でも落とす。
		{
			name:    "トップレベルの未知キー",
			yaml:    "version: 1\nredsah: {}\n",
			wantMsg: "redsah",
		},
		{
			name:    "ネストした未知キー",
			yaml:    "version: 1\nredash:\n  endpiont: https://example.com\n",
			wantMsg: "endpiont",
		},
		{
			name:    "リスト項目内の未知キー",
			yaml:    "version: 1\ndata_sources:\n  - name: a\n    id: 1\n    idd: 2\n",
			wantMsg: "idd",
		},
		{
			// これを取りこぼすとマスクルールが1つ消えたまま動く。
			name:    "マスクルールの patterns のタイポ",
			yaml:    "version: 1\nmasking:\n  rules:\n    - patern: [\"*email*\"]\n      method: redact\n",
			wantMsg: "patern",
		},

		// version
		{name: "version なし", yaml: "redash: {}\n", wantMsg: "version が指定されていません"},
		{name: "version が 2", yaml: "version: 2\n", wantMsg: "version: 2 は扱えません"},
		{name: "version が 0", yaml: "version: 0\n", wantMsg: "version が指定されていません"},
		// 型の不一致は yaml 側が弾く。yaml のエラーはフィールド名を含まないが行番号は付く。
		{name: "version が文字列", yaml: "version: \"1\"\n", wantMsg: "line 1"},

		// duration
		{name: "duration に単位なし", yaml: "version: 1\nredash: {timeout: 300}\n", wantMsg: "duration として読めません"},
		{name: "duration が不正", yaml: "version: 1\nredash: {timeout: しばらく}\n", wantMsg: "duration として読めません"},
		{name: "duration が負", yaml: "version: 1\nredash: {poll_interval: -1s}\n", wantMsg: "負の値"},

		// enum
		{name: "method が不正", yaml: "version: 1\nmasking:\n  rules:\n    - patterns: [a]\n      method: redct\n", wantMsg: `不正な値 "redct"`},
		{name: "on_exceed が不正", yaml: "version: 1\nquery: {on_exceed: warn}\n", wantMsg: `不正な値 "warn"`},
		{name: "default_action が不正", yaml: "version: 1\nmasking: {default_action: hash}\n", wantMsg: `不正な値 "hash"`},
		{name: "データソースの default_action が不正", yaml: "version: 1\ndata_sources: [{name: a, id: 1, default_action: drop}]\n", wantMsg: `不正な値 "drop"`},
		{name: "format が不正", yaml: "version: 1\noutput: {format: yaml}\n", wantMsg: `不正な値 "yaml"`},

		// リスト項目は1枚の中で完結しているので必須を検証する
		{name: "データソースに name がない", yaml: "version: 1\ndata_sources: [{id: 1}]\n", wantMsg: "name が指定されていません"},
		{name: "データソースに id がない", yaml: "version: 1\ndata_sources: [{name: a}]\n", wantMsg: "id は 1 以上"},
		{name: "patterns が空", yaml: "version: 1\nmasking:\n  rules:\n    - patterns: []\n      method: redact\n", wantMsg: "patterns が空です"},
		// 0 は未指定と区別できないので弾けないが、負の値は書き間違い以外にない。
		{name: "max_rows が負", yaml: "version: 1\nquery: {max_rows: -1}\n", wantMsg: "負の値は指定できません"},
		{name: "method がない", yaml: "version: 1\nmasking:\n  rules:\n    - patterns: [a]\n", wantMsg: "method が指定されていません"},

		// その他
		{name: "空ファイル", yaml: "", wantMsg: "設定が空です"},
		{name: "複数ドキュメント", yaml: "version: 1\n---\nversion: 1\n", wantMsg: "複数の YAML ドキュメント"},
		{name: "YAML として壊れている", yaml: "version: 1\n  bad indent\n", wantMsg: "yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(strings.NewReader(tt.yaml))
			if err == nil {
				t.Fatalf("Load() error = nil, want error。デコード結果 = %+v", got)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Load() error = %q, want に %q を含む", err.Error(), tt.wantMsg)
			}
		})
	}
}

// YAML では引用符なしの null は空値として解釈されるため、マスク方法の null は
// 引用しないと通らない。黙って未指定になると、その列がマスクされないまま出力される。
func TestLoad_MaskMethodNull(t *testing.T) {
	t.Run("引用すれば null を指定できる", func(t *testing.T) {
		cfg, err := Load(strings.NewReader("version: 1\nmasking:\n  rules:\n    - patterns: [a]\n      method: \"null\"\n"))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if got := cfg.Masking.Rules[0].Method; got != MaskNull {
			t.Errorf("method = %q, want %q", got, MaskNull)
		}
	})

	t.Run("引用しない null はエラーになり引用を促す", func(t *testing.T) {
		_, err := Load(strings.NewReader("version: 1\nmasking:\n  rules:\n    - patterns: [a]\n      method: null\n"))
		if err == nil {
			t.Fatal("Load() error = nil, want error")
		}
		if !strings.Contains(err.Error(), `method: "null"`) {
			t.Errorf("Load() error = %q, want に引用の案内を含む", err.Error())
		}
	})
}

func TestLoad_EnumValues(t *testing.T) {
	t.Run("method", func(t *testing.T) {
		for _, m := range maskMethods() {
			cfg, err := Load(strings.NewReader("version: 1\nmasking:\n  rules:\n    - patterns: [a]\n      method: \"" + string(m) + "\"\n"))
			if err != nil {
				t.Fatalf("method %q: Load() error = %v", m, err)
			}
			if got := cfg.Masking.Rules[0].Method; got != m {
				t.Errorf("method = %q, want %q", got, m)
			}
		}
	})

	t.Run("on_exceed", func(t *testing.T) {
		for _, o := range onExceeds() {
			cfg, err := Load(strings.NewReader("version: 1\nquery: {on_exceed: " + string(o) + "}\n"))
			if err != nil {
				t.Fatalf("on_exceed %q: Load() error = %v", o, err)
			}
			if got := cfg.Query.OnExceed; got != o {
				t.Errorf("on_exceed = %q, want %q", got, o)
			}
		}
	})

	t.Run("default_action", func(t *testing.T) {
		for _, a := range actions() {
			cfg, err := Load(strings.NewReader("version: 1\nmasking: {default_action: " + string(a) + "}\n"))
			if err != nil {
				t.Fatalf("default_action %q: Load() error = %v", a, err)
			}
			if got := cfg.Masking.DefaultAction; got != a {
				t.Errorf("default_action = %q, want %q", got, a)
			}
		}
	})

	t.Run("format", func(t *testing.T) {
		for _, f := range formats() {
			cfg, err := Load(strings.NewReader("version: 1\noutput: {format: " + string(f) + "}\n"))
			if err != nil {
				t.Fatalf("format %q: Load() error = %v", f, err)
			}
			if got := cfg.Output.Format; got != f {
				t.Errorf("format = %q, want %q", got, f)
			}
		}
	})
}

func TestLoad_Duration(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want time.Duration
	}{
		{name: "秒", yaml: "300s", want: 300 * time.Second},
		{name: "1秒", yaml: "1s", want: time.Second},
		{name: "分と秒", yaml: "1m30s", want: 90 * time.Second},
		{name: "ミリ秒", yaml: "500ms", want: 500 * time.Millisecond},
		{name: "ゼロ", yaml: "0s", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(strings.NewReader("version: 1\nredash: {timeout: " + tt.yaml + "}\n"))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got := cfg.Redash.Timeout.Duration(); got != tt.want {
				t.Errorf("timeout = %v, want %v", got, tt.want)
			}
		})
	}
}

// エラーメッセージは行番号を含む。設定ファイルは手で書くので、
// どの行が悪いかが分からないと直せない。
func TestLoad_ErrorHasLineNumber(t *testing.T) {
	_, err := Load(strings.NewReader("version: 1\nquery:\n  max_rows: 10\n  on_exceed: warn\n"))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("Load() error = %q, want に \"line 4\" を含む", err.Error())
	}
}

func TestLoadFile(t *testing.T) {
	t.Run("読み込める", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sumiq.yaml")
		if err := os.WriteFile(path, []byte(sharedYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFile(path)
		if err != nil {
			t.Fatalf("LoadFile() error = %v", err)
		}
		if cfg.Redash.Endpoint != "https://redash.example.com" {
			t.Errorf("endpoint = %q", cfg.Redash.Endpoint)
		}
	})

	t.Run("エラーにファイル名が付く", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken.yaml")
		if err := os.WriteFile(path, []byte("version: 2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadFile(path)
		if err == nil {
			t.Fatal("LoadFile() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "broken.yaml") {
			t.Errorf("LoadFile() error = %q, want にファイル名を含む", err.Error())
		}
	})

	t.Run("存在しないファイル", func(t *testing.T) {
		if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
			t.Fatal("LoadFile() error = nil, want error")
		}
	})
}
