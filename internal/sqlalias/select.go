package sqlalias

import "strings"

// selectList は SELECT 1つ分の select list。
type selectList struct {
	// depth は SELECT キーワードを囲む括弧の深さ。最も浅い SELECT が
	// 結果列に対応する。
	depth int
	// keyword は SELECT キーワードの token 位置。
	keyword int
	// context は SELECT を囲む括弧の直前の語（小文字化）。括弧の外なら空。
	//
	// この SELECT の出力列が外側から名前で参照されうるか（CTE の本体か、
	// FROM 句の派生テーブルか）を見分けるために持つ。
	context string
	// start / end は select list（SELECT の次から FROM 等の直前まで）の token 範囲。
	//
	// 別の SELECT がこの範囲の中にあれば、その SELECT はスカラーサブクエリと
	// して select list の項目に含まれている。項目の参照列を集めるときに
	// 内側の識別子まで拾っているため、内側の出力名が分からなくても由来は落ちない。
	start, end int
	items      []item
}

// item は select list の項目1つ。
type item struct {
	// name は出力列名。別名も単純な列参照も無い式では空になる。
	name string
	// refs は値の由来になりうる列名。
	refs []string
	// exempted は許可関数の内側にだけ現れた列名。
	exempted []Exemption
	// star は * / t.* の項目かどうか。
	star bool
}

// hasOrigin はこの項目が何かを伝播しうるかを返す。
//
// 由来を持たない項目（count(*) や定数）は、出力名が分からなくても
// 伝播するものが無いため、対応付けができなくても構わない。
func (i item) hasOrigin() bool {
	return len(i.refs) > 0 || len(i.exempted) > 0
}

// selectListEnd は select list の終わりを示すキーワード。
//
// 同じ深さでこれらが現れたところまでが select list。UNION 等の集合演算は
// 枝ごとに別の SELECT として読む。
var selectListEnd = map[string]bool{
	"from": true, "where": true, "group": true, "having": true,
	"order": true, "limit": true, "offset": true, "window": true,
	"qualify": true, "fetch": true, "into": true, "for": true,
	"union": true, "intersect": true, "except": true, "minus": true,
}

// selects は SQL 中のすべての SELECT の select list を返す。
// CTE・サブクエリ・集合演算の各枝がそれぞれ1つになる。
func selects(toks []token, exempt map[string]string) []selectList {
	var out []selectList
	for i, t := range toks {
		if t.kind != kindIdent || !strings.EqualFold(t.text, "select") {
			continue
		}
		if i > 0 && toks[i-1].kind == kindPunct && toks[i-1].text == "." {
			continue // t.select のように修飾された識別子。
		}
		end := selectListEndIndex(toks, i)
		list := selectList{
			depth:   t.depth,
			context: selectContext(toks, i),
			keyword: i,
			start:   i + 1,
			end:     end,
		}
		for _, part := range splitItems(toks[i+1:end], t.depth) {
			list.items = append(list.items, parseItem(part, t.depth, exempt))
		}
		out = append(out, list)
	}
	return out
}

// selectContext は start にある SELECT を囲む括弧の直前の語を返す。
// 括弧の外にある（最も外側の）SELECT なら空を返す。
func selectContext(toks []token, start int) string {
	depth := toks[start].depth
	if depth == 0 {
		return ""
	}
	// SELECT と囲む開き括弧の間の token はすべて depth 以上。逆向きに辿って
	// 最初に depth を下回るのが、その開き括弧そのもの。
	for j := start - 1; j >= 0; j-- {
		if toks[j].depth >= depth {
			continue
		}
		if j == 0 || toks[j].kind != kindPunct || toks[j].text != "(" {
			return ""
		}
		return fold(toks[j-1].text)
	}
	return ""
}

// namedOutput は、SELECT の出力列が外側から名前で参照されうる文脈。
//
// CTE の本体（AS の後ろ）と FROM 句の派生テーブルだけが、外側から列名で
// 引かれる。WHERE / HAVING / ON の中のサブクエリは結果列にならないため、
// 出力名が分からなくてもマスクが外れる経路にならない。
var namedOutput = map[string]bool{
	"as": true, "from": true, "join": true, "lateral": true, ",": true,
}

// selectListEndIndex は start にある SELECT の select list が終わる位置を返す。
func selectListEndIndex(toks []token, start int) int {
	depth := toks[start].depth
	for j := start + 1; j < len(toks); j++ {
		t := toks[j]
		switch {
		case t.depth < depth:
			return j // SELECT を囲む括弧が閉じた。
		case t.depth > depth:
			continue // 入れ子の括弧の中。カンマも AS も外の select list には効かない。
		case t.kind == kindPunct && t.text == ";":
			return j
		case t.kind == kindIdent && selectListEnd[fold(t.text)]:
			return j
		}
	}
	return len(toks)
}

// splitItems は select list を深さ depth のカンマで項目に分ける。
func splitItems(toks []token, depth int) [][]token {
	toks = stripSelectModifiers(toks, depth)

	var out [][]token
	start := 0
	for i, t := range toks {
		if t.kind == kindPunct && t.text == "," && t.depth == depth {
			if item := toks[start:i]; len(item) > 0 {
				out = append(out, item)
			}
			start = i + 1
		}
	}
	if rest := toks[start:]; len(rest) > 0 {
		out = append(out, rest)
	}
	return out
}

// stripSelectModifiers は select list の先頭に付く修飾子を落とす。
//
// 落とさずに項目の一部として読むと「式 + 別名」の形と見分けが付かなくなる
// （SELECT DISTINCT email の email が別名に見える）。
func stripSelectModifiers(toks []token, depth int) []token {
	for len(toks) > 0 && toks[0].kind == kindIdent && toks[0].depth == depth {
		switch fold(toks[0].text) {
		case "distinct", "unique", "all":
			toks = toks[1:]
			// PostgreSQL の DISTINCT ON (a, b)。
			if len(toks) > 1 && toks[0].kind == kindIdent && strings.EqualFold(toks[0].text, "on") &&
				toks[1].kind == kindPunct && toks[1].text == "(" {
				toks = skipGroup(toks[1:], depth)
			}
		case "top":
			// SQL Server の TOP n / TOP (n) PERCENT WITH TIES。
			toks = toks[1:]
			switch {
			case len(toks) > 0 && toks[0].kind == kindLiteral:
				toks = toks[1:]
			case len(toks) > 0 && toks[0].kind == kindPunct && toks[0].text == "(":
				toks = skipGroup(toks, depth)
			}
			if len(toks) > 0 && toks[0].kind == kindIdent && strings.EqualFold(toks[0].text, "percent") {
				toks = toks[1:]
			}
			if len(toks) > 1 && toks[0].kind == kindIdent && strings.EqualFold(toks[0].text, "with") &&
				toks[1].kind == kindIdent && strings.EqualFold(toks[1].text, "ties") {
				toks = toks[2:]
			}
		default:
			return toks
		}
	}
	return toks
}

// skipGroup は toks[0] の開き括弧に対応する閉じ括弧の次からを返す。
func skipGroup(toks []token, depth int) []token {
	for i := 1; i < len(toks); i++ {
		if toks[i].kind == kindPunct && toks[i].text == ")" && toks[i].depth == depth {
			return toks[i+1:]
		}
	}
	return nil
}

// columnAliasList は列別名リストがあれば、その名前を返す。
//
//	WITH q(c1, c2) AS (SELECT ...)      CTE の列別名リスト
//	FROM (SELECT ...) x(c1, c2)         派生テーブルの列別名リスト
//	CROSS JOIN UNNEST(arr) AS t(v)      同じ形
//
// この形は出力列を丸ごと改名するが、内側の SELECT には改名後の名前が
// どこにも現れない。位置で対応させるには「どの CTE / 派生テーブルの何番目か」を
// 辿る必要があり、スコープを潰す解析では追えない。読めたことにせず、
// 呼び出し側に判定不能として返すための検出。
func columnAliasList(toks []token) (string, bool) {
	for i, t := range toks {
		if t.kind != kindPunct || t.text != "(" {
			continue
		}
		// 直前が識別子（CTE 名・派生テーブルの別名）であること。キーワードは
		// 除く。OVER (PARTITION BY a) や FILTER (WHERE ...) を数えない。
		if i == 0 || toks[i-1].kind != kindIdent || keywords[fold(toks[i-1].text)] {
			continue
		}
		end, ok := identListEnd(toks, i)
		if !ok {
			continue
		}
		name := toks[i-1].text
		// WITH q(c) AS ( ... の形。
		if end+2 < len(toks) && toks[end+1].kind == kindIdent &&
			strings.EqualFold(toks[end+1].text, "as") &&
			toks[end+2].kind == kindPunct && toks[end+2].text == "(" {
			return name, true
		}
		// ) x(c) / ) AS x(c) の形。
		if closesGroup(toks, i-1) {
			return name, true
		}
	}
	return "", false
}

// identListEnd は toks[i] の開き括弧が識別子とカンマだけの並びを囲んでいる
// なら、対応する閉じ括弧の位置を返す。空の括弧は false。
func identListEnd(toks []token, i int) (int, bool) {
	depth := toks[i].depth
	idents := 0
	for j := i + 1; j < len(toks); j++ {
		t := toks[j]
		switch {
		case t.kind == kindPunct && t.text == ")" && t.depth == depth:
			return j, idents > 0
		case t.kind == kindQuoted:
			idents++
		case t.kind == kindIdent && !keywords[fold(t.text)]:
			idents++
		case t.kind == kindPunct && t.text == ",":
		default:
			return 0, false
		}
	}
	return 0, false
}

// closesGroup は toks[j] の直前が閉じ括弧かを返す。AS を挟む形も見る。
func closesGroup(toks []token, j int) bool {
	k := j - 1
	if k >= 0 && toks[k].kind == kindIdent && strings.EqualFold(toks[k].text, "as") {
		k--
	}
	return k >= 0 && toks[k].kind == kindPunct && toks[k].text == ")"
}

// parseItem は項目1つから出力名と参照する列名を取り出す。
func parseItem(toks []token, depth int, exempt map[string]string) item {
	if isStar(toks) {
		return item{star: true}
	}

	name, expr := splitAlias(toks, depth)
	if ref, ok := simpleRef(expr); ok {
		if name == "" {
			name = ref
		}
		return item{name: name, refs: []string{ref}}
	}

	refs, exempted := collectRefs(expr, exempt)
	return item{name: name, refs: refs, exempted: exempted}
}

// isStar は項目が * / t.* かを返す。
func isStar(toks []token) bool {
	if len(toks) == 0 {
		return false
	}
	last := toks[len(toks)-1]
	if last.kind != kindPunct || last.text != "*" {
		return false
	}
	// 修飾子だけが前に付ける。a * b や count(*) はここで落ちる。
	for _, t := range toks[:len(toks)-1] {
		switch {
		case t.kind == kindIdent || t.kind == kindQuoted:
		case t.kind == kindPunct && t.text == ".":
		default:
			return false
		}
	}
	return true
}

// splitAlias は項目を出力名と式に分ける。別名が無ければ名前は空を返す。
func splitAlias(toks []token, depth int) (string, []token) {
	n := len(toks)
	if n < 2 {
		return "", toks
	}
	last := toks[n-1]
	if last.depth != depth || !isAliasToken(last) {
		return "", toks
	}
	// AS <ident>。
	if n >= 3 && toks[n-2].kind == kindIdent && toks[n-2].depth == depth &&
		strings.EqualFold(toks[n-2].text, "as") {
		return last.text, toks[:n-2]
	}
	// AS 省略形。直前が式を終えられる token のときだけ別名と読む。
	if endsExpression(toks[n-2]) {
		return last.text, toks[:n-1]
	}
	return "", toks
}

// isAliasToken は別名になれる token かを返す。
//
// キーワードは別名として読まない。x IS NULL の NULL を別名と読むと、
// 出力名が分かったことになって位置対応が働かなくなる。
func isAliasToken(t token) bool {
	if t.kind == kindQuoted {
		return true
	}
	return t.kind == kindIdent && !keywords[fold(t.text)]
}

// endsExpression は token が式の終わりになれるかを返す。
//
// ここを緩めると a AND b の b を別名と読み違える。読み違えると出力名を
// 「分かった」ことにしてしまい、DB が実際に付ける結果列名へ伝播が届かない。
// 分からない側に倒す方が安全（位置対応か判定不能に落ちる）。
func endsExpression(t token) bool {
	switch t.kind {
	case kindQuoted, kindLiteral:
		return true
	case kindPunct:
		return t.text == ")"
	}
	if !keywords[fold(t.text)] {
		return true
	}
	return expressionEnders[fold(t.text)]
}

// expressionEnders は式の末尾になれるキーワード。
var expressionEnders = map[string]bool{
	"end": true, "null": true, "true": true, "false": true,
}

// simpleRef は式が単純な列参照（col / t.col / "col"）ならその列名を返す。
//
// 単純な列参照ではキーワード表を使わない。date や text のように予約語・
// 型名と綴りが同じ列名があり、表で落とすと伝播が届かなくなる。代わりに、
// 単独で現れても列を指さない語だけを除く。
func simpleRef(toks []token) (string, bool) {
	if len(toks) == 0 || len(toks)%2 == 0 {
		// 偶数個は a . b . のような並び。修飾子と . の交替にならない。
		return "", false
	}
	for i, t := range toks {
		if i%2 == 1 {
			if t.kind != kindPunct || t.text != "." {
				return "", false
			}
			continue
		}
		if t.kind != kindIdent && t.kind != kindQuoted {
			return "", false
		}
	}
	last := toks[len(toks)-1]
	if last.kind == kindIdent && bareConstants[fold(last.text)] {
		return "", false
	}
	return last.text, true
}

// bareConstants は単独で現れても列を指さない語。
var bareConstants = map[string]bool{
	"null": true, "true": true, "false": true, "default": true,
	"current_date": true, "current_time": true, "current_timestamp": true,
	"localtime": true, "localtimestamp": true, "sysdate": true,
	"current_user": true, "session_user": true, "user": true,
}

// collectRefs は式に現れる列名を集める。
//
// 許可関数の内側にだけ現れた識別子は refs に入れず exempted に回す。外側にも
// 現れていれば refs に入る（count(email) OVER (PARTITION BY email) の2つ目の
// email は伝播する）。入れ子は「祖先に許可関数の呼び出しがあるか」で見る。
func collectRefs(toks []token, exempt map[string]string) ([]string, []Exemption) {
	var refs nameSet
	// cand は許可関数の内側で見つけた識別子。refs に入らなかったものだけを
	// 最後に Exemption にする。
	var cand []Exemption

	// outer は括弧ごとの、1つ外側の文脈。閉じ括弧で戻すために積む。
	var outer []frame
	cur := frame{}

	for i, t := range toks {
		switch {
		case t.kind == kindPunct && t.text == "(":
			outer = append(outer, cur)
			next := frame{call: isCallOpen(toks, i)}
			// 許可関数は祖先まで見る。内側の入れ子も覆われたままにする。
			if cur.exempt != "" {
				next.exempt = cur.exempt
			} else {
				next.exempt = exemptCallName(toks, i, exempt)
			}
			cur = next
		case t.kind == kindPunct && t.text == ")":
			if len(outer) > 0 {
				cur = outer[len(outer)-1]
				outer = outer[:len(outer)-1]
			}
		case t.kind == kindIdent || t.kind == kindQuoted:
			name, ok := refName(toks, i, cur.call)
			if !ok {
				continue
			}
			if cur.exempt == "" {
				refs.add(name)
				continue
			}
			cand = append(cand, Exemption{Function: cur.exempt, Column: name})
		}
	}

	// 許可関数の外にも現れていれば伝播している。止まっていないので通知しない。
	// 同じ組の重複は、通知に出す直前の dedupeExemptions が落とす。
	var exempted []Exemption
	for _, c := range cand {
		if !refs.has(c.Column) {
			exempted = append(exempted, c)
		}
	}
	return refs.list(), exempted
}

// frame は括弧1つ分の文脈。
type frame struct {
	// exempt は非空なら、この括弧を覆っている許可関数の名前。
	exempt string
	// call は関数呼び出しの引数リストかどうか。
	//
	// 引数リストの中の FROM は句ではなく引数の区切りになる
	// （extract(year FROM ts) / trim(both ' ' FROM s) / substring(s FROM 1)）。
	// 区別しないと、区切りの後ろにある列名を「サブクエリのテーブル名」として
	// 捨ててしまい、伝播が黙って落ちる。
	call bool
}

// isCallOpen は開き括弧が関数呼び出しの引数リストを開いたかを返す。
//
// 直前が識別子で、かつキーワードでないときだけ呼び出しとみなす。
// x IN (SELECT ...) や OVER (PARTITION BY ...) を呼び出しに数えると、
// 中の FROM をテーブル名の目印として使えなくなる。
func isCallOpen(toks []token, i int) bool {
	return i > 0 && toks[i-1].kind == kindIdent && !keywords[fold(toks[i-1].text)]
}

// refName は toks[i] が列参照ならその名前を返す。
// inCall は関数呼び出しの引数リストの中にいるかどうか。
func refName(toks []token, i int, inCall bool) (string, bool) {
	t := toks[i]
	if i+1 < len(toks) && toks[i+1].kind == kindPunct {
		switch toks[i+1].text {
		case ".":
			return "", false // a.b の a。列名は最後の要素だけ。
		case "(":
			return "", false // 関数名。
		}
	}
	// 項目の中のサブクエリに現れるテーブル名を列名として拾わない。
	// 関数の引数リストの中の FROM は句ではなく区切りなので、その後ろは
	// 列参照でありうる（extract(year FROM birth_date)）。
	if !inCall && i > 0 && toks[i-1].kind == kindIdent {
		switch fold(toks[i-1].text) {
		case "from", "join":
			return "", false
		}
	}
	if t.kind == kindIdent && keywords[fold(t.text)] {
		return "", false
	}
	return t.text, true
}

// exemptCallName は toks[i] の開き括弧が許可関数の呼び出しなら、その関数名を
// 設定に書かれた綴りで返す。許可関数でなければ空を返す。
func exemptCallName(toks []token, i int, exempt map[string]string) string {
	if i == 0 {
		return ""
	}
	name := toks[i-1]
	// 引用識別子の呼び出しは許可しない。許可リストは完全一致・修飾なしだけを見る。
	if name.kind != kindIdent {
		return ""
	}
	// スキーマ修飾付きの呼び出し（pg_catalog.count）は別の関数でありうる。
	if i >= 2 && toks[i-2].kind == kindPunct && toks[i-2].text == "." {
		return ""
	}
	return exempt[fold(name.text)]
}

// keywords は式の中に現れても列参照ではない語。
//
// 載せ忘れた語は列名として扱われ、余計にマスクする方向（安全側）に倒れる。
// 逆に列名になりうる語を載せると伝播が落ちるため、句や演算子としてしか
// 使われない語だけに絞る。型名（int / text / date 等）は載せない。列名と
// 綴りが同じことがあり、CAST の中の型名を列名として拾う過剰さの方が安全。
var keywords = map[string]bool{
	"all": true, "and": true, "any": true, "as": true, "asc": true,
	"at": true, "between": true, "by": true, "case": true, "cast": true,
	"collate": true, "cross": true, "desc": true, "distinct": true,
	"else": true, "end": true, "escape": true, "except": true,
	"exists": true, "false": true, "fetch": true, "filter": true,
	"for":  true,
	"from": true, "full": true, "group": true, "having": true,
	"ilike": true, "in": true, "inner": true, "intersect": true,
	"into": true, "is": true, "join": true, "lateral": true,
	"left": true, "like": true, "limit": true, "minus": true,
	"natural": true, "not": true, "null": true, "nulls": true,
	"offset": true, "on": true, "or": true, "order": true,
	"outer": true, "over": true, "partition": true, "qualify": true,
	"recursive": true, "right": true, "select": true, "similar": true,
	"some": true, "then": true, "true": true, "union": true,
	"unique": true, "using": true, "when": true, "where": true,
	"window": true, "with": true, "within": true,
}
