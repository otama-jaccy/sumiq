package mask

import (
	"reflect"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/redash"
	"github.com/otama-jaccy/sumiq/internal/sqlalias"
)

// guardedEngine は analytics の alias_guard を指定したエンジンを作る。
func guardedEngine(t *testing.T, guard config.AliasGuard, m config.Masking) *Engine {
	t.Helper()
	cfg := testConfig(m)
	for i := range cfg.DataSources {
		if cfg.DataSources[i].Name == testDataSource {
			cfg.DataSources[i].AliasGuard = guard
		}
	}
	e, err := New(cfg, testDataSource)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// TestPropagationStrongerWins は別名に伝播したマスクが、既存の
// 「複数マッチは強い方が勝つ」規則の通りに決まることを見る。
func TestPropagationStrongerWins(t *testing.T) {
	tests := []struct {
		name    string
		rules   []config.MaskRule
		sql     string
		want    any
		wantVia string
	}{
		{
			name:    "由来にマッチしたルールが勝つ",
			rules:   []config.MaskRule{rule(config.MaskRedact, "email")},
			sql:     "SELECT email AS contact FROM users",
			want:    redacted,
			wantVia: "email",
		},
		{
			name: "出力列名の直接マッチの方が強ければ由来は勝たない",
			rules: []config.MaskRule{
				rule(config.MaskHash, "email"),
				rule(config.MaskRedact, "contact"),
			},
			sql:  "SELECT email AS contact FROM users",
			want: redacted,
		},
		{
			name: "由来の方が強ければ由来が勝つ",
			rules: []config.MaskRule{
				rule(config.MaskRedact, "email"),
				rule(config.MaskHash, "contact"),
			},
			sql:     "SELECT email AS contact FROM users",
			want:    redacted,
			wantVia: "email",
		},
		{
			name: "別名側の method: none は伝播を打ち消せない",
			rules: []config.MaskRule{
				rule(config.MaskRedact, "email"),
				rule(config.MaskNone, "contact"),
			},
			sql:     "SELECT email AS contact FROM users",
			want:    redacted,
			wantVia: "email",
		},
		{
			name: "partial 同士はどちらも残す部分だけが残る",
			rules: []config.MaskRule{
				partialRule("", 1, 0, "email"),
				partialRule("", 3, 0, "contact"),
			},
			sql:     "SELECT email AS contact FROM users",
			want:    "u" + redacted,
			wantVia: "email",
		},
		{
			name:  "多段の別名でも閉包で辿る",
			rules: []config.MaskRule{rule(config.MaskRedact, "email")},
			sql: "WITH u AS (SELECT email AS c1 FROM users) " +
				"SELECT c1 AS contact FROM u",
			want:    redacted,
			wantVia: "email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEngine(t, masking(tt.rules...))
			got, sum := applySQL(t, e, tt.sql, nil, result([]string{"contact"}, []any{"user@example.com"}))
			if got.Rows[0][0] != tt.want {
				t.Errorf("値 = %#v, want %#v", got.Rows[0][0], tt.want)
			}
			if via := sum.Columns[0].Via; via != tt.wantVia {
				t.Errorf("Via = %q, want %q", via, tt.wantVia)
			}
		})
	}
}

// TestPropagationDoesNotCloseTheAllowlistHole は、allowlist 運用
// （default_action: redact）で開けた method: none の穴が、由来のせいで
// 塞がらないことを見る。
//
// 伝播は「由来にマッチしたルール」だけを見る。由来に default_action を
// 持ち込むと、ルールに挙がっていない由来（型名やテーブル名のような
// 過剰近似で拾った名前も含む）がすべて redact を押し付け、式から作った列に
// 穴を開けられなくなる。マッチしたルールが既定より優先されるという
// 規則（ADR-0003 §7）も崩れる。
func TestPropagationDoesNotCloseTheAllowlistHole(t *testing.T) {
	allowlist := config.Masking{
		DefaultAction: config.ActionRedact,
		Rules: []config.MaskRule{
			rule(config.MaskRedact, "email"),
			rule(config.MaskNone, "label"),
		},
	}
	e := newEngine(t, allowlist)

	t.Run("ルールの無い由来は穴を塞がない", func(t *testing.T) {
		got, sum := applySQL(t, e, "SELECT cast(nickname AS text) AS label FROM u",
			nil, result([]string{"label"}, []any{"taro"}))
		if got.Rows[0][0] != "taro" {
			t.Errorf("値 = %#v, want %q。method: none の穴が塞がっています", got.Rows[0][0], "taro")
		}
		if via := sum.Columns[0].Via; via != "" {
			t.Errorf("Via = %q, want 空", via)
		}
	})

	t.Run("ルールのある由来は穴より強い", func(t *testing.T) {
		got, _ := applySQL(t, e, "SELECT email AS label FROM u",
			nil, result([]string{"label"}, []any{"a@example.com"}))
		if got.Rows[0][0] != redacted {
			t.Errorf("値 = %#v, want %q。伝播が method: none に負けています", got.Rows[0][0], redacted)
		}
	})
}

// TestExemptedFollowsTheClosure は、許可関数の引数がさらに別名だったときも
// 弱化を通知できることを見る。
func TestExemptedFollowsTheClosure(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "email")))
	_, sum := applySQL(t, e,
		"WITH u AS (SELECT email AS contact FROM users) SELECT count(contact) AS n FROM u",
		[]string{"count"}, result([]string{"n"}, []any{"3"}))

	want := []sqlalias.Exemption{{Function: "count", Column: "email"}}
	if got := sum.Columns[0].Exempted; !reflect.DeepEqual(got, want) {
		t.Errorf("Exempted = %+v, want %+v。止まったのは email のマスク", got, want)
	}
}

// TestPropagatedDropRemovesColumn は drop が伝播したとき列ごと消えることを見る。
func TestPropagatedDropRemovesColumn(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskDrop, "email")))
	got, sum := applySQL(t, e, "SELECT id, email AS contact FROM users",
		nil, result([]string{"id", "contact"}, []any{"1", "user@example.com"}))

	if want := []string{"id"}; !reflect.DeepEqual(columnNames(got), want) {
		t.Errorf("列 = %v, want %v", columnNames(got), want)
	}
	want := []ColumnMask{{Name: "contact", Method: config.MaskDrop, Via: "email"}}
	if !reflect.DeepEqual(sum.Dropped(), want) {
		t.Errorf("Dropped() = %+v, want %+v", sum.Dropped(), want)
	}
}

// TestNoPropagationWhenNoAlias は由来が無いときに挙動が変わらないことを見る。
func TestNoPropagationWhenNoAlias(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "email")))
	got, sum := applySQL(t, e, "SELECT id, email FROM users",
		nil, result([]string{"id", "email"}, []any{"1", "user@example.com"}))

	if got.Rows[0][0] != "1" {
		t.Errorf("id = %#v, want %q", got.Rows[0][0], "1")
	}
	if got.Rows[0][1] != redacted {
		t.Errorf("email = %#v, want %q", got.Rows[0][1], redacted)
	}
	for _, c := range sum.Columns {
		if c.Via != "" {
			t.Errorf("列 %q に伝播元 %q が付いています", c.Name, c.Via)
		}
	}
}

// TestExemptFunctionStopsPropagation は許可関数が伝播だけを止めることを見る。
func TestExemptFunctionStopsPropagation(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "email")))
	_, sum := applySQL(t, e, "SELECT count(email) AS n FROM users",
		[]string{"count"}, result([]string{"n"}, []any{"3"}))

	if got := sum.Columns[0].Method; got != config.MaskNone {
		t.Errorf("method = %q, want none", got)
	}
	want := []sqlalias.Exemption{{Function: "count", Column: "email"}}
	if !reflect.DeepEqual(sum.Columns[0].Exempted, want) {
		t.Errorf("Exempted = %+v, want %+v", sum.Columns[0].Exempted, want)
	}
}

// TestExemptFunctionKeepsDefaultAction は許可関数が止めるのは伝播だけで、
// 出力列名への通常の照合と default_action はそのまま効くことを見る。
func TestExemptFunctionKeepsDefaultAction(t *testing.T) {
	tests := []struct {
		name    string
		masking config.Masking
		want    any
	}{
		{
			name:    "denylist 運用では素通りする",
			masking: masking(rule(config.MaskRedact, "email")),
			want:    "3",
		},
		{
			name: "allowlist 運用では default_action がそのまま効く",
			masking: config.Masking{
				DefaultAction: config.ActionRedact,
				Rules:         []config.MaskRule{rule(config.MaskRedact, "email")},
			},
			want: redacted,
		},
		{
			name: "allowlist 運用でも出力列名にマッチするルールは効く",
			masking: config.Masking{
				DefaultAction: config.ActionRedact,
				Rules: []config.MaskRule{
					rule(config.MaskRedact, "email"),
					rule(config.MaskNone, "n"),
				},
			},
			want: "3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEngine(t, tt.masking)
			got, _ := applySQL(t, e, "SELECT count(email) AS n FROM users",
				[]string{"count"}, result([]string{"n"}, []any{"3"}))
			if got.Rows[0][0] != tt.want {
				t.Errorf("値 = %#v, want %#v", got.Rows[0][0], tt.want)
			}
		})
	}
}

// TestExemptedIsReportedOnlyWhenWeakened は、止めるものが無かった許可関数を
// 通知しないことを見る。無害な集計まで並べると本当の弱化が埋もれる。
func TestExemptedIsReportedOnlyWhenWeakened(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "email")))
	_, sum := applySQL(t, e, "SELECT count(id) AS n FROM users",
		[]string{"count"}, result([]string{"n"}, []any{"3"}))

	if got := sum.Columns[0].Exempted; len(got) != 0 {
		t.Errorf("Exempted = %+v, want 空。id にマスクは掛かっていない", got)
	}
}

// TestApplyUndeterminedByGuard は判定不能だったときの扱いが alias_guard で
// 決まることを見る。
func TestApplyUndeterminedByGuard(t *testing.T) {
	// 別名の無い式があり、項目数（2）と結果列数（1）が合わないため
	// 位置でも対応付けできない。
	const sql = "SELECT email AS c, upper(phone) FROM users"
	res := result([]string{"c"}, []any{"user@example.com"})

	t.Run("strict はエラーにする", func(t *testing.T) {
		e := guardedEngine(t, config.AliasGuardStrict, masking(rule(config.MaskRedact, "email")))
		_, _, err := e.Apply(res, sqlalias.Analyze(sql, nil))
		if err == nil {
			t.Fatal("エラーになりませんでした")
		}
		if !strings.Contains(err.Error(), "alias_guard") {
			t.Errorf("エラー文言 %q に対処方法が含まれていません", err.Error())
		}
	})

	t.Run("off は解析できた範囲の伝播を効かせて続ける", func(t *testing.T) {
		e := guardedEngine(t, config.AliasGuardOff, masking(rule(config.MaskRedact, "email")))
		got, sum, err := e.Apply(res, sqlalias.Analyze(sql, nil))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		// 伝播が効いていないことは出力を見ても分からない。呼び出し側が
		// 毎回警告を出せるよう、辿れなかった理由をサマリに残す。
		if sum.AliasUndetermined == nil {
			t.Error("サマリに判定不能の理由が残っていません")
		}
		// c は別名が付いているので、対応付けができなくても由来を辿れる。
		if got.Rows[0][0] != redacted {
			t.Errorf("値 = %#v, want %q", got.Rows[0][0], redacted)
		}
		if via := sum.Columns[0].Via; via != "email" {
			t.Errorf("Via = %q, want email", via)
		}
	})
}

// TestApplyRowsStayAlignedAfterPropagatedDrop は伝播で列を落とした後も
// 残った列の値がずれないことを見る。
func TestApplyRowsStayAlignedAfterPropagatedDrop(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskDrop, "secret")))
	in := result([]string{"a", "s", "b"},
		[]any{"1", "x", "2"},
		[]any{"3", "y", "4"})
	got, _ := applySQL(t, e, "SELECT a, secret AS s, b FROM t", nil, in)

	want := []redash.Row{{"1", "2"}, {"3", "4"}}
	if !reflect.DeepEqual(got.Rows, want) {
		t.Errorf("行 = %#v, want %#v", got.Rows, want)
	}
}

// TestPrecheckAliasByGuard は、ネットワークに出る前の検査でも判定不能の
// 扱いが alias_guard で決まることを見る。
//
// 判定できるかどうかの決定と文言は internal/mask に閉じる。呼び出し側でも
// 判定すると、同じ状況でどちらの検査が先に効いたかで文言が変わる。
func TestPrecheckAliasByGuard(t *testing.T) {
	// SELECT が1つも無いため、結果列を見るまでもなく辿れない。
	analysis := sqlalias.Analyze("SHOW TABLES", nil)

	t.Run("strict はエラーにする", func(t *testing.T) {
		e := guardedEngine(t, config.AliasGuardStrict, masking())
		err := e.PrecheckAlias(analysis)
		if err == nil {
			t.Fatal("エラーになりませんでした")
		}
		for _, want := range []string{"SELECT", "alias_guard", string(config.AliasGuardOff)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("エラー文言 %q に %q が含まれていません", err.Error(), want)
			}
		}
	})

	t.Run("off は通す", func(t *testing.T) {
		e := guardedEngine(t, config.AliasGuardOff, masking())
		if err := e.PrecheckAlias(analysis); err != nil {
			t.Errorf("PrecheckAlias: %v", err)
		}
	})

	t.Run("解析結果を渡し忘れたら strict でなくてもエラーにする", func(t *testing.T) {
		e := guardedEngine(t, config.AliasGuardOff, masking())
		if err := e.PrecheckAlias(nil); err == nil {
			t.Error("エラーになりませんでした")
		}
	})
}
