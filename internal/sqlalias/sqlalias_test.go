package sqlalias

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// analyze は解析して、判定不能なら失敗させる。
func analyze(t *testing.T, sql string, exempt ...string) *Analysis {
	t.Helper()
	a := Analyze(sql, exempt)
	if u := a.Undetermined(); u != nil {
		t.Fatalf("判定不能になりました: %v", u)
	}
	return a
}

// columns は結果列 cols の由来を取り出す。判定不能なら失敗させる。
func columns(t *testing.T, a *Analysis, cols []string) []Origin {
	t.Helper()
	origins, u := a.Columns(cols)
	if u != nil {
		t.Fatalf("Columns(%v): 判定不能になりました: %v", cols, u)
	}
	return origins
}

// sourcesOf は結果列名 → 由来の列名を返す。
func sourcesOf(t *testing.T, a *Analysis, cols []string) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for i, o := range columns(t, a, cols) {
		got[cols[i]] = o.Sources
	}
	return got
}

func TestAnalyzeSources(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		cols []string
		want map[string][]string
	}{
		{
			name: "AS で別名を付ける",
			sql:  "SELECT email AS contact FROM users",
			cols: []string{"contact"},
			want: map[string][]string{"contact": {"email"}},
		},
		{
			name: "AS 省略形",
			sql:  "SELECT email contact FROM users",
			cols: []string{"contact"},
			want: map[string][]string{"contact": {"email"}},
		},
		{
			name: "修飾付きの列参照は最後の要素だけを見る",
			sql:  "SELECT u.email AS user_contact FROM users u JOIN orders o USING (email)",
			cols: []string{"user_contact"},
			want: map[string][]string{"user_contact": {"email"}},
		},
		{
			name: "二重引用符の識別子",
			sql:  `SELECT "Email" AS "payload/user/email" FROM users`,
			cols: []string{"payload/user/email"},
			want: map[string][]string{"payload/user/email": {"Email"}},
		},
		{
			name: "バッククォートの識別子",
			sql:  "SELECT `user email` AS `c` FROM users",
			cols: []string{"c"},
			want: map[string][]string{"c": {"user email"}},
		},
		{
			name: "角括弧の識別子",
			sql:  "SELECT [email] AS [contact] FROM [users]",
			cols: []string{"contact"},
			want: map[string][]string{"contact": {"email"}},
		},
		{
			name: "引用符の中のエスケープを解く",
			sql:  `SELECT "a""b" AS c FROM t`,
			cols: []string{"c"},
			want: map[string][]string{"c": {`a"b`}},
		},
		{
			name: "非 ASCII の列名",
			sql:  "SELECT 顧客メールアドレス AS c FROM t",
			cols: []string{"c"},
			want: map[string][]string{"c": {"顧客メールアドレス"}},
		},
		{
			name: "単一 CTE",
			sql:  "WITH u AS (SELECT email AS contact FROM users) SELECT contact FROM u",
			cols: []string{"contact"},
			want: map[string][]string{"contact": {"email"}},
		},
		{
			name: "多段 CTE で閉包を取る",
			sql: `WITH u AS (SELECT id, email AS contact FROM users),
			           j AS (SELECT u.contact AS c2, o.amount FROM u JOIN orders o ON u.id = o.user_id)
			      SELECT c2, amount FROM j`,
			cols: []string{"c2", "amount"},
			want: map[string][]string{"c2": {"contact", "email"}, "amount": nil},
		},
		{
			name: "サブクエリ",
			sql:  "SELECT t.contact AS c FROM (SELECT email AS contact FROM users) t",
			cols: []string{"c"},
			want: map[string][]string{"c": {"contact", "email"}},
		},
		{
			name: "スカラーサブクエリは外側の項目が参照列を吸い上げる",
			sql:  "SELECT (SELECT max(email) FROM users) AS m FROM t",
			cols: []string{"m"},
			want: map[string][]string{"m": {"email"}},
		},
		{
			name: "UNION ALL の各枝",
			sql:  "SELECT id AS a FROM x UNION ALL SELECT email AS a FROM y",
			cols: []string{"a"},
			want: map[string][]string{"a": {"email", "id"}},
		},
		{
			// extract / trim / substring / overlay の FROM は句ではなく引数の
			// 区切り。サブクエリのテーブル名と同じ扱いで捨てると、
			// 別名が付いていて判定不能にもならないまま伝播が落ちる。
			name: "関数の引数リストの中の FROM は区切りとして読む",
			sql:  "SELECT extract(year FROM birth_date) AS y FROM u",
			cols: []string{"y"},
			want: map[string][]string{"y": {"birth_date", "year"}},
		},
		{
			name: "trim の FROM も区切りとして読む",
			sql:  "SELECT trim(both ' ' FROM full_name) AS n FROM u",
			cols: []string{"n"},
			want: map[string][]string{"n": {"both", "full_name"}},
		},
		{
			name: "サブクエリのテーブル名は由来にしない",
			sql:  "SELECT (SELECT max(email) FROM users) AS m FROM t",
			cols: []string{"m"},
			want: map[string][]string{"m": {"email"}},
		},
		{
			name: "式の中の複数の列",
			sql:  "SELECT concat(first_name, last_name) AS full_name FROM t",
			cols: []string{"full_name"},
			want: map[string][]string{"full_name": {"first_name", "last_name"}},
		},
		{
			name: "CASE 式",
			sql:  "SELECT CASE WHEN flag THEN email ELSE NULL END AS c FROM t",
			cols: []string{"c"},
			want: map[string][]string{"c": {"email", "flag"}},
		},
		{
			name: "SELECT * は元の列名で返るため由来を足さない",
			sql:  "SELECT * FROM users",
			cols: []string{"id", "email"},
			want: map[string][]string{"id": nil, "email": nil},
		},
		{
			name: "t.* と式の混在",
			sql:  "SELECT t.*, email AS contact FROM users t",
			cols: []string{"id", "email", "contact"},
			want: map[string][]string{"id": nil, "email": nil, "contact": {"email"}},
		},
		{
			name: "内側で改名して外側で * を書いても閉包で拾える",
			sql:  "SELECT * FROM (SELECT email AS contact FROM users) t",
			cols: []string{"contact"},
			want: map[string][]string{"contact": {"email"}},
		},
		{
			name: "DISTINCT は項目の一部として読まない",
			sql:  "SELECT DISTINCT email AS contact FROM t",
			cols: []string{"contact"},
			want: map[string][]string{"contact": {"email"}},
		},
		{
			name: "予約語と綴りが同じ列名も単純な列参照として読む",
			sql:  "SELECT date AS d, text AS t FROM logs",
			cols: []string{"d", "t"},
			want: map[string][]string{"d": {"date"}, "t": {"text"}},
		},
		{
			name: "別名の無い式は位置で対応付ける",
			sql:  "SELECT id, upper(email) FROM users",
			cols: []string{"id", "_col1"},
			want: map[string][]string{"id": nil, "_col1": {"email"}},
		},
		{
			name: "由来を持たない項目は位置対応が要らない",
			sql:  "SELECT count(*) FROM users",
			cols: []string{"f0_"},
			want: map[string][]string{"f0_": nil},
		},
		{
			name: "自分自身は由来に含めない",
			sql:  "SELECT email AS email FROM users",
			cols: []string{"email"},
			want: map[string][]string{"email": nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourcesOf(t, analyze(t, tt.sql), tt.cols)
			want := map[string][]string{}
			for k, v := range tt.want {
				want[k] = v
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("由来 = %v, want %v", got, want)
			}
		})
	}
}

// TestAnalyzeIgnoresCommentsAndStrings は文字列・コメントの中の AS / カンマ /
// 括弧に釣られないことを見る。釣られると別名マップがずれ、マスクが黙って外れる。
func TestAnalyzeIgnoresCommentsAndStrings(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"行コメント", "SELECT email AS contact -- , phone AS contact, (\nFROM users"},
		{"ブロックコメント", "SELECT /* , phone AS contact ( */ email AS contact FROM users"},
		{"文字列リテラル", "SELECT email AS contact, 'x, y AS z (' AS s FROM users"},
		{"引用符の中の引用符", "SELECT email AS contact, 'it''s, AS z' AS s FROM users"},
		{"E 付きのエスケープ文字列", `SELECT email AS contact, E'\', AS z' AS s FROM users`},
		{"dollar-quote", "SELECT email AS contact, $$, AS z ($$ AS s FROM users"},
		{"タグ付き dollar-quote", "SELECT email AS contact, $tag$, AS z$tag$ AS s FROM users"},
		{"引用識別子の中のカンマ", `SELECT email AS "a, b" FROM users`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := analyze(t, tt.sql)
			// contact（または a, b）の由来が email だけであることを見る。
			for _, col := range []string{"contact", "a, b"} {
				o, u := a.Columns([]string{col})
				if u != nil {
					continue
				}
				if len(o[0].Sources) == 1 && o[0].Sources[0] == "email" {
					return
				}
			}
			t.Errorf("email からの伝播を拾えていません: %+v", a.alias)
		})
	}
}

// TestAnalyzeRecursiveCTEStops は自己参照する別名で閉包が回らないことを見る。
func TestAnalyzeRecursiveCTEStops(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{
			name: "再帰 CTE",
			sql: `WITH RECURSIVE r AS (
			        SELECT id, email AS e FROM t
			        UNION ALL
			        SELECT r.id, r.e AS e FROM r
			      ) SELECT e FROM r`,
		},
		{
			name: "別名同士が互いを参照する",
			sql:  "SELECT a AS b, b AS a, email AS a FROM t",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 止まらなければテストが返らない。由来に email が入ることも見る。
			got := sourcesOf(t, analyze(t, tt.sql), []string{"e", "a"})
			if !slices.Contains(got["e"], "email") && !slices.Contains(got["a"], "email") {
				t.Errorf("email からの伝播を拾えていません: %v", got)
			}
		})
	}
}

// TestAnalyzeIgnoresSubqueriesOutsideSelectList は、結果列にならない
// サブクエリの中の別名の無い式で判定不能にならないことを見る。
//
// WHERE / HAVING / ON の中のサブクエリは結果列を作らないため、出力名が
// 分からなくてもマスクが外れる経路にならない。ここで判定不能にすると、
// 既定の alias_guard: strict がごく普通の SQL を拒否する。
func TestAnalyzeIgnoresSubqueriesOutsideSelectList(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"WHERE の IN サブクエリ", "SELECT id FROM t WHERE x IN (SELECT lower(email) FROM u)"},
		{"WHERE の EXISTS サブクエリ", "SELECT id FROM t WHERE EXISTS (SELECT upper(email) FROM u)"},
		{"HAVING のスカラーサブクエリ",
			"SELECT id FROM t GROUP BY id HAVING max(z) > (SELECT avg(v) + 1 FROM u)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := analyze(t, tt.sql)
			if _, u := a.Columns([]string{"id"}); u != nil {
				t.Errorf("判定不能になりました: %v", u)
			}
		})
	}
}

// TestAnalyzeExemptionsFollowTheClosure は、許可関数の引数がさらに別名だった
// 場合に、止まった元の列まで辿れることを見る。
//
// 辿らないと「count が止めたのは contact」までしか分からず、contact に
// ルールが無ければ弱化が起きたことを通知に出せない。
func TestAnalyzeExemptionsFollowTheClosure(t *testing.T) {
	a := analyze(t, "WITH u AS (SELECT email AS contact FROM users) "+
		"SELECT count(contact) AS n FROM u", "count")

	want := []Exemption{
		{Function: "count", Column: "contact"},
		{Function: "count", Column: "email"},
	}
	if got := columns(t, a, []string{"n"})[0].Exemptions; !reflect.DeepEqual(got, want) {
		t.Errorf("伝播を止めた列 = %+v, want %+v", got, want)
	}
}

func TestAnalyzeUndetermined(t *testing.T) {
	tests := []struct {
		name   string
		sql    string
		cols   []string
		want   Reason
		detail string
	}{
		{
			name: "トークナイズに失敗する: 閉じていない文字列",
			sql:  "SELECT 'x FROM t",
			cols: []string{"c"},
			want: ReasonUnreadable,
		},
		{
			name: "トークナイズに失敗する: 閉じていないブロックコメント",
			sql:  "SELECT email /* x FROM t",
			cols: []string{"c"},
			want: ReasonUnreadable,
		},
		{
			name: "トークナイズに失敗する: 閉じていない引用識別子",
			sql:  `SELECT "email FROM t`,
			cols: []string{"c"},
			want: ReasonUnreadable,
		},
		{
			name: "トークナイズに失敗する: 閉じていない括弧",
			sql:  "SELECT count(email FROM t",
			cols: []string{"c"},
			want: ReasonUnreadable,
		},
		{
			name:   "トークナイズに失敗する: 方言で終端が変わる文字列",
			sql:    `SELECT 'it\'s' AS s FROM t`,
			cols:   []string{"s"},
			want:   ReasonUnreadable,
			detail: `\'`,
		},
		{
			name: "複数ステートメント",
			sql:  "SELECT email AS c FROM t; SELECT 1",
			cols: []string{"c"},
			want: ReasonUnreadable,
		},
		{
			name: "SELECT が1つも無い",
			sql:  "SHOW TABLES",
			cols: []string{"name"},
			want: ReasonNoSelect,
		},
		{
			name: "非 SQL のクエリランナー",
			sql:  `{"collection": "users", "fields": {"email": 1}}`,
			cols: []string{"email"},
			want: ReasonNoSelect,
		},
		{
			name:   "出力名不明の項目があり、位置対応も使えない: 列数が合わない",
			sql:    "SELECT upper(email) FROM t",
			cols:   []string{"a", "b"},
			want:   ReasonUnknownOutput,
			detail: "項目数",
		},
		{
			name:   "出力名不明の項目があり、位置対応も使えない: * との混在",
			sql:    "SELECT *, upper(email) FROM t",
			cols:   []string{"id", "email", "_col2"},
			want:   ReasonUnknownOutput,
			detail: "*",
		},
		{
			// 列別名リストは出力列を丸ごと改名するが、改名後の名前は内側の
			// SELECT のどこにも現れない。読めたことにすると、内側の列に
			// 掛けたルールが黙って届かなくなる。
			name:   "CTE の列別名リスト",
			sql:    "WITH q(c) AS (SELECT email FROM t) SELECT c FROM q",
			cols:   []string{"c"},
			want:   ReasonUnknownOutput,
			detail: "列別名リスト",
		},
		{
			name:   "派生テーブルの列別名リスト",
			sql:    "SELECT c FROM (SELECT email FROM t) x(c)",
			cols:   []string{"c"},
			want:   ReasonUnknownOutput,
			detail: "列別名リスト",
		},
		{
			name:   "UNNEST の列別名リスト",
			sql:    "SELECT v FROM t CROSS JOIN UNNEST(arr) AS u(v)",
			cols:   []string{"v"},
			want:   ReasonUnknownOutput,
			detail: "列別名リスト",
		},
		{
			name:   "内側の SELECT に別名の無い式がある",
			sql:    "SELECT x FROM (SELECT upper(email) AS x, lower(phone) FROM t) s",
			cols:   []string{"x"},
			want:   ReasonUnknownOutput,
			detail: "内側の SELECT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Analyze(tt.sql, nil)
			u := a.Undetermined()
			if u == nil {
				_, u = a.Columns(tt.cols)
			}
			if u == nil {
				t.Fatalf("判定不能になりませんでした")
			}
			if u.Reason != tt.want {
				t.Errorf("Reason = %v (%s), want %v", u.Reason, u.Error(), tt.want)
			}
			if tt.detail != "" && !strings.Contains(u.Error(), tt.detail) {
				t.Errorf("文言 %q に %q が含まれていません", u.Error(), tt.detail)
			}
			if !strings.Contains(u.Error(), u.Reason.String()) {
				t.Errorf("文言 %q に理由が含まれていません", u.Error())
			}
		})
	}
}

// TestColumnsFillsAnalyzedRangeWhenUndetermined は判定不能でも、名前で引ける
// 範囲の由来は返ることを見る。alias_guard: off で実行を続けるときに、
// 解析できた範囲の伝播だけは効かせるため。
func TestColumnsFillsAnalyzedRangeWhenUndetermined(t *testing.T) {
	a := Analyze("SELECT email AS contact, upper(phone) FROM t", nil)
	if u := a.Undetermined(); u != nil {
		t.Fatalf("この時点では判定できるはず: %v", u)
	}

	origins, u := a.Columns([]string{"contact"}) // 列数が合わないため位置対応は使えない。
	if u == nil {
		t.Fatal("判定不能になりませんでした")
	}
	if want := []string{"email"}; !reflect.DeepEqual(origins[0].Sources, want) {
		t.Errorf("contact の由来 = %v, want %v", origins[0].Sources, want)
	}
}

// TestColumnsAlwaysMatchesLength は戻り値の長さが必ず列数と同じであることを見る。
// 呼び出し側は判定不能でも添字で引く。
func TestColumnsAlwaysMatchesLength(t *testing.T) {
	for _, sql := range []string{
		"SELECT email AS c FROM t",
		"SELECT 'x FROM t",
		"SHOW TABLES",
		"SELECT upper(email) FROM t",
	} {
		cols := []string{"a", "b", "c"}
		origins, _ := Analyze(sql, nil).Columns(cols)
		if len(origins) != len(cols) {
			t.Errorf("%q: len(origins) = %d, want %d", sql, len(origins), len(cols))
		}
	}
}

// TestAnalyzeDoesNotSeeColumnAliasList は、列別名リストと形の似た並びを
// 誤検出しないことを見る。誤検出すると alias_guard: strict が普通の SQL を拒否する。
func TestAnalyzeDoesNotSeeColumnAliasList(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"識別子だけを取る関数", "SELECT coalesce(first_name, last_name) AS n FROM t"},
		{"窓関数の OVER 句", "SELECT count(x) OVER (PARTITION BY a) AS n FROM t"},
		{"FILTER 句", "SELECT count(x) FILTER (WHERE flag) AS n FROM t"},
		{"サブクエリの後ろの関数呼び出し",
			"SELECT n FROM (SELECT 1 AS n) t WHERE upper(a) = upper(b)"},
		{"LATERAL の関数呼び出し",
			"SELECT v FROM (SELECT 1 AS v) t, LATERAL flatten(arr)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if u := Analyze(tt.sql, nil).Undetermined(); u != nil {
				t.Errorf("判定不能になりました: %v", u)
			}
		})
	}
}

func TestAnalyzeExemptFunctions(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		exempt     []string
		col        string
		want       []string
		wantExempt []Exemption
	}{
		{
			name:       "許可関数の内側の識別子は伝播しない",
			sql:        "SELECT count(email) AS n FROM t",
			exempt:     []string{"count"},
			col:        "n",
			wantExempt: []Exemption{{Function: "count", Column: "email"}},
		},
		{
			name:       "DISTINCT 付きでも同じ",
			sql:        "SELECT count(DISTINCT email) AS n FROM t",
			exempt:     []string{"count"},
			col:        "n",
			wantExempt: []Exemption{{Function: "count", Column: "email"}},
		},
		{
			name:       "入れ子は祖先で見る",
			sql:        "SELECT count(upper(email)) AS n FROM t",
			exempt:     []string{"count"},
			col:        "n",
			wantExempt: []Exemption{{Function: "count", Column: "email"}},
		},
		{
			name:       "大文字小文字は無視する",
			sql:        "SELECT COUNT(email) AS n FROM t",
			exempt:     []string{"count"},
			col:        "n",
			wantExempt: []Exemption{{Function: "count", Column: "email"}},
		},
		{
			name:   "許可していない関数は伝播する",
			sql:    "SELECT min(email) AS m FROM t",
			exempt: []string{"count"},
			col:    "m",
			want:   []string{"email"},
		},
		{
			name:   "許可関数の外にも現れていれば伝播する",
			sql:    "SELECT count(email) OVER (PARTITION BY email) AS n FROM t",
			exempt: []string{"count"},
			col:    "n",
			want:   []string{"email"},
		},
		{
			name:   "許可関数を通していない単純な別名は伝播する",
			sql:    "SELECT email AS n FROM t",
			exempt: []string{"count"},
			col:    "n",
			want:   []string{"email"},
		},
		{
			name:   "スキーマ修飾付きの呼び出しは許可しない",
			sql:    "SELECT pg_catalog.count(email) AS n FROM t",
			exempt: []string{"count"},
			col:    "n",
			want:   []string{"email"},
		},
		{
			name:   "許可リストが空なら伝播は止まらない",
			sql:    "SELECT count(email) AS n FROM t",
			exempt: nil,
			col:    "n",
			want:   []string{"email"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := columns(t, analyze(t, tt.sql, tt.exempt...), []string{tt.col})[0]
			if !reflect.DeepEqual(o.Sources, tt.want) {
				t.Errorf("由来 = %v, want %v", o.Sources, tt.want)
			}
			if !reflect.DeepEqual(o.Exemptions, tt.wantExempt) {
				t.Errorf("伝播を止めた列 = %v, want %v", o.Exemptions, tt.wantExempt)
			}
		})
	}
}

// TestExemptFunctionNameIsExactMatch は許可リストが完全一致でしか効かないことを見る。
// グロブや部分一致で効くと、count* が count_raw_emails のようなユーザ定義関数まで通す。
func TestExemptFunctionNameIsExactMatch(t *testing.T) {
	for _, fn := range []string{"count_raw_emails", "xcount", "counts"} {
		t.Run(fn, func(t *testing.T) {
			sql := "SELECT " + fn + "(email) AS n FROM t"
			o := columns(t, analyze(t, sql, "count"), []string{"n"})[0]
			if want := []string{"email"}; !reflect.DeepEqual(o.Sources, want) {
				t.Errorf("由来 = %v, want %v。許可リストは完全一致でしか効いてはならない", o.Sources, want)
			}
		})
	}
}
