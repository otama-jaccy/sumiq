package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/otama-jaccy/sumiq/internal/app"
	"github.com/otama-jaccy/sumiq/internal/config"
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

func TestInitCmd_ForceDefaultsFalse(t *testing.T) {
	parser, c := newParser(t)
	if _, err := parser.Parse([]string{"init"}); err != nil {
		t.Fatalf("Parse() 失敗: %v", err)
	}
	if c.Init.Force {
		t.Error("Force の既定値が false ではありません")
	}
}

func TestInitCmd_ForceFlagParsesAsBool(t *testing.T) {
	parser, c := newParser(t)
	if _, err := parser.Parse([]string{"init", "--force"}); err != nil {
		t.Fatalf("Parse() 失敗: %v", err)
	}
	if !c.Init.Force {
		t.Error("--force が Force に詰め替わっていません")
	}
}

func TestInitCmd_Run_DelegatesToApp(t *testing.T) {
	// Run() が詰め替えのみで internal/app に処理を委ねていることを、
	// deps.Dir 配下に app.Init が作るはずのファイルが実際に作られることから確認する。
	dir := t.TempDir()
	deps := &app.Deps{
		Out:     &bytes.Buffer{},
		Err:     &bytes.Buffer{},
		Dir:     dir,
		Environ: []string{},
	}
	if err := Execute(context.Background(), []string{"init"}, deps); err != nil {
		t.Fatalf("Execute() 失敗: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.SharedFileName)); err != nil {
		t.Errorf("%s が作成されていません（app.Init に委譲されていない可能性）: %v", config.SharedFileName, err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.LocalFileName)); err != nil {
		t.Errorf("%s が作成されていません（app.Init に委譲されていない可能性）: %v", config.LocalFileName, err)
	}
}
