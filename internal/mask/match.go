package mask

import (
	"fmt"
	"regexp"
	"strings"
)

// regexPrefix は patterns を正規表現として読む接頭辞（ADR-0003 §3）。
const regexPrefix = "regex:"

// compilePattern はパターン1つを照合器にする。
//
// 大文字小文字はグロブでも正規表現でも無視する。ADR-0003 §3 がグロブについて
// そう定めているのに加え、SQL の識別子は多くの DBMS で大文字小文字を区別せず、
// 同じ列が email でも Email でも返りうる。片方だけマスクが外れるより、
// 両方に掛かる方に倒す。正規表現で区別が要る場合は (?-i) で戻せる。
//
// **グロブは全体一致、regex: は部分一致になる。** 正規表現の既定を曲げると
// 他のツールと挙動が変わるため揃えない。ADR-0003 §7 の例が
// regex:^(first|last)_name$ と明示的に錨を打っているのはこのため。
//
// この非対称は method: none で効き方が変わる。default_action: redact の
// allowlist 運用で regex:user_id を素通しにすると、user_identity や
// user_id_hash まで素通しになる。同じ意図をグロブの user_id で書いた
// 場合は user_id だけが素通しになる。allowlist に穴を開けるときは
// ^ と $ を書くこと。
//
// 組み立てられないパターンはエラーにする。マッチしないパターンとして
// 読み飛ばすと、そのルールが消えたまま実行される。
func compilePattern(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, fmt.Errorf("空のパターンは書けません")
	}

	if expr, ok := strings.CutPrefix(p, regexPrefix); ok {
		if expr == "" {
			return nil, fmt.Errorf("パターン %q: %s の後が空です", p, regexPrefix)
		}
		re, err := regexp.Compile("(?i)" + expr)
		if err != nil {
			return nil, fmt.Errorf("パターン %q: 正規表現として読めません: %w", p, err)
		}
		return re, nil
	}

	expr, err := globToRegexp(p)
	if err != nil {
		return nil, fmt.Errorf("パターン %q: %w", p, err)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("パターン %q: グロブを解釈できません: %w", p, err)
	}
	return re, nil
}

// globToRegexp はグロブを正規表現に直す。
//
// 特別な意味を持つのは * と ? だけで、他の文字は全て文字そのものとして
// 照合する。列名をそのまま書いたパターンが必ずその列にマッチすることを、
// 列名に何が入っていても保つための決まり。
//
// path.Match / filepath.Match は使わない。どちらも / を区切りとして扱うため
// * が / をまたがず、列名に / が入るデータソース（JSON のパスを列名にする
// クエリランナー等）で patterns: ["*"] がマッチしなくなる。加えて
// path.Match は \ を打ち消しとして読むため、列名に \ を含むデータソースでは
// 列名をそのまま書いたパターンが何にもマッチしない。filepath.Match は
// Windows でその \ の扱いが変わり、同じ設定が実行環境で違う結果になる。
// いずれもマスクが黙って外れる側の食い違いなので、自前で組む。
//
// [ だけはエラーにする。文字クラスとしても文字そのものとしても読めて
// しまい、どちらに倒しても読み違えた側でマスクが外れる。曖昧なまま
// 通さず regex: へ誘導する。
func globToRegexp(glob string) (string, error) {
	var b strings.Builder
	// (?i) は先頭にしか書けない。\A と \z で全体一致にする。
	b.WriteString(`(?i)\A`)

	for _, c := range glob {
		switch c {
		case '*':
			// (?s:.) は改行にもマッチする。列名に改行が入ることは
			// まず無いが、入ったときにマスクが外れる側に倒さない。
			b.WriteString(`(?s:.)*`)
		case '?':
			b.WriteString(`(?s:.)`)
		case '[':
			return "", fmt.Errorf("グロブでは [ を扱いません。" +
				"文字クラスを書くにも、[ を含む列名を指すにも、regex: を使ってください")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	b.WriteString(`\z`)
	return b.String(), nil
}
