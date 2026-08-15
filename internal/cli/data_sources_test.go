package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/app"
)

func TestDataSourcesListCmd_FormatDefaultAndEnum(t *testing.T) {
	parser, c := newParser(t)
	if _, err := parser.Parse([]string{"data-sources", "list"}); err != nil {
		t.Fatalf("Parse() 失敗: %v", err)
	}
	if c.DataSources.List.Format != "table" {
		t.Errorf("Format の既定値が table ではありません: %q", c.DataSources.List.Format)
	}
}

func TestDataSourcesListCmd_FormatRejectsUnknownValue(t *testing.T) {
	parser, _ := newParser(t)
	if _, err := parser.Parse([]string{"data-sources", "list", "--format", "xml"}); err == nil {
		t.Fatal("format: xml を通してしまいました。enum で弾かれるべきです")
	}
}

func TestDataSourcesListCmd_NoPositionalOrDataSourceFlag(t *testing.T) {
	// 対象指定（位置引数・-d/--data-source）は無く、常に全件を取得する。
	parser, _ := newParser(t)
	if _, err := parser.Parse([]string{"data-sources", "list", "-d", "analytics"}); err == nil {
		t.Fatal("-d を受け付けてしまいました。data-sources list に対象指定はありません")
	}
}

func TestDataSourcesListCmd_Run_DelegatesToApp(t *testing.T) {
	deps := &app.Deps{
		Out:     &bytes.Buffer{},
		Err:     &bytes.Buffer{},
		Dir:     t.TempDir(),
		Environ: []string{},
	}
	err := Execute(context.Background(), []string{"data-sources", "list"}, deps)
	if err == nil {
		t.Fatal("設定が無いのにエラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "redash.endpoint") {
		t.Errorf("app.Resolved.Validate() 由来のエラーではないようです: %v", err)
	}
}
