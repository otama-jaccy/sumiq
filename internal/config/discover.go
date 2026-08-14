package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// SharedFileName は <repo>/sumiq.yaml のファイル名。コミットされる共有設定。
const SharedFileName = "sumiq.yaml"

// LocalFileName は <repo>/sumiq.local.yaml のファイル名。gitignore されるローカル設定。
const LocalFileName = "sumiq.local.yaml"

// discovered は探索で決まったファイル1枚。
type discovered struct {
	layer Layer
	path  string
}

// discover は読むべき設定ファイルを、弱いレイヤから順に返す。
//
// 存在しないファイルは結果に含めない。「ファイルが無い」ことはエラーではない。
// ただし --config で明示指定されたファイルだけは、利用者が名前を挙げている以上
// 存在確認をせずに返し、Resolve の LoadFile で落とす。
func discover(opts Options) ([]discovered, error) {
	if opts.ConfigPath != "" {
		// --config 指定時は 2〜4 の探索をスキップする。
		return []discovered{{layer: LayerExplicit, path: opts.ConfigPath}}, nil
	}

	var found []discovered

	userPath, err := opts.userConfigPath()
	if err != nil {
		return nil, err
	}
	if userPath != "" && fileExists(userPath) {
		found = append(found, discovered{layer: LayerUser, path: userPath})
	}

	dir := opts.Dir
	if dir == "" {
		dir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("カレントディレクトリを取得できません: %w", err)
		}
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: 絶対パスに解決できません: %w", dir, err)
	}

	// 遡る上限は先に確定させる。探索しながら git root を判定すると、
	// git root が無い場合にファイルシステムの root まで遡ってしまう。
	root, inRepo := gitRoot(dir)

	// 共有とローカルは別々に遡る。片方だけがリポジトリルートに置かれ、
	// もう片方がサブディレクトリにある構成を許すため。
	if p := searchUp(dir, root, inRepo, SharedFileName); p != "" {
		found = append(found, discovered{layer: LayerShared, path: p})
	}
	if p := searchUp(dir, root, inRepo, LocalFileName); p != "" {
		found = append(found, discovered{layer: LayerLocal, path: p})
	}
	return found, nil
}

// gitRoot は start から遡って最初に見つかる git ワークツリーのルートを返す。
func gitRoot(start string) (string, bool) {
	dir := start
	for {
		if isGitRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// searchUp は start から root まで遡って name を探し、最初に見つかったものを返す。
//
// 近い方が勝つ。同じ名前のファイルが途中の複数階層にあっても、マージはせず
// 最も近い1枚だけを読む。全部を読むと「どこに何を書いたか」が追えなくなるため。
//
// inRepo が false のとき（git リポジトリの外）は start だけを見る。上限の無いまま
// 遡ると、ホームディレクトリに置き忘れた sumiq.local.yaml のような無関係な
// ファイルを黙って読んでしまう。それは api_key_command の実行まで含む。
func searchUp(start, root string, inRepo bool, name string) string {
	dir := start
	for {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			return p
		}
		if !inRepo || dir == root {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// isGitRoot は dir が git のワークツリーのルートかを返す。
//
// .git はディレクトリとは限らない。worktree や submodule では gitdir を指す
// ファイルになるため、種別を問わず存在だけを見る。
func isGitRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// fileExists は path が読める通常ファイル（またはそれへのシンボリックリンク）かを返す。
//
// ディレクトリを設定ファイルと誤認しないよう、種別まで確認する。
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
