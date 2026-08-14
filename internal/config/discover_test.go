package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile は dir/name に内容を書き、そのパスを返す。途中のディレクトリも作る。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mkGitRoot は dir を git のワークツリールートに見せかける。
// 探索の上限を検証するためだけのもので、git コマンドは呼ばない。
func mkGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// testOptions はプロセスの環境に触らない Options を返す。
//
// Environ を空スライス（nil ではない）にしているのが要点。nil だと
// os.Environ() に落ちるため、実行環境に SUMIQ_* があるとテストが揺れる。
func testOptions(dir string) Options {
	return Options{
		Dir:            dir,
		Environ:        []string{},
		UserConfigPath: filepath.Join(dir, "does-not-exist", "config.yaml"),
		Tracked:        func(string) (bool, error) { return false, nil },
	}
}

func TestDiscover_探索順(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)

	userPath := writeFile(t, dir, "user.yaml", "version: 1\n")
	sharedPath := writeFile(t, dir, SharedFileName, "version: 1\n")
	localPath := writeFile(t, dir, LocalFileName, "version: 1\n")

	opts := testOptions(dir)
	opts.UserConfigPath = userPath

	got, err := discover(opts)
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}

	want := []discovered{
		{layer: LayerUser, path: userPath},
		{layer: LayerShared, path: sharedPath},
		{layer: LayerLocal, path: localPath},
	}
	if len(got) != len(want) {
		t.Fatalf("discover() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("discover()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// --config を指定したら 2〜4 は一切見ない。
// ここが漏れると「--config で渡したはずのファイル以外が効いている」状態になる。
func TestDiscover_ConfigPathで探索をスキップする(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	writeFile(t, dir, SharedFileName, "version: 1\n")
	writeFile(t, dir, LocalFileName, "version: 1\n")
	userPath := writeFile(t, dir, "user.yaml", "version: 1\n")
	explicit := writeFile(t, dir, "explicit.yaml", "version: 1\n")

	opts := testOptions(dir)
	opts.UserConfigPath = userPath
	opts.ConfigPath = explicit

	got, err := discover(opts)
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	want := []discovered{{layer: LayerExplicit, path: explicit}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("discover() = %+v, want %+v", got, want)
	}
}

func TestDiscover_ファイルが無くてもエラーにしない(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)

	got, err := discover(testOptions(dir))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discover() = %+v, want 空", got)
	}
}

func TestDiscover_git_root_まで遡る(t *testing.T) {
	root := t.TempDir()
	mkGitRoot(t, root)
	sharedPath := writeFile(t, root, SharedFileName, "version: 1\n")
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := discover(testOptions(sub))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 1 || got[0].path != sharedPath {
		t.Errorf("discover() = %+v, want %s を見つける", got, sharedPath)
	}
}

// git root より上は見ない。リポジトリの外の設定が黙って効くと、
// 「どのファイルが読まれたのか」を追えなくなる。
func TestDiscover_git_root_より上は見ない(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, outer, SharedFileName, "version: 1\n")
	root := filepath.Join(outer, "repo")
	mkGitRoot(t, root)

	got, err := discover(testOptions(root))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discover() = %+v, want 空（git root の外は見ない）", got)
	}
}

// 途中の複数階層に同じ名前があったら、近い方だけを読む。
func TestDiscover_近い方が勝つ(t *testing.T) {
	root := t.TempDir()
	mkGitRoot(t, root)
	writeFile(t, root, SharedFileName, "version: 1\n")
	sub := filepath.Join(root, "sub")
	nearPath := writeFile(t, sub, SharedFileName, "version: 1\n")

	got, err := discover(testOptions(sub))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 1 || got[0].path != nearPath {
		t.Errorf("discover() = %+v, want %s", got, nearPath)
	}
}

// 共有とローカルは別々に遡るので、置かれている階層が違ってもよい。
func TestDiscover_共有とローカルは別階層でもよい(t *testing.T) {
	root := t.TempDir()
	mkGitRoot(t, root)
	sharedPath := writeFile(t, root, SharedFileName, "version: 1\n")
	sub := filepath.Join(root, "sub")
	localPath := writeFile(t, sub, LocalFileName, "version: 1\n")

	got, err := discover(testOptions(sub))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 2 || got[0].path != sharedPath || got[1].path != localPath {
		t.Errorf("discover() = %+v, want [%s %s]", got, sharedPath, localPath)
	}
}

// git リポジトリの外では遡らない。上限が無いまま遡ると、ホームディレクトリに
// 置き忘れた sumiq.local.yaml のような無関係なファイルを黙って読んでしまう。
// それは api_key_command の実行まで含む。
func TestDiscover_gitリポジトリの外では遡らない(t *testing.T) {
	outer := t.TempDir() // .git を作らない
	writeFile(t, outer, SharedFileName, "version: 1\n")
	writeFile(t, outer, LocalFileName, "version: 1\n")
	deep := filepath.Join(outer, "a", "b")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := discover(testOptions(deep))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discover() = %+v, want 空（リポジトリ外では遡らない）", got)
	}
}

// リポジトリの外でも、カレントディレクトリのファイルは読む。
func TestDiscover_gitリポジトリの外でもカレントは見る(t *testing.T) {
	dir := t.TempDir() // .git を作らない
	sharedPath := writeFile(t, dir, SharedFileName, "version: 1\n")

	got, err := discover(testOptions(dir))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 1 || got[0].path != sharedPath {
		t.Errorf("discover() = %+v, want %s", got, sharedPath)
	}
}

// ディレクトリを設定ファイルと取り違えない。
func TestDiscover_同名のディレクトリは無視する(t *testing.T) {
	root := t.TempDir()
	mkGitRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, SharedFileName), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := discover(testOptions(root))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discover() = %+v, want 空", got)
	}
}

// 探索で見つからないのは正常だが、名前を挙げて渡したファイルが無いのは異常。
func TestResolve_ConfigPathのファイルが無ければエラー(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	opts := testOptions(dir)
	opts.ConfigPath = filepath.Join(dir, "missing.yaml")

	if _, err := Resolve(opts); err == nil {
		t.Fatal("Resolve() error = nil, want error")
	}
}

func TestUserConfigPath(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{
			name:    "XDG_CONFIG_HOME があればそちらを使う",
			environ: []string{"XDG_CONFIG_HOME=/xdg", "HOME=/home/u"},
			want:    filepath.Join("/xdg", "sumiq", "config.yaml"),
		},
		{
			// macOS の os.UserConfigDir は ~/Library/Application Support を返すが、
			// ADR-0003 が定めているのは ~/.config なのでそちらに寄せる。
			name:    "無ければ HOME/.config を使う",
			environ: []string{"HOME=/home/u"},
			want:    filepath.Join("/home/u", ".config", "sumiq", "config.yaml"),
		},
		{
			name:    "XDG_CONFIG_HOME が空なら HOME を使う",
			environ: []string{"XDG_CONFIG_HOME=", "HOME=/home/u"},
			want:    filepath.Join("/home/u", ".config", "sumiq", "config.yaml"),
		},
		{
			name:    "HOME が無ければユーザ設定を読まない",
			environ: []string{},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Options{Environ: tt.environ}.userConfigPath()
			if err != nil {
				t.Fatalf("userConfigPath() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("userConfigPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 読んだファイルを後から辿れること。どの設定が効いているかを説明できないと、
// マスクが期待どおりか利用者が確認できない。
func TestResolve_読んだファイルを返す(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	sharedPath := writeFile(t, dir, SharedFileName, "version: 1\nredash: {endpoint: https://a.example.com}\n")
	localPath := writeFile(t, dir, LocalFileName, "version: 1\n")

	res, err := Resolve(testOptions(dir))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	files := res.Files()
	if len(files) != 2 {
		t.Fatalf("Files() = %+v, want 2件", files)
	}
	if files[0].Path != sharedPath || files[0].Layer != LayerShared {
		t.Errorf("Files()[0] = %+v", files[0])
	}
	if files[1].Path != localPath || files[1].Layer != LayerLocal {
		t.Errorf("Files()[1] = %+v", files[1])
	}
}

// 壊れたファイルはどのファイルが悪いか分かる形で落ちること。
func TestResolve_壊れたファイルはパス付きで落ちる(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	writeFile(t, dir, LocalFileName, "version: 1\nmasking:\n  rules:\n    - patern: [a]\n      method: redact\n")

	_, err := Resolve(testOptions(dir))
	if err == nil {
		t.Fatal("Resolve() error = nil, want error")
	}
	if !strings.Contains(err.Error(), LocalFileName) || !strings.Contains(err.Error(), "patern") {
		t.Errorf("Resolve() error = %q, want にファイル名と patern を含む", err.Error())
	}
}
