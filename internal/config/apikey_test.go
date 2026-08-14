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
			// git 管理下でなければ読む。管理下かどうかの検査は別のテストで見る。
			name:  "api_key_command",
			files: layerFiles{local: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"printf from-command\"]}\n"},
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
			got, err := mustResolve(t, tt.files).APIKey()
			if err != nil {
				t.Fatalf("APIKey() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("APIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 解決は必要になるまで走らせない。設定を表示するだけのコマンドが
// 1Password のプロンプトで止まるのはおかしい。
func TestResolve_APIKeyは遅延解決される(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	mkGitRoot(t, dir)
	marker := filepath.Join(dir, "ran")
	opts := testOptions(dir)
	writeFile(t, dir, LocalFileName,
		"version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"touch "+marker+"; printf k\"]}\n")

	res, err := Resolve(opts)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Resolve() の時点で api_key_command が実行されている")
	}

	got, err := res.APIKey()
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if got != "k" {
		t.Errorf("APIKey() = %q, want %q", got, "k")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("APIKey() を呼んでも api_key_command が実行されていない")
	}

	// 2回目は実行し直さない。
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := res.APIKey(); err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("APIKey() のたびに api_key_command が実行されている")
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
			// 解決は遅延するので、エラーは Resolve ではなく APIKey で出る。
			res, err := resolveWith(t, tt.files)
			if err == nil {
				_, err = res.APIKey()
			}
			if err == nil {
				t.Fatal("APIKey() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("APIKey() error = %q, want に %q を含む", err.Error(), tt.wantErr)
			}
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
			// api_key_command は「実行されるコマンド」。コミットできると、
			// クローンしたリポジトリの sumiq.yaml に書かれた任意のコマンドが
			// sumiq の起動だけで走る。ADR-0003 が挙げているのも §4 の
			// ローカルファイルのスキーマだけで、§3 の共有ファイルには無い。
			name:    "git 管理下の api_key_command もエラー",
			files:   layerFiles{shared: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"printf ok\"]}\n"},
			tracked: true,
			wantErr: "api_key_command をここに書くことはできません",
		},
		{
			name:    "api_key_command も管理下でなければ通る",
			files:   layerFiles{local: "version: 1\nredash: {api_key_command: [\"sh\", \"-c\", \"printf ok\"]}\n"},
			tracked: false,
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

// 検査対象は「勝ったレイヤ」ではなく api_key を書いた全ファイル。
//
// 環境変数で API KEY を渡すのは ADR-0003 §5 の経路1、つまり最も普通の使い方で、
// そのときファイルの指定は必ず負ける。勝者だけを見ていると、共有ファイルに
// コミットされた api_key を素通しすることになる。構造で防ぐはずの事故が、
// 一番ありふれた構成でだけ防げていない状態になる。
func TestResolve_負けたレイヤのAPIKeyもgit検査する(t *testing.T) {
	tests := []struct {
		name  string
		files layerFiles
	}{
		{
			name: "環境変数が勝っても共有ファイルの api_key を検出する",
			files: layerFiles{
				shared:  "version: 1\nredash: {api_key: LEAKED}\n",
				environ: []string{EnvAPIKey + "=from-env"},
			},
		},
		{
			name: "ローカルが勝っても共有ファイルの api_key を検出する",
			files: layerFiles{
				shared: "version: 1\nredash: {api_key: LEAKED}\n",
				local:  "version: 1\nredash: {api_key: from-local}\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			mkGitRoot(t, dir)
			opts := testOptions(dir)
			opts.Environ = tt.files.environ
			if opts.Environ == nil {
				opts.Environ = []string{}
			}
			sharedPath := writeFile(t, dir, SharedFileName, tt.files.shared)
			if tt.files.local != "" {
				writeFile(t, dir, LocalFileName, tt.files.local)
			}
			// 共有ファイルだけが git 管理下。ローカルは gitignore される前提。
			opts.Tracked = func(path string) (bool, error) { return path == sharedPath, nil }

			_, err := Resolve(opts)
			if err == nil {
				t.Fatal("Resolve() error = nil, want error。コミットされた api_key が素通りしている")
			}
			if !strings.Contains(err.Error(), "git の管理下にあります") {
				t.Errorf("Resolve() error = %q", err.Error())
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
	got, err := res.APIKey()
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if got != "from-env" {
		t.Errorf("APIKey() = %q", got)
	}
}

// ${env:VAR} は設定ファイルに書くための記法。環境変数の値に適用すると、
// 利用者が書いていない redash.api_key を指すエラーが返ることになる。
func TestResolve_環境変数のAPIKeyは展開しない(t *testing.T) {
	res := mustResolve(t, layerFiles{
		environ: []string{EnvAPIKey + `=abc${env:NOPE}`},
	})
	got, err := res.APIKey()
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if want := `abc${env:NOPE}`; got != want {
		t.Errorf("APIKey() = %q, want %q", got, want)
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
	got, err := res.APIKey()
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if got != "secret" {
		t.Errorf("APIKey() = %q, want %q", got, "secret")
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

// git が失敗したら「追跡外」ではなくエラーを返すこと。
//
// git は追跡外（1）以外にも 128 で落ちる。他人所有のチェックアウト
// （detected dubious ownership）、壊れた index、読めない .git。
// これを追跡外に倒すと、共有チェックアウトを使う環境でだけ、
// コミットされた api_key を素通しすることになる。
func TestGitTracked_判定できなければエラー(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git が無いのでスキップする")
	}

	dir := t.TempDir()
	cmd := exec.Command(git, "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	path := writeFile(t, dir, SharedFileName, "version: 1\n")

	// .git は在るが中身が壊れている状態を作る。gitRoot はリポジトリ内と
	// 判定し、git 自身は 128 で落ちる。
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tracked, err := gitTracked(path)
	if err == nil {
		t.Fatalf("gitTracked() error = nil (tracked=%v), want error。"+
			"判定できないことを追跡外と混同している", tracked)
	}
	if tracked {
		t.Error("gitTracked() = true, want false")
	}
}

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh が無いのでスキップする")
	}
}
