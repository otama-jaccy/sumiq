package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// apiKeyCommandTimeout は api_key_command の実行を打ち切るまでの時間。
//
// 1Password CLI のように対話的な認証を挟むコマンドが指定されると、
// 上限が無ければ sumiq は無言で止まったままになる。
const apiKeyCommandTimeout = 30 * time.Second

// apiKeyCommandWaitDelay は打ち切り後にパイプを強制的に閉じるまでの猶予。
const apiKeyCommandWaitDelay = 5 * time.Second

// apiKeySource は API KEY の指定1つ分と、その出どころ。
//
// api_key と api_key_command は「どちらか一方」であり、レイヤをまたいで
// 混ざらないよう組で差し替える。共有ファイルに api_key_command、ローカルに
// api_key を書いた場合、後から読むローカルの指定だけが残る。
// git 管理下かの検査はここではなく checkAPIKeyFiles が全ファイルに対して行う。
// 勝ったレイヤの出どころは検査に要らないため、パスは持たない。
type apiKeySource struct {
	key     string
	command []string
	layer   Layer
	set     bool
}

// absorb は l に API KEY の指定があれば、それで丸ごと置き換える。
func (s *apiKeySource) absorb(l layered) error {
	r := l.cfg.Redash
	if r.APIKey == "" && len(r.APIKeyCommand) == 0 {
		return nil
	}
	// 同じファイルに両方書かれていたら、どちらを使うつもりだったのか決められない。
	// 黙って一方を選ぶと、選ばれなかった方を設定したつもりのまま動く。
	if r.APIKey != "" && len(r.APIKeyCommand) > 0 {
		return fmt.Errorf("%s: redash.api_key と redash.api_key_command は同時に指定できません。"+
			"どちらか一方にしてください", l.origin())
	}
	*s = apiKeySource{
		key:     r.APIKey,
		command: r.APIKeyCommand,
		layer:   l.layer,
		set:     true,
	}
	return nil
}

// resolveAPIKey は API KEY を確定させる。
//
// 取得元は ADR-0003 §5 の3経路。優先順位はレイヤの強弱そのものであり、
// 環境変数 SUMIQ_REDASH_API_KEY は環境変数レイヤとして最も強いところに入るため、
// ここでは apiKeySource が指す1つを解決するだけでよい。
func (s apiKeySource) resolve(opts Options) (string, error) {
	if !s.set {
		return "", nil
	}
	if s.key != "" {
		// 環境変数から来た値は展開しない。${env:VAR} は設定ファイルに書くための
		// 記法（ADR-0003 §5）であり、環境変数の値に適用すると、利用者が書いて
		// いない redash.api_key を指すエラーが返ることになる。
		if s.layer == LayerEnv {
			return s.key, nil
		}
		return expandEnvRefs(s.key, opts.lookupEnv)
	}
	return runAPIKeyCommand(s.command, s.layer)
}

// apiKeySpec は API KEY の指定を書いたファイルと、その項目名。
type apiKeySpec struct {
	path  string
	field string
}

// checkAPIKeyFiles は API KEY の指定が git の管理下のファイルに無いことを確かめる。
//
// 検査するのは「勝ったレイヤ」ではなく指定を書いた全ファイル。
// 環境変数で API KEY を渡している人（ADR-0003 §5 の経路1。最も普通の使い方）は
// ファイルの指定が負けるため、勝者だけを見ると、共有ファイルにコミットされた
// api_key を素通しすることになる。この検査は事故を防ぐためのものなので、
// 「その値が実際に使われるか」とは無関係に走らせる。
//
// api_key_command も対象にする。値そのものは秘密でないが、これは
// 「実行されるコマンド」であり、コミットできると、クローンしたリポジトリの
// sumiq.yaml に書かれた任意のコマンドが sumiq の起動だけで走ることになる。
// ADR-0003 が api_key_command を挙げているのも §4 のローカルファイルの
// スキーマだけで、§3 の共有ファイルには含まれていない。
func checkAPIKeyFiles(specs []apiKeySpec, opts Options) error {
	for _, s := range specs {
		tracked, err := opts.tracked(s.path)
		if err != nil {
			return err
		}
		if tracked {
			// api_key が ${env:VAR} なら実害は無いが、それも落とす。許すと
			// 「git に入っている api_key は中身を読んで安全か判断する」ことが
			// レビュアーの仕事になる。判断を挟まず一律で落とす方が、
			// 規約より確実に事故を防ぐ。
			return fmt.Errorf("%s は git の管理下にあります。redash.%s をここに書くことはできません。"+
				"%s に書くか、環境変数 %s を使ってください",
				s.path, s.field, LocalFileName, EnvAPIKey)
		}
	}
	return nil
}

// runAPIKeyCommand は api_key_command を実行し、標準出力を API KEY として読む。
func runAPIKeyCommand(command []string, layer Layer) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// WaitDelay が無いと締切は守られない。CommandContext が kill するのは
	// 直接の子だけで、孫が標準出力のパイプを握ったままなら Output() は
	// EOF を待って止まり続ける（api_key_command: ["sh", "-c", "daemon & op read ..."]）。
	// 上限を設けた意味が消えるため、kill 後にパイプを強制的に閉じる。
	cmd.WaitDelay = apiKeyCommandWaitDelay

	out, err := cmd.Output()
	if err != nil {
		// 打ち切りの判定は失敗したときだけ。先に見ると、締切ぎりぎりで成功した
		// コマンドの出力を捨ててタイムアウトとして報告してしまう。
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s: redash.api_key_command が %s 以内に終わりませんでした: %v",
				layer, apiKeyCommandTimeout, command)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("%s: redash.api_key_command が失敗しました: %v: %w", layer, command, err)
		}
		return "", fmt.Errorf("%s: redash.api_key_command が失敗しました: %v: %w: %s", layer, command, err, msg)
	}

	// コマンド出力は末尾に改行が付くのが普通なので落とす。
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("%s: redash.api_key_command が空の出力を返しました: %v", layer, command)
	}
	return key, nil
}

// envRefPrefix は設定ファイル中で環境変数を参照する記法の開始部分。
const envRefPrefix = "${env:"

// expandEnvRefs は ${env:VAR} を環境変数の値に置き換える。
//
// 展開するのはこの記法だけで、${...} の他の形や $VAR はそのまま残す。
// API KEY は任意の文字列であり、たまたま $ を含みうるため、
// 展開の対象を明示的に書かれたものだけに限る。
//
// 参照先が未設定ならエラーにする。空文字に展開して進むと、認証だけが
// 失敗する分かりにくいエラーになって返ってくる。
func expandEnvRefs(s string, lookup func(string) (string, bool)) (string, error) {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, envRefPrefix)
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])
		body := rest[i+len(envRefPrefix):]
		end := strings.Index(body, "}")
		if end < 0 {
			return "", fmt.Errorf("redash.api_key: %s に対応する } がありません: %q", envRefPrefix, s)
		}
		name := body[:end]
		if name == "" {
			return "", fmt.Errorf("redash.api_key: ${env:} に変数名がありません: %q", s)
		}
		v, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("redash.api_key: 環境変数 %s が設定されていません", name)
		}
		b.WriteString(v)
		rest = body[end+1:]
	}
}

// gitTracked は path が git の管理下にあるかを返す。
//
// 判定できなかった場合はエラーを返す。追跡外と混同してはならない。
// git は「そのファイルは追跡外」以外にも 128 で落ちる。他人所有の
// チェックアウト（detected dubious ownership）、壊れた index、読めない .git。
// これを全部「追跡外」に倒すと、共有チェックアウトを使う環境でだけ、
// コミットされた api_key を素通しすることになる。検査が最も要る場所で
// 検査が消える。
//
// リポジトリの外にあるかどうかは git に尋ねず .git を辿って判定する。
// git のエラーメッセージは翻訳されるため、文言での判別は環境に依存する。
//
// git が入っていない環境では追跡外として扱う。判定できないことを理由に
// 実行を止めると、git を使わない利用者が api_key を書けなくなる。
// この検査が防いでいるのは「うっかりコミットする」事故であり、
// git の無い環境ではそもそもその事故が起きない。
func gitTracked(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("%s: 絶対パスに解決できません: %w", path, err)
	}
	if _, inRepo := gitRoot(filepath.Dir(abs)); !inRepo {
		return false, nil
	}

	// カレントディレクトリではなく対象ファイルの位置を基準に問い合わせる。
	// sumiq はサブディレクトリからも、リポジトリの外からも起動されうる。
	//
	// --literal-pathspecs は必須。git はパスを pathspec として解釈するため、
	// これが無いと proj[1] のような名前のディレクトリでパスが glob として
	// 扱われ、実在するファイルにマッチせず「追跡外」が返る。秘密の混入を
	// 止める検査が、チェックアウト先の名前次第で開いてしまう。
	cmd := exec.Command("git", "-C", filepath.Dir(abs), "--literal-pathspecs",
		"ls-files", "--error-unmatch", "--", abs)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}
	// --error-unmatch は「追跡外」のときだけ 1 を返す。それ以外の
	// 終了コードは git 自体が失敗したという意味であり、追跡外ではない。
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return false, fmt.Errorf("%s が git の管理下にあるか判定できませんでした: %w", path, err)
	}
	return false, fmt.Errorf("%s が git の管理下にあるか判定できませんでした: %w: %s", path, err, msg)
}
