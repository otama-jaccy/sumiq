package mask

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/config"
)

// このファイルは「マッチするルールがあるのにマスクされない」ケースが
// 無いことを見る。マスクは安全装置であり、外れたことは出力を見ても分からない。

const secretValue = "secret@example.com"

// leakColumns はマスクが外れやすい列名。
//
// 大文字小文字、区切りに見える記号、非 ASCII、空白を混ぜてある。
// path.Match / filepath.Match をそのまま使うと / や \ を含む列名で
// * が届かなくなり、ここが落ちる。
var leakColumns = []string{
	"email",
	"Email",
	"EMAIL",
	"user_email",
	"payload/user/email",
	`payload\user\email`,
	"user.email",
	"user email",
	"顧客メールアドレス",
	"e",
}

// TestAllowlistHoleIsWholeMatch は method: none で開ける穴が、
// 書いた列名の分だけであることを見る。
//
// regex: は部分一致なので、regex:user_id と書くと user_identity や
// hashed_user_id まで素通しになる。allowlist に穴を開ける唯一の手段に
// 部分一致を許さない。
func TestAllowlistHoleIsWholeMatch(t *testing.T) {
	allowlist := config.Masking{
		DefaultAction: config.ActionRedact,
		Rules:         []config.MaskRule{rule(config.MaskNone, "user_id")},
	}
	e, err := New(testConfig(allowlist), testDataSource)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cols := []string{"user_id", "user_id_email", "user_identity", "hashed_user_id_secret"}
	row := make([]any, len(cols))
	for i := range row {
		row[i] = secretValue
	}
	got, _ := apply(t, e, result(cols, row))

	if got.Rows[0][0] != secretValue {
		t.Errorf("穴を開けた列 user_id = %#v, want %q", got.Rows[0][0], secretValue)
	}
	for i, c := range cols[1:] {
		if got.Rows[0][i+1] != redacted {
			t.Errorf("列 %q = %#v, want %q", c, got.Rows[0][i+1], redacted)
		}
	}
}

// maskingMethods は値を隠す method（none 以外）。
var maskingMethods = []config.MaskMethod{
	config.MaskPartial,
	config.MaskHash,
	config.MaskNull,
	config.MaskRedact,
	config.MaskDrop,
}

// matchingPatterns は column に必ずマッチするはずのパターンを返す。
func matchingPatterns(column string) []string {
	return []string{
		"*",
		column,
		strings.ToUpper(column),
		strings.ToLower(column),
		"*" + column + "*",
		"regex:" + regexp.QuoteMeta(column),
		"regex:.*",
	}
}

// TestMatchingRuleAlwaysMasks はマッチするパターンとマスク方法の全ての
// 組み合わせで、値が出力に残らないことを見る。
func TestMatchingRuleAlwaysMasks(t *testing.T) {
	for _, column := range leakColumns {
		for _, pattern := range matchingPatterns(column) {
			for _, method := range maskingMethods {
				name := fmt.Sprintf("%s/%s/%s", column, pattern, method)
				t.Run(name, func(t *testing.T) {
					assertMasked(t, column, method, ruleFor(method, pattern))
				})
			}
		}
	}
}

// TestScopedRuleAlwaysMasks は data_sources を指定したルールでも同じことを見る。
func TestScopedRuleAlwaysMasks(t *testing.T) {
	for _, scope := range [][]string{
		{testDataSource},
		{testDataSource, "sandbox"},
		{"sandbox", testDataSource},
	} {
		for _, method := range maskingMethods {
			t.Run(fmt.Sprintf("%v/%s", scope, method), func(t *testing.T) {
				r := ruleFor(method, "*email*")
				r.DataSources = scope
				assertMasked(t, "user_email", method, r)
			})
		}
	}
}

// TestUnmatchedPatternDoesNotHideMatchedOne は複数パターンのうち1つでも
// マッチすればマスクされることを見る。
func TestUnmatchedPatternDoesNotHideMatchedOne(t *testing.T) {
	for _, patterns := range [][]string{
		{"nomatch", "email"},
		{"email", "nomatch"},
		{"nomatch", "regex:^email$", "other"},
	} {
		t.Run(strings.Join(patterns, ","), func(t *testing.T) {
			assertMasked(t, "email", config.MaskRedact, rule(config.MaskRedact, patterns...))
		})
	}
}

// TestDefaultRedactMasksEveryColumn は allowlist 運用で、ルールが1つも
// 無くても全ての列が伏せられることを見る。
func TestDefaultRedactMasksEveryColumn(t *testing.T) {
	e, err := New(testConfig(config.Masking{DefaultAction: config.ActionRedact}), testDataSource)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	row := make([]any, len(leakColumns))
	for i := range row {
		row[i] = secretValue
	}
	got, sum := apply(t, e, result(leakColumns, row))
	for i, c := range leakColumns {
		if got.Rows[0][i] != redacted {
			t.Errorf("列 %q = %#v, want %q", c, got.Rows[0][i], redacted)
		}
		if m := methodOf(t, sum, c); m != config.MaskRedact {
			t.Errorf("列 %q の method = %q, want redact", c, m)
		}
	}
}

// assertMasked は rule を適用したときに column の値が出力に残らないことを見る。
func assertMasked(t *testing.T, column string, method config.MaskMethod, r config.MaskRule) {
	t.Helper()

	e, err := New(testConfig(masking(r)), testDataSource)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, sum := apply(t, e, result([]string{column}, []any{secretValue}))

	if m := methodOf(t, sum, column); m != method {
		t.Fatalf("サマリの method = %q, want %q", m, method)
	}

	if method == config.MaskDrop {
		if len(got.Columns) != 0 {
			t.Fatalf("drop したのに列が残っています: %v", columnNames(got))
		}
		if len(got.Rows[0]) != 0 {
			t.Fatalf("drop したのに値が残っています: %#v", got.Rows[0])
		}
		return
	}

	v := got.Rows[0][0]
	if v == secretValue {
		t.Fatalf("値がマスクされていません: %#v", v)
	}
	// 部分的に残す方法でも、元の値を復元できるほど残ってはいけない。
	if s, ok := v.(string); ok && strings.Contains(s, "@example.com") && method != config.MaskPartial {
		t.Fatalf("%s なのに値の一部が残っています: %q", method, s)
	}
}

// aliasRenames は email を別の名前で返すクエリ。結果列名は email に
// マッチしないため、伝播が効いていなければマスクが外れる。
var aliasRenames = []struct {
	name   string
	sql    string
	column string
}{
	{"AS で改名する", "SELECT email AS contact FROM users", "contact"},
	{"AS を省略して改名する", "SELECT email contact FROM users", "contact"},
	{"修飾付きの列を改名する", "SELECT u.email AS user_contact FROM users u", "user_contact"},
	{"式に混ぜて改名する", "SELECT lower(email) AS contact FROM users", "contact"},
	{"CTE をまたいで改名する",
		"WITH u AS (SELECT email AS c1 FROM users) SELECT c1 AS contact FROM u", "contact"},
	{"サブクエリをまたいで改名する",
		"SELECT t.c1 AS contact FROM (SELECT email AS c1 FROM users) t", "contact"},
	{"UNION の片方の枝で改名する",
		"SELECT contact FROM other UNION ALL SELECT email AS contact FROM users", "contact"},
	{"引用識別子で改名する",
		`SELECT email AS "payload/user/email" FROM users`, "payload/user/email"},
	{"内側で改名して外側は * を書く",
		"SELECT * FROM (SELECT email AS contact FROM users) t", "contact"},
}

// TestAliasDoesNotDropMask は別名を付けただけではマスクが外れないことを見る。
//
// 伝播を無効化する1行（Apply が Origin を見ない、closure が空を返す、
// resolveWithOrigin が stronger を取らない）を入れると落ちる。通っている
// ことの確認だけでは、伝播が効いているかは分からない。
func TestAliasDoesNotDropMask(t *testing.T) {
	for _, r := range aliasRenames {
		for _, method := range maskingMethods {
			t.Run(fmt.Sprintf("%s/%s", r.name, method), func(t *testing.T) {
				assertPropagated(t, r.sql, r.column, method, nil)
			})
		}
	}
}

// TestExemptFunctionDoesNotOpenAHole は許可関数を設定しても、その関数を
// 通していない別名には伝播が効くことを見る。
//
// 止まるのは「許可関数の内側にだけ現れた列」だけ。関数名で切った許可が
// 列そのものへの許可に広がっていないことを確かめる。
func TestExemptFunctionDoesNotOpenAHole(t *testing.T) {
	exempt := []string{"count"}
	for _, method := range maskingMethods {
		t.Run(string(method), func(t *testing.T) {
			// 許可関数を通していない単純な別名。
			assertPropagated(t, "SELECT email AS n FROM users", "n", method, exempt)
			// 許可関数の外にも現れている場合。
			assertPropagated(t, "SELECT count(email) OVER (PARTITION BY email) AS n FROM users",
				"n", method, exempt)
			// 許可していない関数。
			assertPropagated(t, "SELECT min(email) AS n FROM users", "n", method, exempt)
			// スキーマ修飾付きの呼び出しは許可リストに当たらない。
			assertPropagated(t, "SELECT pg_catalog.count(email) AS n FROM users", "n", method, exempt)
		})
	}
}

// assertPropagated は email に掛けたルールが、改名された列にも効くことを見る。
func assertPropagated(t *testing.T, sql, column string, method config.MaskMethod, exempt []string) {
	t.Helper()

	e, err := New(testConfig(masking(ruleFor(method, "email"))), testDataSource)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, sum := applySQL(t, e, sql, exempt, result([]string{column}, []any{secretValue}))

	if m := methodOf(t, sum, column); m != method {
		t.Fatalf("%s: 列 %q の method = %q, want %q。別名にマスクが伝播していません", sql, column, m, method)
	}
	if via := viaOf(t, sum, column); via != "email" {
		t.Errorf("%s: 列 %q の Via = %q, want email", sql, column, via)
	}

	if method == config.MaskDrop {
		if len(got.Columns) != 0 {
			t.Fatalf("%s: drop が伝播したのに列が残っています: %v", sql, columnNames(got))
		}
		return
	}

	v := got.Rows[0][0]
	if v == secretValue {
		t.Fatalf("%s: 値がマスクされていません: %#v", sql, v)
	}
	if s, ok := v.(string); ok && strings.Contains(s, "@example.com") && method != config.MaskPartial {
		t.Fatalf("%s: %s なのに値の一部が残っています: %q", sql, method, s)
	}
}

// viaOf はサマリから列の伝播元を取り出す。
func viaOf(t *testing.T, s Summary, column string) string {
	t.Helper()
	for _, c := range s.Columns {
		if c.Name == column {
			return c.Via
		}
	}
	t.Fatalf("サマリに列 %q がありません: %+v", column, s.Columns)
	return ""
}
