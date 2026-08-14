package config

import (
	"strings"
	"testing"
	"time"
)

func TestEnvConfig(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		check   func(*testing.T, *Config)
	}{
		{
			name:    "endpoint",
			environ: []string{"SUMIQ_REDASH_ENDPOINT=https://env.example.com"},
			check: func(t *testing.T, c *Config) {
				if c.Redash.Endpoint != "https://env.example.com" {
					t.Errorf("endpoint = %q", c.Redash.Endpoint)
				}
			},
		},
		{
			name:    "timeout",
			environ: []string{"SUMIQ_REDASH_TIMEOUT=90s"},
			check: func(t *testing.T, c *Config) {
				if got := c.Redash.Timeout.Duration(); got != 90*time.Second {
					t.Errorf("timeout = %v", got)
				}
			},
		},
		{
			name:    "poll_interval",
			environ: []string{"SUMIQ_REDASH_POLL_INTERVAL=2s"},
			check: func(t *testing.T, c *Config) {
				if got := c.Redash.PollInterval.Duration(); got != 2*time.Second {
					t.Errorf("poll_interval = %v", got)
				}
			},
		},
		{
			name:    "auto_limit",
			environ: []string{"SUMIQ_QUERY_AUTO_LIMIT=false"},
			check: func(t *testing.T, c *Config) {
				if c.Query.AutoLimit == nil || *c.Query.AutoLimit {
					t.Errorf("auto_limit = %v, want false", c.Query.AutoLimit)
				}
			},
		},
		{
			name:    "max_rows",
			environ: []string{"SUMIQ_QUERY_MAX_ROWS=42"},
			check: func(t *testing.T, c *Config) {
				if c.Query.MaxRows != 42 {
					t.Errorf("max_rows = %d", c.Query.MaxRows)
				}
			},
		},
		{
			name:    "on_exceed",
			environ: []string{"SUMIQ_QUERY_ON_EXCEED=truncate"},
			check: func(t *testing.T, c *Config) {
				if c.Query.OnExceed != OnExceedTruncate {
					t.Errorf("on_exceed = %q", c.Query.OnExceed)
				}
			},
		},
		{
			name:    "default_action",
			environ: []string{"SUMIQ_MASKING_DEFAULT_ACTION=redact"},
			check: func(t *testing.T, c *Config) {
				if c.Masking.DefaultAction != ActionRedact {
					t.Errorf("default_action = %q", c.Masking.DefaultAction)
				}
			},
		},
		{
			name:    "format",
			environ: []string{"SUMIQ_OUTPUT_FORMAT=csv"},
			check: func(t *testing.T, c *Config) {
				if c.Output.Format != FormatCSV {
					t.Errorf("format = %q", c.Output.Format)
				}
			},
		},
		{
			name:    "api_key",
			environ: []string{EnvAPIKey + "=k"},
			check: func(t *testing.T, c *Config) {
				if c.Redash.APIKey != "k" {
					t.Errorf("api_key = %q", c.Redash.APIKey)
				}
			},
		},
		{
			name:    "同じ名前が複数あれば後勝ち",
			environ: []string{"SUMIQ_QUERY_MAX_ROWS=1", "SUMIQ_QUERY_MAX_ROWS=2"},
			check: func(t *testing.T, c *Config) {
				if c.Query.MaxRows != 2 {
					t.Errorf("max_rows = %d, want 2", c.Query.MaxRows)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := envConfig(environLookup(tt.environ))
			if err != nil {
				t.Fatalf("envConfig() error = %v", err)
			}
			if cfg == nil {
				t.Fatal("envConfig() = nil, want 設定")
			}
			tt.check(t, cfg)
		})
	}
}

func TestEnvConfig_指定が無ければnil(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
	}{
		{name: "空", environ: []string{}},
		{name: "無関係な変数だけ", environ: []string{"PATH=/bin", "SUMIQ_UNKNOWN=x"}},
		// 空文字は「変数を消し忘れた」と区別できないので未設定として扱う。
		{name: "空文字は未設定扱い", environ: []string{"SUMIQ_REDASH_ENDPOINT="}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := envConfig(environLookup(tt.environ))
			if err != nil {
				t.Fatalf("envConfig() error = %v", err)
			}
			if cfg != nil {
				t.Errorf("envConfig() = %+v, want nil", cfg)
			}
		})
	}
}

func TestEnvConfig_エラー(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		wantMsg string
	}{
		{name: "duration が不正", environ: []string{"SUMIQ_REDASH_TIMEOUT=300"}, wantMsg: "duration として読めません"},
		{name: "duration が負", environ: []string{"SUMIQ_REDASH_POLL_INTERVAL=-1s"}, wantMsg: "負の値"},
		{name: "bool が不正", environ: []string{"SUMIQ_QUERY_AUTO_LIMIT=yes-please"}, wantMsg: "true / false"},
		{name: "整数が不正", environ: []string{"SUMIQ_QUERY_MAX_ROWS=いっぱい"}, wantMsg: "整数で指定してください"},
		{name: "on_exceed が不正", environ: []string{"SUMIQ_QUERY_ON_EXCEED=warn"}, wantMsg: `不正な値 "warn"`},
		{name: "default_action が不正", environ: []string{"SUMIQ_MASKING_DEFAULT_ACTION=hash"}, wantMsg: `不正な値 "hash"`},
		{name: "format が不正", environ: []string{"SUMIQ_OUTPUT_FORMAT=yaml"}, wantMsg: `不正な値 "yaml"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := envConfig(environLookup(tt.environ))
			if err == nil {
				t.Fatalf("envConfig() error = nil, want error。結果 = %+v", cfg)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("envConfig() error = %q, want に %q を含む", err.Error(), tt.wantMsg)
			}
			// どの環境変数が悪いのか分からないと直せない。
			name, _, _ := strings.Cut(tt.environ[0], "=")
			if !strings.Contains(err.Error(), name) {
				t.Errorf("envConfig() error = %q, want に %q を含む", err.Error(), name)
			}
		})
	}
}
