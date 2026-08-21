// Package sqlalias は SQL の select list から、結果列の由来になりうる列名を取り出す。
//
// 判断の根拠は docs/adr/0016-sql-alias-mask-propagation.md にある。
//
// # 何のためにあるか
//
// internal/mask は Redash が返した結果の列名にしかルールを照合できない。列名は
// クエリの書き方で決まるため、SELECT email AS contact と書くだけで email に
// 掛けたルールがマッチしなくなる。このパッケージは「contact は email から
// 来ている」という対応を SQL から拾い、マスクを出力列へ伝播させるための
// 材料を渡す。
//
// # 過剰近似で設計する
//
// 正確な列レベル lineage は解析できない。* の展開にはスキーマカタログが要り、
// sumiq はそれを持たない。そのため、ここが返す由来は「取りこぼさないこと」を
// 優先した過剰近似であり、関係の無い列名（テーブル名や型名）が混ざることが
// ある。混ざれば余計にマスクするだけで済むが、落とすとマスクが黙って外れる。
//
// スコープも潰す。別の SELECT の同名の別名は同じものとして扱う。これも
// 過剰マスク側の割り切りで、原因は mask.Summary の Via から追える。
//
// # 読めなかったことを「問題なし」にしない
//
// 読めない構文・SELECT の無いクエリ・出力名を決められない項目は、解析できた
// ことにせず判定不能（Undetermined）として返す。呼び出し側がそれをエラーに
// するか実行を続けるかは、データソース単位の alias_guard が決める。
//
// このパッケージは internal/config も internal/redash も import しない。
// 他パッケージの型を知らない葉パッケージとして保つ。
package sqlalias

import (
	"fmt"
	"slices"
	"strings"
)

// Reason は解析できなかった理由の分類。
type Reason int

const (
	// ReasonUnreadable は SQL を字句に分解できなかった。引用符・コメント・
	// 括弧が閉じていない、方言で終端が変わる文字列リテラル、複数ステートメント。
	ReasonUnreadable Reason = iota + 1
	// ReasonNoSelect は SELECT が1つも見つからなかった。非 SQL のクエリランナーや
	// SHOW / CALL のような文。
	ReasonNoSelect
	// ReasonUnknownOutput は出力名を決められない項目があり、結果列と位置でも
	// 対応付けられなかった。
	ReasonUnknownOutput
)

func (r Reason) String() string {
	switch r {
	case ReasonUnreadable:
		return "SQL を字句として読めませんでした"
	case ReasonNoSelect:
		return "SELECT が見つかりませんでした"
	case ReasonUnknownOutput:
		return "出力列名を決められない項目があります"
	}
	return "不明な理由"
}

// UndeterminedError は列の由来を判定できなかったことを表す。
//
// 「由来が無い」ことと区別できるようにするための型。呼び出し側はこれを
// 見て実行を止めるか続けるかを決める。
type UndeterminedError struct {
	Reason Reason
	// Detail は読めなかった箇所や、対応付けができなかった項目。
	// SQL 本文やリテラルの値は入れない（列名までに留める）。
	Detail string
	// Advice はどう書けば通るか。
	Advice string
}

func (e *UndeterminedError) Error() string {
	msg := e.Reason.String()
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Advice != "" {
		msg += "。" + e.Advice
	}
	return msg
}

// Exemption は許可関数（propagation_exempt_functions）が伝播を止めた1件。
type Exemption struct {
	// Function は伝播を止めた許可関数の名前。設定に書かれた綴りで入る。
	Function string
	// Column は伝播しなかった元の列名。
	Column string
}

// Origin は結果列1つの由来。
type Origin struct {
	// Sources は値の由来になりうる列名。その列自身は含まない。
	Sources []string
	// Exemptions は許可関数が伝播を止めた列。
	Exemptions []Exemption
}

// Analysis は1つのクエリについて、結果列の由来を引けるようにしたもの。
//
// 解析できなかった場合も *Analysis は返る。読めた範囲の対応は保持したまま、
// 判定不能であることを Undetermined が返す。alias_guard: off で実行を
// 続けるときに、解析できた範囲の伝播だけは効かせるため。
type Analysis struct {
	// undetermined は解析できなかった理由。解析できたなら nil。
	undetermined *UndeterminedError
	// alias は出力名（小文字化）→ その項目が参照した列名。全 SELECT の
	// 項目を1つの表に混ぜる（スコープを潰す）。
	alias map[string][]string
	// exemptions は出力名（小文字化）→ 許可関数が伝播を止めた列。
	exemptions map[string][]Exemption
	// top は最も浅い SELECT。結果列に位置で対応させるのはこれだけ。
	// UNION の各枝で複数になる。
	top []selectList
}

// Analyze は sql を解析する。
//
// exemptFunctions は伝播を止める関数名（propagation_exempt_functions）。
// 大文字小文字は無視する。空なら伝播は一切止まらない。
//
// 解析できなくても *Analysis は返る（nil にはならない）。判定不能かどうかは
// Undetermined で確かめること。
func Analyze(sql string, exemptFunctions []string) *Analysis {
	exempt := make(map[string]string, len(exemptFunctions))
	for _, f := range exemptFunctions {
		exempt[fold(f)] = f
	}

	toks, err := tokenize(sql)
	if err != nil {
		return undetermined(ReasonUnreadable, err.Error(),
			"引用符・コメント・括弧を閉じた1文にしてください")
	}
	if err := singleStatement(toks); err != nil {
		return undetermined(ReasonUnreadable, err.Error(),
			"1回の実行で流す SQL は1文にしてください")
	}

	lists := selects(toks, exempt)
	if len(lists) == 0 {
		return undetermined(ReasonNoSelect, "",
			"SELECT を含まないクエリでは列の由来を辿れません")
	}
	if name, ok := columnAliasList(toks); ok {
		return undetermined(ReasonUnknownOutput,
			fmt.Sprintf("%s の列別名リストは、内側の SELECT の項目と対応付けられません", name),
			"内側の SELECT で列に別名（AS）を付け、列別名リストを使わない形にしてください")
	}

	a := newAnalysis()
	minDepth := lists[0].depth
	for _, l := range lists[1:] {
		minDepth = min(minDepth, l.depth)
	}

	for _, l := range lists {
		top := l.depth == minDepth
		if top {
			a.top = append(a.top, l)
		}
		// select list の項目に含まれるスカラーサブクエリなら、外側の項目が
		// 内側の識別子まで参照列として拾っているため、内側の出力名が
		// 分からなくても由来は落ちない。
		absorbed := !top && inSelectList(lists, l)
		// 出力列が外側から名前で引かれない SELECT（WHERE / HAVING / ON の
		// 中のサブクエリ）は、出力名が分からなくても結果列に届かない。
		named := namedOutput[l.context]

		for _, it := range l.items {
			if it.name != "" {
				a.addAlias(it)
				continue
			}
			if it.star || !it.hasOrigin() {
				continue // 伝播するものが無い項目。名前が分からなくても構わない。
			}
			// トップレベルなら結果列と位置で対応させれば辿れる可能性がある
			// （needsPositional）。使えるかは列数が出揃う Columns で決まる。
			if top || absorbed || !named {
				continue
			}
			// 内側の SELECT で別名の無い式。結果列名も位置も辿れない。
			if a.undetermined == nil {
				a.undetermined = &UndeterminedError{
					Reason: ReasonUnknownOutput,
					Detail: fmt.Sprintf("内側の SELECT に別名の無い式があります（参照している列: %s）",
						strings.Join(it.refs, ", ")),
					Advice: aliasAdvice,
				}
			}
		}
	}
	return a
}

// addAlias は出力名の分かった項目を別名の表に足す。
//
// 自分自身への参照（SELECT email のような単純な列参照）は入れない。
// closure が self を除くため辿っても何も増えず、列の数だけ表が膨らむ。
func (a *Analysis) addAlias(it item) {
	key := fold(it.name)
	for _, ref := range it.refs {
		if !strings.EqualFold(ref, it.name) {
			a.alias[key] = append(a.alias[key], ref)
		}
	}
	a.exemptions[key] = append(a.exemptions[key], it.exempted...)
}

// needsPositional はトップレベルの select list に「出力名が分からず、かつ
// 由来を持つ項目」があるかを返す。あるときだけ結果列との位置対応が要る。
func (a *Analysis) needsPositional() bool {
	for _, l := range a.top {
		for _, it := range l.items {
			if it.name == "" && !it.star && it.hasOrigin() {
				return true
			}
		}
	}
	return false
}

// aliasAdvice は出力名が分からないときの案内。
const aliasAdvice = "その式に別名（AS）を付けて出力列名を明示してください"

func newAnalysis() *Analysis {
	return &Analysis{
		alias:      map[string][]string{},
		exemptions: map[string][]Exemption{},
	}
}

func undetermined(r Reason, detail, advice string) *Analysis {
	a := newAnalysis()
	a.undetermined = &UndeterminedError{Reason: r, Detail: detail, Advice: advice}
	return a
}

// Undetermined は解析できなかった理由を返す。解析できたなら nil。
//
// 結果列と突き合わせて初めて分かる理由（位置対応が使えない）はここには
// 出ない。それは Columns が返す。
func (a *Analysis) Undetermined() *UndeterminedError {
	return a.undetermined
}

// Columns は結果列 names それぞれの由来を、names と同じ順で返す。
//
// 対応付けができなければ *UndeterminedError を返す。その場合も戻り値の
// []Origin は names と同じ長さで、名前で引けた範囲だけを埋めてある
// （alias_guard: off で実行を続けるときに使う）。
func (a *Analysis) Columns(names []string) ([]Origin, *UndeterminedError) {
	out := make([]Origin, len(names))
	for i, n := range names {
		out[i] = Origin{
			Sources:    a.closure([]string{n}, n),
			Exemptions: a.expandExemptions(a.exemptions[fold(n)]),
		}
	}
	if a.undetermined != nil {
		return out, a.undetermined
	}
	if !a.needsPositional() {
		return out, nil
	}
	return out, a.bindByPosition(names, out)
}

// bindByPosition は結果列と select list の項目を位置で対応付ける。
//
// 別名の無い式の結果列名は DB が決めるため、名前では引けない。トップレベルの
// select list に * が無く、項目数と結果列数が一致するときだけ位置で対応させる。
func (a *Analysis) bindByPosition(names []string, out []Origin) *UndeterminedError {
	for _, l := range a.top {
		if hasStar(l) {
			return &UndeterminedError{
				Reason: ReasonUnknownOutput,
				Detail: "別名の無い式と * が同じ select list にあり、結果列と位置で対応付けられません",
				Advice: aliasAdvice,
			}
		}
		if len(l.items) != len(names) {
			return &UndeterminedError{
				Reason: ReasonUnknownOutput,
				Detail: fmt.Sprintf("別名の無い式があり、select list の項目数（%d）と結果列数（%d）が一致しません",
					len(l.items), len(names)),
				Advice: aliasAdvice,
			}
		}
	}

	for _, l := range a.top {
		for i, it := range l.items {
			out[i].Sources = mergeNames(out[i].Sources, a.closure(it.refs, names[i]))
			out[i].Exemptions = a.expandExemptions(append(out[i].Exemptions, it.exempted...))
		}
	}
	return nil
}

// expandExemptions は伝播を止めた列を、その列の由来まで広げる。
//
// 許可関数の引数がさらに別名だった場合（count(contact) の contact が
// email 由来）、実際に止まったのは元の列に掛かっていたマスクである。
// 広げないと、弱化が起きたことを通知に出せない。
func (a *Analysis) expandExemptions(all []Exemption) []Exemption {
	out := make([]Exemption, 0, len(all))
	for _, e := range all {
		out = append(out, e)
		for _, src := range a.closure([]string{e.Column}, e.Column) {
			out = append(out, Exemption{Function: e.Function, Column: src})
		}
	}
	return dedupeExemptions(out)
}

// closure は seeds から辿れる列名をすべて返す。self と同じ名前は除く。
//
// 再帰 CTE や自己参照する別名（SELECT email AS email）で回らないよう、
// 一度辿った名前は visited で止める。
func (a *Analysis) closure(seeds []string, self string) []string {
	// visited は辿る順のキューも兼ねる。add は初出のときだけ足すため、
	// 一度辿った名前を二度辿らない。
	var visited nameSet
	for _, s := range seeds {
		visited.add(s)
	}
	for i := 0; i < len(visited.names); i++ {
		for _, src := range a.alias[fold(visited.names[i])] {
			visited.add(src)
		}
	}

	// 由来が無いことは nil で表す。空スライスと使い分けはしない。
	var out []string
	for _, name := range visited.names {
		if !strings.EqualFold(name, self) {
			out = append(out, name)
		}
	}
	return sortNames(out)
}

// inSelectList は l が別の SELECT の select list の中にあるかを返す。
//
// FROM 句の派生テーブルや CTE の本体は select list の外にあるため false になる。
// そちらは出力名が分からないと本当に由来が辿れない。
func inSelectList(all []selectList, l selectList) bool {
	for _, o := range all {
		if o.keyword == l.keyword {
			continue
		}
		if o.start <= l.keyword && l.keyword < o.end {
			return true
		}
	}
	return false
}

// hasStar は select list に * / t.* があるかを返す。
func hasStar(l selectList) bool {
	for _, it := range l.items {
		if it.star {
			return true
		}
	}
	return false
}

// singleStatement は複数ステートメントでないことを確かめる。
//
// 末尾の ; だけは1文の終わりとして通す。
func singleStatement(toks []token) error {
	for i, t := range toks {
		if t.kind == kindPunct && t.text == ";" && i != len(toks)-1 {
			return fmt.Errorf("; で区切られた複数のステートメントは扱えません")
		}
	}
	return nil
}

// mergeNames は2つの列名の並びを、大文字小文字を無視して重ねる。
func mergeNames(a, b []string) []string {
	var set nameSet
	for _, n := range a {
		set.add(n)
	}
	for _, n := range b {
		set.add(n)
	}
	return sortNames(set.list())
}

// sortNames は列名を並べ替える。同じ SQL から必ず同じ並びを返すため。
func sortNames(names []string) []string {
	slices.SortFunc(names, func(x, y string) int {
		return strings.Compare(fold(x), fold(y))
	})
	return names
}

// dedupeExemptions は同じ（関数, 列）の組を1つにまとめる。
func dedupeExemptions(all []Exemption) []Exemption {
	if len(all) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(all))
	out := make([]Exemption, 0, len(all))
	for _, e := range all {
		key := fold(e.Function) + "\x00" + fold(e.Column)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	slices.SortFunc(out, func(x, y Exemption) int {
		if c := strings.Compare(fold(x.Column), fold(y.Column)); c != 0 {
			return c
		}
		return strings.Compare(fold(x.Function), fold(y.Function))
	})
	return out
}

// nameSet は列名の集合。大文字小文字を無視して重ね、綴りは最初に現れた
// ものを残す。利用者が書いた通りの名前を通知に出すため。
type nameSet struct {
	seen  map[string]bool
	names []string
}

// add は name を加える。新しく加わったときだけ true を返す。
func (s *nameSet) add(name string) bool {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	k := fold(name)
	if s.seen[k] {
		return false
	}
	s.seen[k] = true
	s.names = append(s.names, name)
	return true
}

func (s *nameSet) has(name string) bool { return s.seen[fold(name)] }

func (s *nameSet) list() []string { return s.names }

// fold は識別子を突き合わせるための形に直す。
//
// SQL の識別子の畳み方は DBMS で違い、同じ列が email でも Email でも
// 返りうる。internal/mask のパターン照合も大文字小文字を無視するため、
// ここも無視する側に揃える。
func fold(s string) string { return strings.ToLower(s) }
