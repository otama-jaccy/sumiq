// Package mask は結果セットの列を設定に従ってマスクする。
//
// 方針の根拠は docs/adr/0003-config-file-design.md §7・§9（マージ規則とマスク方法）、
// docs/adr/0004-output-formats.md §3（形式ごとの表現）、
// docs/adr/0006-mask-method-null-quoting.md（method: "null" の書き方）にある。
//
// # 列に適用する方法の決め方
//
// 列名にマッチしたルールが1つも無ければ default_action が決める。
// 1つでもマッチすれば、その中で最も強い方法が勝つ。
//
//	drop > redact > null > hash > partial > none
//
// ADR-0003 §7 の並びに null が無かったため、redact と hash の間に置いた。
// 判断は docs/adr/0009-mask-null-strength.md にある。
//
// マッチしたルールは default_action より優先される。default_action: redact
// （allowlist 運用）で特定の列を通す method: none は、これが成り立たないと
// 機能しない（ADR-0003 §7）。強い方に倒すのは「複数のルールがマッチしたとき」
// だけであり、既定との比較には持ち込まない。
//
// # 値を見ない
//
// マスクは列に対して決まり、値そのものは見ない。NULL の値も他の値と同じく
// 置換する。redact なら NULL も **** になる。値を見て分岐すると
// 「この行だけ空だった」という事実が出力に残り、それ自体が漏れになる。
package mask

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/redash"
)

// saltLen は hash に使う salt の長さ。
//
// salt は Engine を作るたびに生成する。1回の実行内では一貫するため件数集計や
// 突き合わせはできるが、実行をまたぐと値が変わる（ADR-0003 §9）。
const saltLen = 32

// Engine は1回の実行に固定したマスク方針。
//
// salt を持つため、実行ごとに1つ作って使い回す。作り直すと同じ値が別の
// ハッシュになり、同一実行内での突き合わせができなくなる。
type Engine struct {
	rules []compiledRule
	// fallback はどのルールにもマッチしなかった列に適用する方法。
	fallback config.MaskMethod
	salt     []byte
}

// compiledRule は照合器まで組み立てたルール1件。
//
// Engine はデータソースを固定して作るため、ここに残るのは対象の
// データソースに効くルールだけ。data_sources の絞り込みは New で済ませる。
type compiledRule struct {
	matchers []*regexp.Regexp
	mask     columnMask
}

// columnMask は列1つに確定したマスク。
type columnMask struct {
	method config.MaskMethod
	// partial は method が partial のときに残す範囲。
	partial partialSpec
}

// New は解決済みの設定から、データソース1つ分のマスクエンジンを作る。
//
// dataSource はこの実行が対象とするデータソースの名前。data_sources で
// スコープ指定されたルールの絞り込みと、データソース単位の default_action の
// 適用に使う。
//
// 設定全体を受け取るのは、ルールが指しているデータソース名が実在するかを
// 見るため。対象のデータソースだけを渡すと、他のデータソースを指した名前の
// 綴り間違いを誰も見なくなる。
//
// 設定の誤りはここで全て弾く。ルールを1つでも黙って落とすと、マスクされる
// はずの列が素通りする。
func New(cfg config.Config, dataSource string) (*Engine, error) {
	// 定義されていない名前で引けると、data_sources でスコープ指定された
	// ルールが1つもマッチしないまま実行される。
	ds, ok := findDataSource(cfg.DataSources, dataSource)
	if !ok {
		return nil, fmt.Errorf("データソース %q は設定に定義されていません", dataSource)
	}

	m := cfg.Masking
	fallback, err := fallbackMethod(m.DefaultAction, ds.DefaultAction)
	if err != nil {
		return nil, err
	}

	e := &Engine{fallback: fallback, salt: make([]byte, saltLen)}
	if _, err := rand.Read(e.salt); err != nil {
		// salt を作れないまま固定値に落とすと、ハッシュが実行をまたいで
		// 復元可能になる。マスクを弱めるくらいなら実行しない。
		return nil, fmt.Errorf("hash 用の salt を生成できませんでした: %w", err)
	}

	for i, r := range m.Rules {
		// 検査は「そのルールが今回使われるか」と切り離して走らせる。
		// 先にスコープで絞ると、別のデータソース向けのルールの書き間違いが、
		// そのデータソースを引く日まで見つからない。
		cr, err := compileRule(r, cfg.DataSources)
		if err != nil {
			return nil, fmt.Errorf("masking.rules[%d] %v: %w", i, r.Patterns, err)
		}
		if !scopedTo(r.DataSources, dataSource) {
			continue // 他のデータソース向けのルール。
		}
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

// fallbackMethod は default_action から、マッチしなかった列に適用する方法を決める。
//
// データソース単位の指定はグローバルより優先されるが、厳格化方向にしか効かない
// （ADR-0003 §7）。緩い指定はエラーにせず、単に効かない。ここで落とすと、
// レビュー済みの共有ファイルにしか書けない組み合わせ（config 側は共有ファイルの
// 引き下げを通す）で実行そのものができなくなる。
//
// 両方を検証してから比べる。厳しい方を先に選ぶと、緩い方が読めない値でも
// 通ってしまい、「解決されていない設定」の検出が入力の半分でしか効かなくなる。
func fallbackMethod(global, perDataSource config.Action) (config.MaskMethod, error) {
	method, err := actionMethod(global)
	if err != nil {
		return "", fmt.Errorf("masking.default_action: %w", err)
	}
	if perDataSource == "" {
		return method, nil
	}
	perMethod, err := actionMethod(perDataSource)
	if err != nil {
		return "", fmt.Errorf("data_sources の default_action: %w", err)
	}
	if strictness(perDataSource) > strictness(global) {
		return perMethod, nil
	}
	return method, nil
}

// actionMethod は default_action を、マッチしなかった列に適用する方法に直す。
//
// 空になるのは default_action が解決されていないとき。既定を補って続けると
// allowlist 運用が黙って denylist に落ちる。
func actionMethod(a config.Action) (config.MaskMethod, error) {
	switch a {
	case config.ActionNone:
		return config.MaskNone, nil
	case config.ActionRedact:
		return config.MaskRedact, nil
	}
	return "", fmt.Errorf("解決されていないか、扱えない値です: %q", a)
}

// strictness は Action の厳しさを返す。大きいほど厳しい。未指定は最も緩い。
func strictness(a config.Action) int {
	if a == config.ActionRedact {
		return 1
	}
	return 0
}

// compileRule はルール1件を照合器まで組み立てる。
// known は設定に定義されている全データソース。
func compileRule(r config.MaskRule, known []config.DataSource) (compiledRule, error) {
	if len(r.Patterns) == 0 {
		return compiledRule{}, errors.New("patterns が空です")
	}
	spec, err := partialSpecOf(r)
	if err != nil {
		return compiledRule{}, err
	}
	if !knownMethod(r.Method) {
		return compiledRule{}, fmt.Errorf("method: %q は扱えません", r.Method)
	}
	for i, name := range r.DataSources {
		// 定義されていない名前（空文字列と綴り間違いを含む）はどの
		// データソースにも一致せず、ルールを丸ごと無効にする。
		// エラーも出ないため、マスクが外れたことに気付く手掛かりが無い。
		if _, ok := findDataSource(known, name); !ok {
			return compiledRule{}, fmt.Errorf("data_sources[%d]: %q は設定に定義されていません。"+
				"定義されていない名前を書いてもルールは効きません", i, name)
		}
	}

	cr := compiledRule{mask: columnMask{method: r.Method, partial: spec}}
	for _, p := range r.Patterns {
		// method: none は allowlist に穴を開ける唯一の手段であり、
		// 意図した列だけに掛かることが確かめられなければならない。
		// regex: は部分一致なので、regex:user_id と書くと user_identity や
		// hashed_user_id まで素通しになる。グロブは全体一致なので、
		// 書いた名前がそのまま穴の大きさになる。
		if r.Method == config.MaskNone && strings.HasPrefix(p, regexPrefix) {
			return compiledRule{}, fmt.Errorf("パターン %q: method: none に %s は書けません。"+
				"%s は部分一致のため、意図した列より広く素通しになります。"+
				"通す列をグロブで1つずつ挙げてください", p, regexPrefix, regexPrefix)
		}
		m, err := compilePattern(p)
		if err != nil {
			return compiledRule{}, err
		}
		cr.matchers = append(cr.matchers, m)
	}
	return cr, nil
}

// findDataSource は名前でデータソースを引く。
func findDataSource(all []config.DataSource, name string) (config.DataSource, bool) {
	for _, ds := range all {
		if ds.Name == name {
			return ds, true
		}
	}
	return config.DataSource{}, false
}

// scopedTo は data_sources の指定がこのデータソースに掛かるかを返す。
// 無指定は全データソースに適用する（ADR-0003 §3）。
func scopedTo(scope []string, dataSource string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, name := range scope {
		if name == dataSource {
			return true
		}
	}
	return false
}

// matches は列名にマッチするパターンがあるかを返す。
func (r compiledRule) matches(column string) bool {
	for _, m := range r.matchers {
		if m.MatchString(column) {
			return true
		}
	}
	return false
}

// Summary は列ごとに適用したマスクの一覧。stderr への通知（#5）に使う。
type Summary struct {
	// Columns は入力の列すべてを入力順に持つ。マスクしなかった列も、
	// drop で出力から消えた列も含む。
	//
	// マスクした列だけに絞らないのは、出力に残った列と突き合わせられる
	// ようにするため。drop された列は出力を見ても存在自体が分からない。
	Columns []ColumnMask
}

// ColumnMask は列1つに適用したマスク方法。
type ColumnMask struct {
	Name   string
	Method config.MaskMethod
}

// Masked は実際にマスクした列（method: none 以外）を入力順に返す。
func (s Summary) Masked() []ColumnMask {
	out := make([]ColumnMask, 0, len(s.Columns))
	for _, c := range s.Columns {
		if c.Method != config.MaskNone {
			out = append(out, c)
		}
	}
	return out
}

// MaskedKept は Masked のうち、出力には残る列（drop 以外）を返す。
// drop された列は Dropped が別に返すため、ここには含めない。同じ列を
// 両方に出すと、どちらが「値が変わった」でどちらが「列ごと消えた」かが
// 呼び出し側で見分けにくくなる。
func (s Summary) MaskedKept() []ColumnMask {
	out := make([]ColumnMask, 0, len(s.Columns))
	for _, c := range s.Masked() {
		if c.Method != config.MaskDrop {
			out = append(out, c)
		}
	}
	return out
}

// Dropped は drop で出力から消えた列名を入力順に返す。
func (s Summary) Dropped() []string {
	var out []string
	for _, c := range s.Columns {
		if c.Method == config.MaskDrop {
			out = append(out, c.Name)
		}
	}
	return out
}

// Apply は結果セットにマスクを適用し、新しい結果とサマリを返す。
//
// 入力は書き換えない。drop された列は Columns からも各行からも消える
// （ADR-0004 §3）。マスクした値は常に文字列になり、null だけが nil になる。
func (e *Engine) Apply(res *redash.Result) (*redash.Result, Summary, error) {
	if res == nil {
		// 結果が無いことを空の結果として返すと、0 件のクエリと区別が付かない。
		return nil, Summary{}, errors.New("マスク対象の結果がありません")
	}

	sum := Summary{Columns: make([]ColumnMask, 0, len(res.Columns))}
	cols := make([]redash.Column, 0, len(res.Columns))
	masks := make([]columnMask, 0, len(res.Columns))
	// srcIndex は出力の列 → 入力の列。drop で番号がずれるため保持する。
	srcIndex := make([]int, 0, len(res.Columns))

	for i, c := range res.Columns {
		cm := e.resolve(c.Name)
		sum.Columns = append(sum.Columns, ColumnMask{Name: c.Name, Method: cm.method})
		if cm.method == config.MaskDrop {
			continue
		}
		cols = append(cols, c)
		masks = append(masks, cm)
		srcIndex = append(srcIndex, i)
	}

	rows := make([]redash.Row, 0, len(res.Rows))
	for _, row := range res.Rows {
		out := make(redash.Row, len(srcIndex))
		for j, src := range srcIndex {
			// 列数より短い行は欠けた値を NULL と同じ扱いにする。
			// 長い行の余りは落とす。列名が無い値はマスクの判定ができない。
			var v any
			if src < len(row) {
				v = row[src]
			}
			out[j] = e.maskValue(masks[j], v)
		}
		rows = append(rows, out)
	}

	return &redash.Result{Columns: cols, Rows: rows}, sum, nil
}

// resolve は列1つに適用する方法を決める。
func (e *Engine) resolve(column string) columnMask {
	best := columnMask{method: e.fallback}
	matched := false
	for _, r := range e.rules {
		if !r.matches(column) {
			continue
		}
		if !matched {
			best, matched = r.mask, true
			continue
		}
		best = stronger(best, r.mask)
	}
	return best
}
