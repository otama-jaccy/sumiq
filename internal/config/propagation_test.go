package config

import (
	"strings"
	"testing"
)

// dsWithGuard は alias_guard を指定したデータソース定義1つ分の設定を返す。
func dsWithGuard(guard string) string {
	return "version: 1\ndata_sources:\n  - name: mongo\n    id: 9\n    alias_guard: " + guard + "\n"
}

func TestResolve_AliasGuardOffは共有ファイル専用(t *testing.T) {
	off := dsWithGuard("off")

	tests := []struct {
		name    string
		files   layerFiles
		wantErr bool
	}{
		{name: "共有ファイルには書ける", files: layerFiles{shared: off}},
		{name: "--config 指定のファイルにも書ける", files: layerFiles{explicit: off}},
		{name: "ローカルには書けない", files: layerFiles{local: off}, wantErr: true},
		{name: "ユーザ設定にも書けない", files: layerFiles{user: off}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr {
				wantError(t, tt.files, "alias_guard: \"off\" は共有ファイル")
				return
			}
			res := mustResolve(t, tt.files)
			ds, _, ok := res.DataSource("mongo")
			if !ok {
				t.Fatal("mongo が定義されていません")
			}
			if ds.AliasGuard != AliasGuardOff {
				t.Errorf("alias_guard = %q, want %q", ds.AliasGuard, AliasGuardOff)
			}
			if ds.AliasGuard.Strict() {
				t.Error("off なのに Strict() が true です")
			}
		})
	}
}

// TestResolve_AliasGuardOffは負けたレイヤでも弾く は、名前の勝ち負けと
// 項目ごとの引き継ぎを経ても off が紛れ込まないことを見る。
//
// 共有設定が同じ名前を後から定義すると、レビューされないレイヤの定義は
// 名前の上書きとしては負ける。それでも mergeDataSource は指定された項目を
// 引き継ぐため、畳んだ後の値だけを見る検査では off が生き残る。
func TestResolve_AliasGuardOffは負けたレイヤでも弾く(t *testing.T) {
	wantError(t, layerFiles{
		user:   dsWithGuard("off"),
		shared: "version: 1\ndata_sources:\n  - name: mongo\n    id: 9\n",
	}, "alias_guard: \"off\" は共有ファイル")
}

func TestResolve_AliasGuardの既定はStrict(t *testing.T) {
	tests := []struct {
		name  string
		files layerFiles
	}{
		{name: "指定しない", files: layerFiles{shared: "version: 1\ndata_sources:\n  - name: mongo\n    id: 9\n"}},
		{name: "strict と書く", files: layerFiles{shared: dsWithGuard("strict")}},
		{name: "レビューされないレイヤの strict", files: layerFiles{local: dsWithGuard("strict")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := mustResolve(t, tt.files)
			ds, _, ok := res.DataSource("mongo")
			if !ok {
				t.Fatal("mongo が定義されていません")
			}
			if !ds.AliasGuard.Strict() {
				t.Errorf("alias_guard = %q なのに Strict() が false です", ds.AliasGuard)
			}
		})
	}
}

func TestResolve_AliasGuardの不正な値(t *testing.T) {
	for _, guard := range []string{"on", "yes", `""`, "strict, off"} {
		t.Run(guard, func(t *testing.T) {
			wantError(t, layerFiles{shared: dsWithGuard(guard)}, "alias_guard")
		})
	}
}

// exemptConfig は許可リスト1件だけを持つ設定を返す。
func exemptConfig(name string) string {
	return "version: 1\nmasking:\n  propagation_exempt_functions:\n    - name: " + name +
		"\n      note: \"件数しか出ないため\"\n"
}

func TestResolve_許可リストは共有ファイル専用(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		wantErr bool
	}{
		{name: "共有ファイルには書ける", files: layerFiles{shared: exemptConfig("count")}},
		{name: "--config 指定のファイルにも書ける", files: layerFiles{explicit: exemptConfig("count")}},
		{name: "ローカルには書けない", files: layerFiles{local: exemptConfig("count")}, wantErr: true},
		{name: "ユーザ設定にも書けない", files: layerFiles{user: exemptConfig("count")}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr {
				wantError(t, tt.files, "propagation_exempt_functions は共有ファイル")
				return
			}
			res := mustResolve(t, tt.files)
			if got := res.Config.Masking.ExemptFunctionNames(); len(got) != 1 || got[0] != "count" {
				t.Errorf("許可リスト = %v, want [count]", got)
			}
		})
	}
}

// TestResolve_許可リストの既定は空 は、count のような関数が組み込みで
// 許されていないことを見る。設定に書いていない弱化が既定で効いていると、
// 事故のときに「なぜ外れたか」を設定から追えない。
func TestResolve_許可リストの既定は空(t *testing.T) {
	res := mustResolve(t, layerFiles{shared: "version: 1\nmasking: {default_action: none}\n"})
	if got := res.Config.Masking.ExemptFunctionNames(); len(got) != 0 {
		t.Errorf("許可リスト = %v, want 空", got)
	}
}

func TestResolve_許可リストの関数名(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "普通の関数名", value: "count"},
		{name: "大文字", value: "COUNT"},
		{name: "下線と数字", value: "count_2"},
		{name: "空", value: `""`, wantErr: "name が指定されていません"},
		{name: "グロブ", value: `"count*"`, wantErr: "完全一致でのみ"},
		{name: "疑問符", value: `"coun?"`, wantErr: "完全一致でのみ"},
		{name: "正規表現", value: `"regex:^count$"`, wantErr: "完全一致でのみ"},
		{name: "括弧付き", value: `"count(email)"`, wantErr: "括弧や空白は書けません"},
		{name: "空白入り", value: `"count distinct"`, wantErr: "括弧や空白は書けません"},
		{name: "スキーマ修飾", value: `"pg_catalog.count"`, wantErr: "スキーマ修飾"},
		{name: "数字始まり", value: `"2count"`, wantErr: "扱えない文字"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := layerFiles{shared: exemptConfig(tt.value)}
			if tt.wantErr != "" {
				wantError(t, files, tt.wantErr)
				return
			}
			res := mustResolve(t, files)
			if got := res.Config.Masking.ExemptFunctionNames(); len(got) != 1 {
				t.Errorf("許可リスト = %v, want 1件", got)
			}
		})
	}
}

func TestResolve_許可リストの重複(t *testing.T) {
	dup := "version: 1\nmasking:\n  propagation_exempt_functions:\n" +
		"    - name: count\n    - name: COUNT\n"
	wantError(t, layerFiles{shared: dup}, "同じ関数名が2回")
}

// TestExemptFunctionNamesは順序を保つ は、設定に書いた順がそのまま
// internal/sqlalias に渡ることを見る。
func TestExemptFunctionNamesは順序を保つ(t *testing.T) {
	m := Masking{PropagationExemptFunctions: []ExemptFunction{
		{Name: "count"}, {Name: "approx_distinct"},
	}}
	got := strings.Join(m.ExemptFunctionNames(), ",")
	if want := "count,approx_distinct"; got != want {
		t.Errorf("ExemptFunctionNames() = %q, want %q", got, want)
	}
}
