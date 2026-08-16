package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/app/template"
	"github.com/otama-jaccy/sumiq/internal/config"
)

func TestInit_CreatesBothFiles(t *testing.T) {
	dir := t.TempDir()
	deps, _, errW := newTestDeps(dir)

	if err := Init(deps, InitParams{}); err != nil {
		t.Fatalf("Init() 失敗: %v", err)
	}

	sharedPath := filepath.Join(dir, config.SharedFileName)
	localPath := filepath.Join(dir, config.LocalFileName)
	if _, err := os.Stat(sharedPath); err != nil {
		t.Errorf("%s が作成されていません: %v", sharedPath, err)
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Errorf("%s が作成されていません: %v", localPath, err)
	}
	if got := errW.String(); !strings.Contains(got, "API KEY") {
		t.Errorf("次にやることの案内が stderr にありません: %s", got)
	}
}

func TestInit_GeneratedSharedConfigLoads(t *testing.T) {
	dir := t.TempDir()
	deps, _, _ := newTestDeps(dir)

	if err := Init(deps, InitParams{}); err != nil {
		t.Fatalf("Init() 失敗: %v", err)
	}

	if _, err := config.LoadFile(filepath.Join(dir, config.SharedFileName)); err != nil {
		t.Errorf("生成された %s が config.LoadFile でパースできません: %v", config.SharedFileName, err)
	}
}

func TestInit_WithoutForceRefusesWhenEitherFileExists(t *testing.T) {
	dir := t.TempDir()
	deps, _, _ := newTestDeps(dir)

	sharedPath := filepath.Join(dir, config.SharedFileName)
	localPath := filepath.Join(dir, config.LocalFileName)
	const existingShared = "existing shared content\n"
	if err := os.WriteFile(sharedPath, []byte(existingShared), 0o644); err != nil {
		t.Fatalf("前提となる %s を書き込めません: %v", sharedPath, err)
	}
	// sumiq.local.yaml は存在しない状態でも、片方が存在するだけで拒否されることを確認する。

	err := Init(deps, InitParams{})
	if err == nil {
		t.Fatal("既存ファイルがあるのに --force 無しでエラーになりませんでした")
	}
	if !strings.Contains(err.Error(), config.SharedFileName) {
		t.Errorf("エラーメッセージに競合したファイル名が含まれていません: %v", err)
	}

	got, readErr := os.ReadFile(sharedPath)
	if readErr != nil {
		t.Fatalf("%s を読めません: %v", sharedPath, readErr)
	}
	if string(got) != existingShared {
		t.Errorf("%s の内容が変更されています: got %q, want %q", sharedPath, got, existingShared)
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Errorf("%s が書き込まれていません（片方が存在するだけで両方とも書き込まれないはず）", localPath)
	}
}

func TestInit_ForceOverwritesBoth(t *testing.T) {
	dir := t.TempDir()
	deps, _, _ := newTestDeps(dir)

	sharedPath := filepath.Join(dir, config.SharedFileName)
	localPath := filepath.Join(dir, config.LocalFileName)
	if err := os.WriteFile(sharedPath, []byte("stale shared\n"), 0o644); err != nil {
		t.Fatalf("前提となる %s を書き込めません: %v", sharedPath, err)
	}
	if err := os.WriteFile(localPath, []byte("stale local\n"), 0o644); err != nil {
		t.Fatalf("前提となる %s を書き込めません: %v", localPath, err)
	}

	if err := Init(deps, InitParams{Force: true}); err != nil {
		t.Fatalf("--force 付きの Init() が失敗しました: %v", err)
	}

	got, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatalf("%s を読めません: %v", sharedPath, err)
	}
	if string(got) != string(template.Shared) {
		t.Errorf("--force で %s が上書きされていません", sharedPath)
	}
}

func TestInit_WarnsWhenGitignoreMissingEntry(t *testing.T) {
	dir := t.TempDir()
	deps, _, errW := newTestDeps(dir)

	if err := Init(deps, InitParams{}); err != nil {
		t.Fatalf("Init() 失敗: %v", err)
	}
	if !strings.Contains(errW.String(), ".gitignore") {
		t.Errorf(".gitignore が無いのに警告が出ていません: %s", errW.String())
	}
}

func TestInit_NoWarningWhenGitignoreHasEntry(t *testing.T) {
	dir := t.TempDir()
	deps, _, errW := newTestDeps(dir)

	// 前後の空白は許容されるべきなのでインデントを付けて確認する。
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n  "+config.LocalFileName+"  \n"), 0o644); err != nil {
		t.Fatalf(".gitignore を書き込めません: %v", err)
	}

	if err := Init(deps, InitParams{}); err != nil {
		t.Fatalf("Init() 失敗: %v", err)
	}
	if strings.Contains(errW.String(), ".gitignore") {
		t.Errorf(".gitignore にエントリがあるのに警告が出ました: %s", errW.String())
	}
}

// TestEmbeddedTemplatesMatchRootExamples は internal/app/template の埋め込み
// テンプレートと、リポジトリルート直下の .example ファイルのバイト列が
// 一致することを確認する。go:embed は親ディレクトリを参照できず複製せざるを
// 得ないため、この一致だけが2つがズレていないことの担保になる。
func TestEmbeddedTemplatesMatchRootExamples(t *testing.T) {
	cases := []struct {
		embedded []byte
		example  string
	}{
		{template.Shared, "../../sumiq.yaml.example"},
		{template.Local, "../../sumiq.local.yaml.example"},
	}
	for _, c := range cases {
		want, err := os.ReadFile(c.example)
		if err != nil {
			t.Fatalf("%s を読めません: %v", c.example, err)
		}
		if string(c.embedded) != string(want) {
			t.Errorf("%s と埋め込みテンプレートのバイト列が一致しません", c.example)
		}
	}
}
