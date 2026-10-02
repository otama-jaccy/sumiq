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
// # 別名（AS）で改名された列
//
// 列名はクエリの書き方で決まるため、SELECT email AS contact と書くだけで
// email に掛けたルールがマッチしなくなる。そこで internal/sqlalias が SQL から
// 取り出した「その列の由来になりうる列名」にもルールを照合し、最も強いものを
// 出力列に適用する（伝播）。判断は docs/adr/0016-sql-alias-mask-propagation.md。
//
// 伝播は「複数マッチは強い方が勝つ」規則の拡張として入れてある。そのため
// 別名の側に method: none を書いても伝播は打ち消せない。伝播を止める手段は
// masking.propagation_exempt_functions（許可関数）だけに一本化してある。
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
	"github.com/otama-jaccy/sumiq/internal/sqlalias"
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
	// strictAlias は結果列と SQL の列を対応付けられなかったときに
	// エラーにするか。データソースの alias_guard から決まる。
	strictAlias bool
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

	e := &Engine{
		fallback:    fallback,
		salt:        make([]byte, saltLen),
		strictAlias: ds.AliasGuard.Strict(),
	}
	if _, err := rand.Read(e.salt); err != nil {
		// salt を作れないまま固定値に落とすと、ハッシュが実行をまたいで
		// 復元可能になる。マスクを弱めるくらいなら実行しない。
		return nil, fmt.Errorf("hash 用の salt を生成できませんでした: %w", err)
	}

	for _, r := range m.Rules {
		// 検査は「そのルールが今回使われるか」と切り離して走らせる。
		// 先にスコープで絞ると、別のデータソース向けのルールの書き間違いが、
		// そのデータソースを引く日まで見つからない。
		cr, err := compileRule(r, cfg.DataSources)
		if err != nil {
			// r.Origin はマージ後の union index ではなく、利用者が開くファイルの
			// 何番目かを示す（config.RuleOrigin）。全レイヤの和集合での添字を
			// そのまま出すと、開いたファイルのどの行かと一致しなくなる。
			return nil, fmt.Errorf("%s %v: %w", r.Origin, r.Patterns, err)
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
	// AliasUndetermined は結果列と SQL の列を対応付けられなかった理由。
	// 対応付けできたなら nil。
	//
	// 入るのは alias_guard: off のときだけ（strict では Apply がエラーにする）。
	// 呼び出し側が毎回警告を出せるようにするために持つ。伝播が効いていない
	// ことは出力を見ても分からない。
	AliasUndetermined *sqlalias.UndeterminedError

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
	// Via は伝播元の列名。直接マッチで決まったなら空。
	//
	// 過剰マスクの原因を追えるようにするために持つ。internal/sqlalias は
	// スコープを潰す（別スコープの同名の別名を同じものとして扱う）ため、
	// 身に覚えのない列が伏せられたときの手掛かりがこれしかない。
	Via string
	// Exempted は許可関数が伝播を止めた元の列名。弱化が起きたことを
	// 毎回見えるようにするために持つ。
	Exempted []sqlalias.Exemption
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

// Exempted は許可関数が伝播を止めた列を入力順に返す。
//
// マスクが掛かっていない列（method: none）も含める。止まったことが見えなければ
// 弱化に気付けない。
func (s Summary) Exempted() []ColumnMask {
	var out []ColumnMask
	for _, c := range s.Columns {
		if len(c.Exempted) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Dropped は drop で出力から消えた列を入力順に返す。
//
// 列名だけでなく ColumnMask を返すのは、伝播で消えた列の由来（Via）を
// 通知に出せるようにするため。出力に残らない列は、なぜ消えたのかを
// 結果から確かめられない。
func (s Summary) Dropped() []ColumnMask {
	var out []ColumnMask
	for _, c := range s.Columns {
		if c.Method == config.MaskDrop {
			out = append(out, c)
		}
	}
	return out
}

// Apply は結果セットにマスクを適用し、新しい結果とサマリを返す。
//
// 入力は書き換えない。drop された列は Columns からも各行からも消える
// （ADR-0004 §3）。マスクした値は常に文字列になり、null だけが nil になる。
//
// a は実行した SQL の解析結果（internal/sqlalias）。省略できる形にしていない
// のは、渡し忘れを実行時ではなくコンパイル時に見つけるため。渡し忘れると
// 別名で改名された列のマスクが黙って外れる。
func (e *Engine) Apply(res *redash.Result, a *sqlalias.Analysis) (*redash.Result, Summary, error) {
	if res == nil {
		// 結果が無いことを空の結果として返すと、0 件のクエリと区別が付かない。
		return nil, Summary{}, errors.New("マスク対象の結果がありません")
	}
	if a == nil {
		// nil を「由来が無い」として扱うと、伝播が丸ごと消えたまま実行される。
		return nil, Summary{}, errors.New("列の由来の解析結果がありません")
	}

	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	// 対応付けができなかったこと（判定不能）を「由来なし」に倒さない。
	// strict なら実行を止め、off なら解析できた範囲の伝播だけを効かせる。
	origins, undetermined := a.Columns(names)
	if err := e.aliasGuardError(undetermined); err != nil {
		return nil, Summary{}, err
	}

	sum := Summary{
		AliasUndetermined: undetermined,
		Columns:           make([]ColumnMask, 0, len(res.Columns)),
	}
	cols := make([]redash.Column, 0, len(res.Columns))
	masks := make([]columnMask, 0, len(res.Columns))
	// srcIndex は出力の列 → 入力の列。drop で番号がずれるため保持する。
	srcIndex := make([]int, 0, len(res.Columns))

	for i, c := range res.Columns {
		cm, via, exempted := e.resolveWithOrigin(c.Name, origins[i])
		sum.Columns = append(sum.Columns, ColumnMask{
			Name:     c.Name,
			Method:   cm.method,
			Via:      via,
			Exempted: exempted,
		})
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

// PrecheckAlias は SQL の解析結果を、Redash に問い合わせる前に検査する。
//
// 判定不能を alias_guard: strict で止めるのは、ネットワークに出る前でなければ
// 意味が無い（mask.New を client.Execute より前に置いてあるのと同じ理由）。
//
// 結果列と突き合わせて初めて分かる理由（位置対応が使えない）はここでは
// 分からない。それは Apply が同じ規則で判定する。
func (e *Engine) PrecheckAlias(a *sqlalias.Analysis) error {
	if a == nil {
		return errors.New("列の由来の解析結果がありません")
	}
	return e.aliasGuardError(a.Undetermined())
}

// aliasGuardError は判定不能を、alias_guard: strict のときだけエラーにする。
//
// 判定不能をどう扱うかの決定と文言をここ1か所に集約する。呼び出し側でも
// 判定すると、利用者から見て同じ状況なのに、どちらの検査が先に効いたかで
// 文言が変わる。片方だけ直したときに挙動がずれる経路にもなる。
func (e *Engine) aliasGuardError(u *sqlalias.UndeterminedError) error {
	if u == nil || !e.strictAlias {
		return nil
	}
	return fmt.Errorf("SQL から結果列の由来を辿れませんでした: %w。"+
		"このデータソースに alias_guard: %s を共有ファイル（%s）で指定すると、"+
		"辿れないクエリでも実行できます（その場合、別名で改名された列に"+
		"マスクは伝播しません）", u, config.AliasGuardOff, config.SharedFileName)
}

// resolveWithOrigin は列1つに適用する方法を、由来も含めて決める。
//
// 出力列名そのものへの照合と、由来になりうる列名への照合のうち、最も強い
// ものが勝つ。既存の「複数マッチは強い方が勝つ」規則をそのまま使うため、
// partial 同士は tighten で「どちらも残す部分」だけが残る。
//
// 由来の側にも default_action が効く。allowlist 運用では、由来が
// method: none で通っている列でも出力列の側で穴を開けていなければ伏せる。
// これは伝播の結果ではなく allowlist 運用そのもの（ADR-0003 §7）。
func (e *Engine) resolveWithOrigin(column string, o sqlalias.Origin) (columnMask, string, []sqlalias.Exemption) {
	cm := e.resolve(column)
	via := ""
	for _, src := range o.Sources {
		sm, ok := e.matchedMask(src)
		if !ok {
			continue
		}
		merged := stronger(cm, sm)
		if merged != cm {
			// 強くした（または partial を狭めた）由来だけを記録する。
			via = src
		}
		cm = merged
	}
	return cm, via, e.stoppedPropagation(o.Exemptions, cm)
}

// stoppedPropagation は許可関数が実際に弱化を起こした分だけを返す。
//
// 許可関数の内側にあった列に、適用済みの方法より強いマスクが掛かっていな
// ければ、止めたものは無い。count(id) のような無害な集計まで並べると、
// 本当に弱化が起きた行が埋もれる。
//
// 判定は resolve ではなく matchedMask で行う。default_action は出力列の側で
// 既に効いており、ここに持ち込むと allowlist 運用では全ての許可関数が
// 「弱化した」ことになる。
func (e *Engine) stoppedPropagation(all []sqlalias.Exemption, applied columnMask) []sqlalias.Exemption {
	var out []sqlalias.Exemption
	for _, x := range all {
		if m, ok := e.matchedMask(x.Column); ok && strength(m.method) > strength(applied.method) {
			out = append(out, x)
		}
	}
	return out
}

// resolve は列1つに適用する方法を決める。
// マッチするルールが1つも無ければ default_action が決める。
func (e *Engine) resolve(column string) columnMask {
	if m, ok := e.matchedMask(column); ok {
		return m
	}
	return columnMask{method: e.fallback}
}

// matchedMask は列名にマッチしたルールのうち最も強いものを返す。
// マッチが1つも無ければ ok は false になる。
//
// default_action を混ぜないのは、伝播（resolveWithOrigin）が「マッチした
// ルールだけ」を由来から拾うため。由来に既定を持ち込むと、allowlist 運用
// （default_action: redact）では、ルールに挙がっていない由来がすべて redact を
// 押し付けることになり、出力列に書いた method: none の穴が塞がる。
// マッチしたルールが既定より優先される（ADR-0003 §7）という規則も崩れる。
func (e *Engine) matchedMask(column string) (columnMask, bool) {
	var best columnMask
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
	return best, matched
}
