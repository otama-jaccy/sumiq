package mask

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/config"
	"github.com/otama-jaccy/sumiq/internal/redash"
	"github.com/otama-jaccy/sumiq/internal/sqlalias"
)

const testDataSource = "analytics"

// rule はテスト用にルールを1件組み立てる。
func rule(method config.MaskMethod, patterns ...string) config.MaskRule {
	return config.MaskRule{Patterns: patterns, Method: method}
}

// partialRule は keep 系を指定した partial ルールを組み立てる。
func partialRule(keep string, prefix, suffix int, patterns ...string) config.MaskRule {
	return config.MaskRule{
		Patterns:   patterns,
		Method:     config.MaskPartial,
		Keep:       keep,
		KeepPrefix: prefix,
		KeepSuffix: suffix,
	}
}

// masking は default_action: none のマスク方針を組み立てる。
func masking(rules ...config.MaskRule) config.Masking {
	return config.Masking{DefaultAction: config.ActionNone, Rules: rules}
}

// testDataSources はテストで参照するデータソースの定義。
// data_sources に書いた名前は定義済みでなければ New に弾かれる。
func testDataSources() []config.DataSource {
	return []config.DataSource{
		{Name: testDataSource, ID: 1},
		{Name: "sandbox", ID: 2},
		{Name: "other", ID: 3},
		{Name: "prod", ID: 4},
	}
}

// testConfig はマスク方針とデータソース定義から設定を組み立てる。
func testConfig(m config.Masking) config.Config {
	return config.Config{Masking: m, DataSources: testDataSources()}
}

func newEngine(t *testing.T, m config.Masking, rules ...config.MaskRule) *Engine {
	t.Helper()
	m.Rules = append(m.Rules, rules...)
	e, err := New(testConfig(m), testDataSource)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// result は列名と行から結果セットを組み立てる。
func result(cols []string, rows ...[]any) *redash.Result {
	res := &redash.Result{}
	for _, c := range cols {
		res.Columns = append(res.Columns, redash.Column{Name: c, Type: "string"})
	}
	for _, r := range rows {
		res.Rows = append(res.Rows, redash.Row(r))
	}
	return res
}

// noAlias は列名をそのまま出力名にする SQL の解析結果を返す。
//
// 別名を使っていない（伝播が起きない）クエリを表す。列名は引用識別子にして
// 渡すため、記号や空白を含む列名でもそのままの名前で出力列になる。
func noAlias(t *testing.T, res *redash.Result) *sqlalias.Analysis {
	t.Helper()
	quoted := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		quoted = append(quoted, `"`+strings.ReplaceAll(c.Name, `"`, `""`)+`"`)
	}
	return mustAnalyze(t, "SELECT "+strings.Join(quoted, ", ")+" FROM t", nil)
}

// mustAnalyze は sql を解析する。判定不能なら失敗させる。
func mustAnalyze(t *testing.T, sql string, exempt []string) *sqlalias.Analysis {
	t.Helper()
	a := sqlalias.Analyze(sql, exempt)
	if u := a.Undetermined(); u != nil {
		t.Fatalf("%s: 判定不能になりました: %v", sql, u)
	}
	return a
}

// apply は別名を使っていないクエリとして Apply を通す。
func apply(t *testing.T, e *Engine, res *redash.Result) (*redash.Result, Summary) {
	t.Helper()
	got, sum, err := e.Apply(res, noAlias(t, res))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return got, sum
}

// applySQL は sql を実行した結果として Apply を通す。伝播を見るテストで使う。
func applySQL(t *testing.T, e *Engine, sql string, exempt []string, res *redash.Result) (*redash.Result, Summary) {
	t.Helper()
	got, sum, err := e.Apply(res, mustAnalyze(t, sql, exempt))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return got, sum
}

// columnNames は結果の列名を返す。
func columnNames(res *redash.Result) []string {
	names := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		names = append(names, c.Name)
	}
	return names
}

// columnMaskOf はサマリから列1つの記録を取り出す。
func columnMaskOf(t *testing.T, s Summary, column string) ColumnMask {
	t.Helper()
	for _, c := range s.Columns {
		if c.Name == column {
			return c
		}
	}
	t.Fatalf("サマリに列 %q がありません: %+v", column, s.Columns)
	return ColumnMask{}
}

// methodOf はサマリから列に適用された method を取り出す。
func methodOf(t *testing.T, s Summary, column string) config.MaskMethod {
	t.Helper()
	return columnMaskOf(t, s, column).Method
}

func TestApplyMasksEachMethod(t *testing.T) {
	tests := []struct {
		name string
		rule config.MaskRule
		want any
	}{
		{"redact", rule(config.MaskRedact, "email"), "****"},
		{"null", rule(config.MaskNull, "email"), nil},
		{"partial keep_prefix", partialRule("", 2, 0, "email"), "us****"},
		{"partial keep_suffix", partialRule("", 0, 3, "email"), "****com"},
		{"partial keep domain", partialRule(keepDomain, 0, 0, "email"), "****@example.com"},
		{"partial keep domain と prefix", partialRule(keepDomain, 1, 0, "email"), "u****@example.com"},
		{"none", rule(config.MaskNone, "email"), "user@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEngine(t, masking(tt.rule))
			got, sum := apply(t, e, result([]string{"email"}, []any{"user@example.com"}))
			if got.Rows[0][0] != tt.want {
				t.Errorf("値 = %#v, want %#v", got.Rows[0][0], tt.want)
			}
			if m := methodOf(t, sum, "email"); m != tt.rule.Method {
				t.Errorf("サマリの method = %q, want %q", m, tt.rule.Method)
			}
		})
	}
}

// TestApplyDropRemovesColumn は drop が列とヘッダの両方から消えることを見る。
//
// 値だけ空にして列名を残すと「取得したが空だった」と誤読される（ADR-0004 §3）。
func TestApplyDropRemovesColumn(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskDrop, "secret")))
	in := result([]string{"id", "secret", "name"},
		[]any{"1", "s1", "a"},
		[]any{"2", "s2", "b"})

	got, sum := apply(t, e, in)

	if want := []string{"id", "name"}; !reflect.DeepEqual(columnNames(got), want) {
		t.Errorf("列 = %v, want %v", columnNames(got), want)
	}
	// 列を落とした後も、残った列の値がずれないこと。
	want := []redash.Row{{"1", "a"}, {"2", "b"}}
	if !reflect.DeepEqual(got.Rows, want) {
		t.Errorf("行 = %#v, want %#v", got.Rows, want)
	}
	if want := []ColumnMask{{Name: "secret", Method: config.MaskDrop}}; !reflect.DeepEqual(sum.Dropped(), want) {
		t.Errorf("Dropped() = %+v, want %+v", sum.Dropped(), want)
	}
	// drop された列は出力を見ても存在が分からない。サマリには必ず残す。
	if m := methodOf(t, sum, "secret"); m != config.MaskDrop {
		t.Errorf("サマリの method = %q, want drop", m)
	}
}

// TestApplyDoesNotMutateInput は入力の結果セットを書き換えないことを見る。
func TestApplyDoesNotMutateInput(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "email"), rule(config.MaskDrop, "memo")))
	in := result([]string{"id", "email", "memo"}, []any{"1", "user@example.com", "note"})

	apply(t, e, in)

	if want := []string{"id", "email", "memo"}; !reflect.DeepEqual(columnNames(in), want) {
		t.Errorf("入力の列 = %v, want %v", columnNames(in), want)
	}
	if want := (redash.Row{"1", "user@example.com", "note"}); !reflect.DeepEqual(in.Rows[0], want) {
		t.Errorf("入力の行 = %#v, want %#v", in.Rows[0], want)
	}
}

// TestSummaryCoversAllColumns はサマリが入力の全列を入力順に持つことを見る。
func TestSummaryCoversAllColumns(t *testing.T) {
	e := newEngine(t, masking(
		rule(config.MaskRedact, "email"),
		rule(config.MaskDrop, "memo"),
	))

	_, sum := apply(t, e, result([]string{"id", "email", "memo"}, []any{"1", "a@example.com", "x"}))

	want := []ColumnMask{
		{Name: "id", Method: config.MaskNone},
		{Name: "email", Method: config.MaskRedact},
		{Name: "memo", Method: config.MaskDrop},
	}
	if !reflect.DeepEqual(sum.Columns, want) {
		t.Errorf("Columns = %+v, want %+v", sum.Columns, want)
	}
	if got := sum.Masked(); !reflect.DeepEqual(got, want[1:]) {
		t.Errorf("Masked() = %+v, want %+v", got, want[1:])
	}
}

// TestDefaultAction はどのルールにもマッチしない列の扱いを見る。
func TestDefaultAction(t *testing.T) {
	tests := []struct {
		name     string
		global   config.Action
		perDS    config.Action
		rules    []config.MaskRule
		want     any
		wantMask config.MaskMethod
	}{
		{
			name:     "none は素通し",
			global:   config.ActionNone,
			want:     "plain",
			wantMask: config.MaskNone,
		},
		{
			name:     "redact は全列を伏せる",
			global:   config.ActionRedact,
			want:     "****",
			wantMask: config.MaskRedact,
		},
		{
			name:     "データソース単位の指定は厳しくする方向に効く",
			global:   config.ActionNone,
			perDS:    config.ActionRedact,
			want:     "****",
			wantMask: config.MaskRedact,
		},
		{
			// config はレビュー済みの共有ファイルからの引き下げを通すが、
			// 適用側は厳しい方を採る（ADR-0003 §7）。
			name:     "データソース単位の指定は緩める方向には効かない",
			global:   config.ActionRedact,
			perDS:    config.ActionNone,
			want:     "****",
			wantMask: config.MaskRedact,
		},
		{
			// allowlist 運用で特定の列を通す唯一の手段。既定より弱くても
			// マッチしたルールが勝たなければ機能しない。
			name:     "マッチした method: none は既定の redact より優先される",
			global:   config.ActionRedact,
			rules:    []config.MaskRule{rule(config.MaskNone, "col")},
			want:     "plain",
			wantMask: config.MaskNone,
		},
		{
			// 既定と混ぜて強い方を採ると、ドメインを残す指定が効かなくなる。
			name:     "マッチした partial は既定の redact より優先される",
			global:   config.ActionRedact,
			rules:    []config.MaskRule{partialRule("", 2, 0, "col")},
			want:     "pl****",
			wantMask: config.MaskPartial,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Masking:     config.Masking{DefaultAction: tt.global, Rules: tt.rules},
				DataSources: []config.DataSource{{Name: testDataSource, ID: 1, DefaultAction: tt.perDS}},
			}
			e, err := New(cfg, testDataSource)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, sum := apply(t, e, result([]string{"col"}, []any{"plain"}))
			if got.Rows[0][0] != tt.want {
				t.Errorf("値 = %#v, want %#v", got.Rows[0][0], tt.want)
			}
			if m := methodOf(t, sum, "col"); m != tt.wantMask {
				t.Errorf("method = %q, want %q", m, tt.wantMask)
			}
		})
	}
}

// TestDataSourceScope は data_sources によるスコープ指定を見る。
func TestDataSourceScope(t *testing.T) {
	tests := []struct {
		name        string
		scope       []string
		dataSource  string
		wantMasked  bool
		wantSummary config.MaskMethod
	}{
		{"無指定は全データソースに適用", nil, "analytics", true, config.MaskRedact},
		{"無指定は別のデータソースにも適用", nil, "sandbox", true, config.MaskRedact},
		{"指定したデータソースには適用", []string{"analytics"}, "analytics", true, config.MaskRedact},
		{"指定外のデータソースには適用しない", []string{"analytics"}, "sandbox", false, config.MaskNone},
		{"複数指定のいずれかに一致すれば適用", []string{"other", "sandbox"}, "sandbox", true, config.MaskRedact},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := New(testConfig(masking(config.MaskRule{
				Patterns:    []string{"memo"},
				Method:      config.MaskRedact,
				DataSources: tt.scope,
			})), tt.dataSource)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, sum := apply(t, e, result([]string{"memo"}, []any{"plain"}))
			masked := got.Rows[0][0] != "plain"
			if masked != tt.wantMasked {
				t.Errorf("マスクされた = %v (値 %#v), want %v", masked, got.Rows[0][0], tt.wantMasked)
			}
			if m := methodOf(t, sum, "memo"); m != tt.wantSummary {
				t.Errorf("method = %q, want %q", m, tt.wantSummary)
			}
		})
	}
}

// TestOtherDataSourceRuleIsValidated は、いま引かないデータソース向けの
// ルールも検証されることを見る。
//
// スコープで先に絞ると、書き間違いがそのデータソースを引く日まで
// 見つからない。負けたレイヤの設定も検査対象にするのと同じ理由。
func TestOtherDataSourceRuleIsValidated(t *testing.T) {
	tests := []struct {
		name string
		rule config.MaskRule
	}{
		{
			name: "グロブで扱わない [",
			rule: config.MaskRule{Patterns: []string{"user[0-9]"}, Method: config.MaskRedact},
		},
		{
			name: "patterns が空",
			rule: config.MaskRule{Method: config.MaskRedact},
		},
		{
			name: "keep 系が無い partial",
			rule: config.MaskRule{Patterns: []string{"memo"}, Method: config.MaskPartial},
		},
		{
			name: "未知の method",
			rule: config.MaskRule{Patterns: []string{"memo"}, Method: config.MaskMethod("obfuscate")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.rule
			r.DataSources = []string{"prod"}
			_, err := New(testConfig(masking(r)), testDataSource)
			if err == nil {
				t.Fatal("別のデータソース向けのルールの誤りが素通りしました")
			}
		})
	}
}

// TestApplyMasksNullValues は NULL の値もマスクされることを見る。
//
// 値を見て分岐すると、その列で NULL だった行だけ出力が変わり、
// 値が無いという事実が漏れる。
func TestApplyMasksNullValues(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "a"), rule(config.MaskHash, "b")))
	got, _ := apply(t, e, result([]string{"a", "b"}, []any{nil, nil}))
	if got.Rows[0][0] != "****" {
		t.Errorf("redact した NULL = %#v, want ****", got.Rows[0][0])
	}
	if s, ok := got.Rows[0][1].(string); !ok || len(s) != hashLength {
		t.Errorf("hash した NULL = %#v, want %d 文字のハッシュ", got.Rows[0][1], hashLength)
	}
}

// TestApplyShortRow は列数より短い行が来ても列がずれないことを見る。
func TestApplyShortRow(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "b")))
	in := &redash.Result{
		Columns: []redash.Column{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Rows:    []redash.Row{{"1"}},
	}
	got, _ := apply(t, e, in)
	want := redash.Row{"1", "****", nil}
	if !reflect.DeepEqual(got.Rows[0], want) {
		t.Errorf("行 = %#v, want %#v", got.Rows[0], want)
	}
}

// TestApplyNilResult は結果が無いことを空の結果に倒さないことを見る。
func TestApplyNilResult(t *testing.T) {
	e := newEngine(t, masking())
	if _, _, err := e.Apply(nil, sqlalias.Analyze("SELECT 1", nil)); err == nil {
		t.Fatal("エラーになりませんでした")
	}
}

// TestApplyNilAnalysis は解析結果を渡し忘れたことを「由来なし」に倒さないことを見る。
//
// nil を空の解析として扱うと、別名で改名された列の伝播が丸ごと消えたまま実行される。
func TestApplyNilAnalysis(t *testing.T) {
	e := newEngine(t, masking(rule(config.MaskRedact, "email")))
	if _, _, err := e.Apply(result([]string{"email"}, []any{"a@example.com"}), nil); err == nil {
		t.Fatal("エラーになりませんでした")
	}
}

func TestNewRejects(t *testing.T) {
	tests := []struct {
		name       string
		masking    config.Masking
		perDS      config.Action
		dataSource string
		want       string
	}{
		{
			name:       "定義されていないデータソース",
			masking:    masking(rule(config.MaskRedact, "a")),
			dataSource: "analitycs",
			want:       "定義されていません",
		},
		{
			name:    "default_action が未解決",
			masking: config.Masking{Rules: []config.MaskRule{rule(config.MaskRedact, "a")}},
			want:    "default_action",
		},
		{
			name:    "patterns が空",
			masking: masking(rule(config.MaskRedact)),
			want:    "patterns",
		},
		{
			name:    "空のパターン",
			masking: masking(rule(config.MaskRedact, "")),
			want:    "空のパターン",
		},
		{
			name:    "未知の method",
			masking: masking(rule(config.MaskMethod("obfuscate"), "a")),
			want:    "obfuscate",
		},
		{
			name:    "未知の keep",
			masking: masking(partialRule("local", 0, 0, "a")),
			want:    "keep",
		},
		{
			name:    "keep 系が無い partial",
			masking: masking(partialRule("", 0, 0, "a")),
			want:    "partial",
		},
		{
			name:    "負の keep_prefix",
			masking: masking(partialRule("", -1, 0, "a")),
			want:    "負の値",
		},
		{
			name:    "グロブで扱わない [",
			masking: masking(rule(config.MaskRedact, "user[0-9]")),
			want:    "regex:",
		},
		{
			name:    "読めない正規表現",
			masking: masking(rule(config.MaskRedact, "regex:(")),
			want:    "正規表現",
		},
		{
			name: "空のデータソース名",
			masking: masking(config.MaskRule{
				Patterns:    []string{"a"},
				Method:      config.MaskRedact,
				DataSources: []string{""},
			}),
			want: "data_sources[0]",
		},
		{
			// 綴り間違いはどのデータソースにも一致せず、ルールを丸ごと無効にする。
			name: "定義されていないデータソースを指すルール",
			masking: masking(config.MaskRule{
				Patterns:    []string{"a"},
				Method:      config.MaskRedact,
				DataSources: []string{"analitycs"},
			}),
			want: "data_sources[0]",
		},
		{
			// regex: は部分一致なので、意図した列より広く素通しになる。
			name:    "method: none に書いた regex:",
			masking: masking(rule(config.MaskNone, "regex:^user_id$")),
			want:    "method: none",
		},
		{
			// method: partial の書き忘れ。そのまま通すと列が丸ごと素通りする。
			name:    "partial 以外に書いた keep_prefix",
			masking: masking(config.MaskRule{Patterns: []string{"a"}, Method: config.MaskNone, KeepPrefix: 3}),
			want:    "partial",
		},
		{
			name:    "partial 以外に書いた keep",
			masking: masking(config.MaskRule{Patterns: []string{"a"}, Method: config.MaskHash, Keep: keepDomain}),
			want:    "partial",
		},
		{
			name:    "扱えないデータソース単位の default_action",
			masking: masking(rule(config.MaskRedact, "a")),
			perDS:   config.Action("mask"),
			want:    "default_action",
		},
		{
			// 厳しい方を先に選ぶと、緩い方が読めない値でも通ってしまう。
			name: "データソース単位が redact でもグローバルの未解決は落とす",
			masking: config.Masking{
				Rules: []config.MaskRule{rule(config.MaskRedact, "a")},
			},
			perDS: config.ActionRedact,
			want:  "masking.default_action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(tt.masking)
			cfg.DataSources[0].DefaultAction = tt.perDS
			name := tt.dataSource
			if name == "" {
				name = testDataSource
			}
			_, err := New(cfg, name)
			if err == nil {
				t.Fatal("エラーになりませんでした")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("エラー文 = %v, want %q を含む", err, tt.want)
			}
		})
	}
}

// New が返すルールのエラーは、全レイヤの和集合での添字ではなく、
// 利用者が開くファイルの何番目かを示すこと（#18）。
//
// 共有ファイルに正しいルールを1件、ローカルファイルに不正なパターンの
// ルールを1件置く。和集合での添字は1（共有の後ろ）になるが、ローカル
// ファイル自身の中では0番目のルールなので、エラーは「ローカル設定
// (パス) の masking.rules[0]」を指すべきで、masking.rules[1] ではない。
func TestNew_エラーはルールの由来を示す(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	shared := "version: 1\n" +
		"data_sources: [{name: analytics, id: 1}]\n" +
		"masking:\n  rules:\n    - patterns: [\"ok\"]\n      method: redact\n"
	if err := os.WriteFile(filepath.Join(dir, config.SharedFileName), []byte(shared), 0o600); err != nil {
		t.Fatal(err)
	}
	local := "version: 1\nmasking:\n  rules:\n    - patterns: [\"user[0-9]\"]\n      method: redact\n"
	if err := os.WriteFile(filepath.Join(dir, config.LocalFileName), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := config.Resolve(config.Options{
		Dir:            dir,
		Environ:        []string{},
		UserConfigPath: filepath.Join(dir, "does-not-exist", "config.yaml"),
	})
	if err != nil {
		t.Fatalf("config.Resolve: %v", err)
	}
	if len(res.Config.Masking.Rules) != 2 {
		t.Fatalf("rules = %d件, want 2件: %+v", len(res.Config.Masking.Rules), res.Config.Masking.Rules)
	}

	_, err = New(res.Config, "analytics")
	if err == nil {
		t.Fatal("New() error = nil, want error")
	}
	if !strings.Contains(err.Error(), config.LocalFileName) {
		t.Errorf("error = %q, want に由来ファイル %q を含む", err.Error(), config.LocalFileName)
	}
	if !strings.Contains(err.Error(), "masking.rules[0]") {
		t.Errorf("error = %q, want にファイル内での位置 masking.rules[0] を含む", err.Error())
	}
	if strings.Contains(err.Error(), "masking.rules[1]") {
		t.Errorf("error = %q, 全レイヤの和集合での添字 masking.rules[1] を含むべきではない", err.Error())
	}
}
