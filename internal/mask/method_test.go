package mask

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/otama-jaccy/sumiq/internal/config"
)

// weakestFirst は method を弱い順に並べたもの。
//
//	drop > redact > null > hash > partial > none
//
// この並びが強度解決の唯一の根拠。ADR-0003 §7 と
// docs/adr/0009-mask-null-strength.md に対応する。
var weakestFirst = []config.MaskMethod{
	config.MaskNone,
	config.MaskPartial,
	config.MaskHash,
	config.MaskNull,
	config.MaskRedact,
	config.MaskDrop,
}

// TestStrengthOrder は宣言した並びどおりに強度が付いていることを見る。
func TestStrengthOrder(t *testing.T) {
	for i := 1; i < len(weakestFirst); i++ {
		weak, strong := weakestFirst[i-1], weakestFirst[i]
		if strength(strong) <= strength(weak) {
			t.Errorf("strength(%q) = %d は strength(%q) = %d より大きくありません",
				strong, strength(strong), weak, strength(weak))
		}
	}
	for _, m := range weakestFirst {
		if !knownMethod(m) {
			t.Errorf("knownMethod(%q) = false", m)
		}
	}
}

// TestStrengthCoversConfigMethods は config が受け付ける method を
// mask が全て知っていることを見る。
//
// config 側に method を足して strength / knownMethod を直し忘れると、
// 新しい method が最も弱い扱いになって他のルールに負ける。
// config のエラー文が受け付ける値の一覧を持っているので、それと突き合わせる。
func TestStrengthCoversConfigMethods(t *testing.T) {
	_, err := config.Load(strings.NewReader("version: 1\n" +
		"masking:\n  rules:\n    - patterns: [\"a\"]\n      method: obfuscate\n"))
	if err == nil {
		t.Fatal("不正な method がエラーになりませんでした")
	}

	for _, m := range weakestFirst {
		if !strings.Contains(err.Error(), "\""+string(m)+"\"") {
			t.Errorf("config のエラー文に %q がありません: %v", m, err)
		}
	}
	// 一覧の要素数が増えていたら、mask 側に知らない method がある。
	if got, want := strings.Count(err.Error(), " / ")+1, len(weakestFirst); got != want {
		t.Errorf("config が受け付ける method の数 = %d, want %d。"+
			"config に method が増えたら strength と knownMethod を直すこと: %v", got, want, err)
	}
}

// TestStrongestWins は同じ列に2つのルールがマッチしたとき、
// 強い方が勝つことを全ての組み合わせで見る。
func TestStrongestWins(t *testing.T) {
	for i, a := range weakestFirst {
		for j, b := range weakestFirst {
			want := a
			if j > i {
				want = b
			}
			t.Run(string(a)+"+"+string(b), func(t *testing.T) {
				// 順序に依存しないことも見る。設定に書いた順で結果が
				// 変わると、レイヤの並びでマスクの強さが変わる。
				for _, rules := range [][]config.MaskRule{
					{ruleFor(a, "col"), ruleFor(b, "col")},
					{ruleFor(b, "col"), ruleFor(a, "col")},
				} {
					e := newEngine(t, masking(rules...))
					_, sum := apply(t, e, result([]string{"col"}, []any{"plain"}))
					if got := methodOf(t, sum, "col"); got != want {
						t.Errorf("%q と %q がマッチ → %q, want %q",
							rules[0].Method, rules[1].Method, got, want)
					}
				}
			})
		}
	}
}

// ruleFor は method に応じた最小のルールを組み立てる。
func ruleFor(m config.MaskMethod, patterns ...string) config.MaskRule {
	if m == config.MaskPartial {
		return partialRule("", 1, 0, patterns...)
	}
	return rule(m, patterns...)
}

// TestPartialTighten は同じ強さの partial が複数マッチしたとき、
// 両方が残す部分だけが残ることを見る。
//
// 片方を選ぶ実装にすると、もう片方が隠すつもりだった部分が出る。
func TestPartialTighten(t *testing.T) {
	tests := []struct {
		name  string
		rules []config.MaskRule
		want  string
	}{
		{
			name: "残す文字数は少ない方に合わせる",
			rules: []config.MaskRule{
				partialRule("", 4, 0, "col"),
				partialRule("", 1, 0, "col"),
			},
			want: "u****",
		},
		{
			name: "片方がドメインを残さなければ残さない",
			rules: []config.MaskRule{
				partialRule(keepDomain, 1, 0, "col"),
				partialRule("", 1, 0, "col"),
			},
			want: "u****",
		},
		{
			name: "ドメインだけを残す指定と先頭だけを残す指定は打ち消し合う",
			rules: []config.MaskRule{
				partialRule(keepDomain, 0, 0, "col"),
				partialRule("", 1, 0, "col"),
			},
			want: "****",
		},
		{
			name: "両方がドメインを残すなら残す",
			rules: []config.MaskRule{
				partialRule(keepDomain, 2, 0, "col"),
				partialRule(keepDomain, 1, 0, "col"),
			},
			want: "u****@example.com",
		},
		{
			name: "共通して残す部分が無ければ全て伏せる",
			rules: []config.MaskRule{
				partialRule("", 2, 0, "col"),
				partialRule("", 0, 2, "col"),
			},
			want: "****",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEngine(t, masking(tt.rules...))
			got, _ := apply(t, e, result([]string{"col"}, []any{"user@example.com"}))
			if got.Rows[0][0] != tt.want {
				t.Errorf("値 = %#v, want %#v", got.Rows[0][0], tt.want)
			}
		})
	}
}

// TestKeepEnds は残す指定が値の長さ以上でも素の値が出ないことを見る。
//
// この上限を外すと keep_prefix: 4 に 3 文字の値が来たときに全部出る。
func TestKeepEnds(t *testing.T) {
	tests := []struct {
		name           string
		value          string
		prefix, suffix int
		want           string
	}{
		{"先頭を残す", "abcdef", 2, 0, "ab****"},
		{"末尾を残す", "abcdef", 0, 2, "****ef"},
		{"両端を残す", "abcdef", 1, 1, "a****f"},
		{"残す指定が長さと同じなら全て伏せる", "abc", 3, 0, "****"},
		{"残す指定が長さを超えるなら全て伏せる", "abc", 4, 0, "****"},
		{"両端の合計が長さ以上なら全て伏せる", "abcd", 2, 2, "****"},
		{"空の値", "", 2, 0, "****"},
		{"何も残さない指定", "abcdef", 0, 0, "****"},
		{"マルチバイトはルーンで数える", "日本語です", 2, 0, "日本****"},
		// prefix + suffix が桁溢れして負になると、合計だけを見る検査は
		// すり抜け、rs[:prefix] で落ちる。
		{"桁溢れする指定", "abcdef", math.MaxInt, 1, "****"},
		{"桁溢れする指定（末尾）", "abcdef", 1, math.MaxInt, "****"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keepEnds(tt.value, tt.prefix, tt.suffix); got != tt.want {
				t.Errorf("keepEnds(%q, %d, %d) = %q, want %q",
					tt.value, tt.prefix, tt.suffix, got, tt.want)
			}
		})
	}
}

// TestApplyPartialDomain は @ が無い値でドメインを残そうとしないことを見る。
func TestApplyPartialDomain(t *testing.T) {
	tests := []struct {
		value string
		spec  partialSpec
		want  string
	}{
		{"user@example.com", partialSpec{domain: true}, "****@example.com"},
		{"a@b@example.com", partialSpec{domain: true}, "****@example.com"},
		{"notanemail", partialSpec{domain: true}, "****"},
		{"notanemail", partialSpec{domain: true, prefix: 3}, "not****"},
		{"@example.com", partialSpec{domain: true}, "****@example.com"},
		{"user@日本語.jp", partialSpec{domain: true}, "****@日本語.jp"},
		// ドットが無い並びは残さない。自由記述に混ざったメンションと
		// TLD 無しのアドレスを区別できないため、残さない側に倒す。
		{"user@localhost", partialSpec{domain: true}, "****"},
		{"連絡先: @taro_handle", partialSpec{domain: true}, "****"},
		{"担当 @yamada", partialSpec{domain: true}, "****"},
		// @ の後ろがホスト名の形をしていなければ残さない。自由記述の列に
		// *mail* のようなパターンが掛かると、本文が丸ごと出る。
		{"送付先は bob@corp.example.com、案件は #4471 です", partialSpec{domain: true}, "****"},
		{"contact bob@example.com now", partialSpec{domain: true}, "****"},
		{"user@", partialSpec{domain: true}, "****"},
		{"user@ example.com", partialSpec{domain: true}, "****"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := applyPartial(tt.value, tt.spec); got != tt.want {
				t.Errorf("applyPartial(%q, %+v) = %q, want %q", tt.value, tt.spec, got, tt.want)
			}
		})
	}
}

// TestHashIsConsistentWithinRun は同じ実行内で同じ値が同じハッシュになり、
// 実行をまたぐと変わることを見る（ADR-0003 §9）。
func TestHashIsConsistentWithinRun(t *testing.T) {
	in := result([]string{"user_id"},
		[]any{json.Number("9007199254740993")},
		[]any{json.Number("9007199254740993")},
		[]any{json.Number("9007199254740992")})

	e := newEngine(t, masking(rule(config.MaskHash, "user_id")))
	got, _ := apply(t, e, in)

	first, second, other := got.Rows[0][0], got.Rows[1][0], got.Rows[2][0]
	if first != second {
		t.Errorf("同じ値のハッシュが違います: %v / %v", first, second)
	}
	// 2^53 をまたぐ id が同じハッシュに潰れたら、値が float64 に
	// 落ちている。
	if first == other {
		t.Errorf("違う値が同じハッシュになりました: %v", first)
	}
	if s, ok := first.(string); !ok || len(s) != hashLength {
		t.Errorf("ハッシュ = %#v, want %d 文字", first, hashLength)
	}

	other2, _ := apply(t, newEngine(t, masking(rule(config.MaskHash, "user_id"))), in)
	if other2.Rows[0][0] == first {
		t.Error("別の実行で同じハッシュになりました。salt が実行ごとに変わっていません")
	}
}

func TestRenderValue(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"NULL", nil, ""},
		{"文字列", "abc", "abc"},
		{"整数", json.Number("9007199254740993"), "9007199254740993"},
		{"小数", json.Number("1.5"), "1.5"},
		{"真偽値", true, "true"},
		{"その他", []any{1, 2}, "[1 2]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RenderValue(tt.value); got != tt.want {
				t.Errorf("RenderValue(%#v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestMaskedValuesAreStrings はマスクした値が常に文字列になることを見る
// （ADR-0004 §3）。null だけは nil を返す。
func TestMaskedValuesAreStrings(t *testing.T) {
	e := newEngine(t, masking(
		rule(config.MaskRedact, "a"),
		rule(config.MaskHash, "b"),
		partialRule("", 1, 0, "c"),
		rule(config.MaskNull, "d"),
		rule(config.MaskNone, "e"),
	))

	got, _ := apply(t, e, result([]string{"a", "b", "c", "d", "e"},
		[]any{json.Number("1"), json.Number("2"), json.Number("345"), json.Number("4"), json.Number("5")}))

	for i, name := range []string{"a", "b", "c"} {
		if _, ok := got.Rows[0][i].(string); !ok {
			t.Errorf("列 %s = %#v (%T), want string", name, got.Rows[0][i], got.Rows[0][i])
		}
	}
	if got.Rows[0][3] != nil {
		t.Errorf("列 d = %#v, want nil", got.Rows[0][3])
	}
	// マスクしていない列は元の型のまま。
	if want := json.Number("5"); !reflect.DeepEqual(got.Rows[0][4], want) {
		t.Errorf("列 e = %#v, want %#v", got.Rows[0][4], want)
	}
}
