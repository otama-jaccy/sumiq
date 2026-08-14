package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/otama-jaccy/sumiq/internal/app"
)

// kong のタグは型のない文字列 DSL で、検証は実行時にしか効かない
// （.claude/rules/go-architecture.md）。ここではタグどおりに振る舞うことを
// 実際に kong.New / Parse を走らせて確認する。

// newParser は空の CLI に対する kong パーサを作る。
func newParser(t *testing.T) (*kong.Kong, *CLI) {
	t.Helper()
	var c CLI
	parser, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	return parser, &c
}

func TestQueryCmd_DataSourceRequired(t *testing.T) {
	parser, _ := newParser(t)
	if _, err := parser.Parse([]string{"query", "SELECT 1"}); err == nil {
		t.Fatal("-d/--data-source を省略してもエラーになりませんでした")
	}
}

func TestQueryCmd_FormatDefaultAndEnum(t *testing.T) {
	parser, c := newParser(t)
	if _, err := parser.Parse([]string{"query", "-d", "analytics", "SELECT 1"}); err != nil {
		t.Fatalf("Parse() 失敗: %v", err)
	}
	if c.Query.Format != "table" {
		t.Errorf("Format の既定値が table ではありません: %q", c.Query.Format)
	}
	if c.Query.DataSource != "analytics" {
		t.Errorf("DataSource が詰め替わっていません: %q", c.Query.DataSource)
	}
	if c.Query.SQL != "SELECT 1" {
		t.Errorf("SQL が詰め替わっていません: %q", c.Query.SQL)
	}
}

func TestQueryCmd_FormatRejectsUnknownValue(t *testing.T) {
	parser, _ := newParser(t)
	if _, err := parser.Parse([]string{"query", "-d", "analytics", "--format", "xml", "SELECT 1"}); err == nil {
		t.Fatal("format: xml を通してしまいました。enum で弾かれるべきです")
	}
}

func TestQueryCmd_NoShortFlagForFormatOrConfig(t *testing.T) {
	// -f / -o は将来の --file / --output のため予約する（ADR-0004 §1）。
	// --format / --config に短縮形を割り当てていないことを、-f が
	// 未知のフラグとして拒否されることで確認する。
	parser, _ := newParser(t)
	if _, err := parser.Parse([]string{"query", "-d", "analytics", "-f", "json", "SELECT 1"}); err == nil {
		t.Fatal("-f を受け付けてしまいました。--format に短縮形を割り当ててはいけません")
	}
}

func TestQueryCmd_Run_DelegatesToApp(t *testing.T) {
	// Run() が詰め替えのみで internal/app に処理を委ねていることを、
	// 設定が無い状態でのエラーが app.Query 由来であることから確認する。
	deps := &app.Deps{
		Out:     &bytes.Buffer{},
		Err:     &bytes.Buffer{},
		Dir:     t.TempDir(),
		Environ: []string{},
	}
	err := Execute(context.Background(), []string{"query", "-d", "analytics", "SELECT 1"}, deps)
	if err == nil {
		t.Fatal("設定が無いのにエラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "redash.endpoint") {
		t.Errorf("app.Resolved.Validate() 由来のエラーではないようです: %v", err)
	}
}
