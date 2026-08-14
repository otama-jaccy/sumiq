package mask

import (
	"testing"
)

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		column  string
		want    bool
	}{
		// グロブ
		{"完全一致", "email", "email", true},
		{"部分一致にはならない", "email", "user_email", false},
		{"* は任意の並び", "*email*", "user_email_address", true},
		{"* は空にもマッチ", "*email*", "email", true},
		{"? は1文字", "user?id", "user_id", true},
		{"? は0文字にはマッチしない", "user?id", "userid", false},
		{"末尾一致", "*tel", "mobile_tel", true},

		// 大文字小文字
		{"グロブは大文字小文字を無視する", "*email*", "User_EMAIL", true},
		{"パターン側が大文字でも無視する", "*EMAIL*", "user_email", true},

		// 区切り文字を持たない
		{"* は / をまたぐ", "*", "payload/user/email", true},
		{"* は / をまたぐ（部分）", "*email*", "payload/email", true},
		{"* は \\ をまたぐ", "*", `a\b`, true},
		{"* は改行をまたぐ", "*", "a\nb", true},

		// * と ? 以外は文字そのもの
		{`\ は打ち消しではなく文字そのもの`, `payload\user`, `payload\user`, true},
		{`\ を打ち消しとして読むとマッチしなくなる`, `payload\user`, "payloaduser", false},
		{"] は文字そのもの", "data]", "data]", true},
		{"* を文字として指すには regex: を使う", `regex:^col\*$`, "col*", true},
		{"[ を含む列名も regex: で指せる", `regex:^data\[0\]$`, "data[0]", true},

		// 正規表現
		{"regex:", "regex:^(first|last)_name$", "first_name", true},
		{"regex: の不一致", "regex:^(first|last)_name$", "middle_name", false},
		{"regex: は部分一致", "regex:email", "user_email_address", true},
		{"regex: も大文字小文字を無視する", "regex:^email$", "EMAIL", true},
		{"regex: は (?-i) で区別に戻せる", "regex:(?-i)^email$", "EMAIL", false},
		{"regex: の . は / にもマッチする", "regex:^payload.user$", "payload/user", true},
		{"regex: 接頭辞は先頭でだけ効く", "a regex:b", "a regex:b", true},

		// 記号を含む列名
		{"記号はそのまま照合する", "col.name", "col.name", true},
		{"グロブの . はワイルドカードではない", "col.name", "colxname", false},
		{"日本語の列名", "*名前*", "顧客名前", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := compilePattern(tt.pattern)
			if err != nil {
				t.Fatalf("compilePattern(%q): %v", tt.pattern, err)
			}
			if got := m.MatchString(tt.column); got != tt.want {
				t.Errorf("%q が %q にマッチ = %v, want %v", tt.pattern, tt.column, got, tt.want)
			}
		})
	}
}

// TestCompilePatternRejects は組み立てられないパターンを弾くことを見る。
//
// マッチしないパターンとして読み飛ばすと、そのルールが消えたまま実行される。
func TestCompilePatternRejects(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"空", ""},
		{"regex: の後が空", "regex:"},
		{"文字クラス", "col[0-9]"},
		{"閉じていない [", "col[0-9"},
		{"[ だけ", "["},
		{"読めない正規表現", "regex:(unclosed"},
		{"読めない正規表現（繰り返し）", "regex:*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := compilePattern(tt.pattern); err == nil {
				t.Errorf("compilePattern(%q) がエラーになりませんでした", tt.pattern)
			}
		})
	}
}
