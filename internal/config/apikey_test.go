package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve_APIKeyの取得元(t *testing.T) {
	tests := []struct {
		name  string
		files layerFiles
		want  string
	}{
		{
			name:  "環境変数",
			files: layerFiles{environ: []string{EnvAPIKey + "=from-env"}},
			want:  "from-env",
		},
		{
			name:  "ローカルファイルの api_key",
			files: layerFiles{local: "version: 1\nredash: {api_key: from-local}\n"},
			want:  "from-local",
		},
		{
			// 環境変数は最も強いレイヤなので、ファイルの指定に勝つ。
			name: "環境変数はファイルに勝つ",
			files: layerFiles{
				local:   "version: 1\nredash: {api_key: from-local}\n",
				environ: []string{EnvAPIKey + "=from-env"},
			},
			want: "from-env",
		},
		{
			name: "${env:VAR} を展開する",
			files: layerFiles{
				local:   "version: 1\nredash: {api_key: \"${env:REDASH_API_KEY}\"}\n",
				environ: []string{"REDASH_API_KEY=expanded"},
			},
			want: "expanded",
		},
		{
			name: "文字列の一部に埋まっていても展開する",
			files: layerFiles{
				local:   "version: 1\nredash: {api_key: \"pre-${env:A}-${env:B}-post\"}\n",
				environ: []string{"A=1", "B=2"},
			},
			want: "pre-1-2-post",
		},
		{
			// 展開するのは ${env:...} だけ。API KEY は任意の文字列なので、
			// たまたま $ を含むものを壊さない。
			name:  "他の $ 記法は展開しない",
			files: layerFiles{local: "version: 1\nredash: {api_key: \"$HOME-${FOO}\"}\n"},
			want:  "$HOME-${FOO}",
		},
		{
			// api_key_command は秘密そのものではないので共有ファイルに書ける。
			name:  "共有ファイルの api_key_command",
			files: layerFiles{shared: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"printf from-command\"]}\n"},
			want:  "from-command",
		},
		{
			// 組で差し替えるので、後のレイヤの指定だけが残る。
			// 共有の command が生き残ったまま local の key と競合してはならない。
			name: "ローカルの api_key が共有の api_key_command を置き換える",
			files: layerFiles{
				shared: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"exit 1\"]}\n",
				local:  "version: 1\nredash: {api_key: from-local}\n",
			},
			want: "from-local",
		},
		{
			name:  "どこにも指定が無ければ空",
			files: layerFiles{shared: "version: 1\n"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.files.shared+tt.files.local, "api_key_command") {
				requireSh(t)
			}
			if got := mustResolve(t, tt.files).APIKey; got != tt.want {
				t.Errorf("APIKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolve_APIKeyのエラー(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		wantErr string
		needsSh bool
	}{
		{
			name:    "参照先の環境変数が無い",
			files:   layerFiles{local: "version: 1\nredash: {api_key: \"${env:MISSING_VAR}\"}\n"},
			wantErr: "環境変数 MISSING_VAR が設定されていません",
		},
		{
			name:    "${env: が閉じていない",
			files:   layerFiles{local: "version: 1\nredash: {api_key: \"${env:BROKEN\"}\n"},
			wantErr: "対応する } がありません",
		},
		{
			name:    "変数名が空",
			files:   layerFiles{local: "version: 1\nredash: {api_key: \"${env:}\"}\n"},
			wantErr: "変数名がありません",
		},
		{
			// どちらを使うつもりだったのか決められないので、黙って選ばない。
			name:    "同じファイルに api_key と api_key_command",
			files:   layerFiles{local: "version: 1\nredash: {api_key: k, api_key_command: [\"true\"]}\n"},
			wantErr: "同時に指定できません",
		},
		{
			name:    "api_key_command が失敗する",
			files:   layerFiles{local: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"echo boom >&2; exit 3\"]}\n"},
			wantErr: "api_key_command が失敗しました",
			needsSh: true,
		},
		{
			name:    "api_key_command が空を返す",
			files:   layerFiles{local: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"printf ''\"]}\n"},
			wantErr: "空の出力を返しました",
			needsSh: true,
		},
		{
			name:    "api_key_command のコマンドが存在しない",
			files:   layerFiles{local: "version: 1\nredash: {api_key_command: [\"sumiq-no-such-command\"]}\n"},
			wantErr: "api_key_command が失敗しました",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needsSh {
				requireSh(t)
			}
			wantError(t, tt.files, tt.wantErr)
		})
	}
}

// 「書かないでください」という規約ではなく、構造で止める。
func TestResolve_git管理下のAPIKeyはエラー(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		tracked bool
		wantErr string
	}{
		{
			name:    "git 管理下のファイルの api_key",
			files:   layerFiles{local: "version: 1\nredash: {api_key: leaked}\n"},
			tracked: true,
			wantErr: "git の管理下にあります",
		},
		{
			// ${env:} なら実害は無いが、それを許すと「中身を読んで安全か判断する」
			// ことがレビュアーの仕事になる。判断を挟まず一律で落とす。
			name:    "git 管理下なら ${env:VAR} でもエラー",
			files:   layerFiles{local: "version: 1\nredash: {api_key: \"${env:X}\"}\n", environ: []string{"X=v"}},
			tracked: true,
			wantErr: "git の管理下にあります",
		},
		{
			// 秘密そのものではなく秘密の取り方なので、コミットされていてよい。
			name:    "api_key_command は git 管理下でも通る",
			files:   layerFiles{shared: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"printf ok\"]}\n"},
			tracked: true,
		},
		{
			name:    "管理下でなければ通る",
			files:   layerFiles{local: "version: 1\nredash: {api_key: fine}\n"},
			tracked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.files.shared, "api_key_command") {
				requireSh(t)
			}
			dir := t.TempDir()
			mkGitRoot(t, dir)
			opts := testOptions(dir)
			opts.Environ = tt.files.environ
			if opts.Environ == nil {
				opts.Environ = []string{}
			}
			opts.Tracked = func(string) (bool, error) { return tt.tracked, nil }
			if tt.files.shared != "" {
				writeFile(t, dir, SharedFileName, tt.files.shared)
			}
			if tt.files.local != "" {
				writeFile(t, dir, LocalFileName, tt.files.local)
			}

			_, err := Resolve(opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Resolve() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Resolve() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Resolve() error = %q, want に %q を含む", err.Error(), tt.wantErr)
			}
		})
	}
}

// 環境変数由来の api_key には git の検査を掛けない。ファイルではないため。
func TestResolve_環境変数のAPIKeyはgit検査の対象外(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	opts := testOptions(dir)
	opts.Environ = []string{EnvAPIKey + "=from-env"}
	opts.Tracked = func(path string) (bool, error) {
		t.Errorf("Tracked(%q) が呼ばれた。環境変数由来の api_key は検査対象外", path)
		return true, nil
	}

	res, err := Resolve(opts)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.APIKey != "from-env" {
		t.Errorf("APIKey = %q", res.APIKey)
	}
}

// 解決前の生の指定を Config から読めないこと。読める場所にあると、
// ${env:} を展開せずにそれを使う実装がいずれ現れる。
func TestResolve_生のAPIKeyはConfigに残らない(t *testing.T) {
	res := mustResolve(t, layerFiles{local: "version: 1\nredash: {api_key: secret}\n"})

	if res.Config.Redash.APIKey != "" {
		t.Errorf("Config.Redash.APIKey = %q, want 空", res.Config.Redash.APIKey)
	}
	if res.Config.Redash.APIKeyCommand != nil {
		t.Errorf("Config.Redash.APIKeyCommand = %v, want nil", res.Config.Redash.APIKeyCommand)
	}
	if res.APIKey != "secret" {
		t.Errorf("APIKey = %q, want %q", res.APIKey, "secret")
	}
}

// gitTracked の実装そのものを本物の git リポジトリで確かめる。
// 他のテストは Options.Tracked を差し替えているため、ここが唯一の検証になる。
func TestGitTracked(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git が無いのでスキップする")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		// コミットは作らないので user.name / user.email は要らない。
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")

	trackedPath := writeFile(t, dir, SharedFileName, "version: 1\n")
	untrackedPath := writeFile(t, dir, LocalFileName, "version: 1\n")
	// add した時点で ls-files --error-unmatch は 0 を返す。コミットは要らない。
	run("add", SharedFileName)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "追跡下", path: trackedPath, want: true},
		{name: "追跡外", path: untrackedPath, want: false},
		{name: "存在しないファイル", path: filepath.Join(dir, "missing.yaml"), want: false},
		{name: "リポジトリの外", path: writeFile(t, t.TempDir(), "outside.yaml", ""), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitTracked(tt.path)
			if err != nil {
				t.Fatalf("gitTracked() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("gitTracked(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh が無いのでスキップする")
	}
}
