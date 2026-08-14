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
	got, sum, err := e.Apply(result(leakColumns, row))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
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

	got, sum, err := e.Apply(result([]string{column}, []any{secretValue}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

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
