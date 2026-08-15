package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// layerFiles はレイヤごとに置くファイルの内容。空文字なら置かない。
type layerFiles struct {
	user     string
	shared   string
	local    string
	explicit string
	environ  []string
}

// resolveWith は layerFiles を実ファイルに落として Resolve を通す。
//
// merge() を直接叩かず Resolve 越しに検証するのは、レイヤの組み立て
// （どのファイルがどの Layer になるか）まで含めて壊れを検出したいため。
// ここを分けると「マージ規則は正しいが共有ファイルがローカル扱い」を見逃す。
func resolveWith(t *testing.T, f layerFiles) (*Resolved, error) {
	t.Helper()
	dir := t.TempDir()
	mkGitRoot(t, dir)

	opts := testOptions(dir)
	opts.Environ = f.environ
	if opts.Environ == nil {
		opts.Environ = []string{}
	}
	if f.user != "" {
		opts.UserConfigPath = writeFile(t, dir, "user-config.yaml", f.user)
	}
	if f.shared != "" {
		writeFile(t, dir, SharedFileName, f.shared)
	}
	if f.local != "" {
		writeFile(t, dir, LocalFileName, f.local)
	}
	if f.explicit != "" {
		opts.ConfigPath = writeFile(t, dir, "explicit.yaml", f.explicit)
	}
	return Resolve(opts)
}

func mustResolve(t *testing.T, f layerFiles) *Resolved {
	t.Helper()
	res, err := resolveWith(t, f)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	return res
}

func wantError(t *testing.T, f layerFiles, msg string) {
	t.Helper()
	res, err := resolveWith(t, f)
	if err == nil {
		t.Fatalf("Resolve() error = nil, want error。結果 = %+v", res.Config)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Errorf("Resolve() error = %q, want に %q を含む", err.Error(), msg)
	}
}

func TestResolve_埋め込みデフォルト(t *testing.T) {
	res := mustResolve(t, layerFiles{})

	want := defaults()
	if !reflect.DeepEqual(&res.Config, want) {
		t.Errorf("Config = %+v\nwant %+v", res.Config, want)
	}
}

// スカラーは後のレイヤが勝つ。未指定（ゼロ値）は上書きしない。
func TestResolve_スカラーの上書き(t *testing.T) {
	tests := []struct {
		name  string
		files layerFiles
		check func(*testing.T, *Resolved)
	}{
		{
			name:  "共有がデフォルトに勝つ",
			files: layerFiles{shared: "version: 1\nredash: {endpoint: https://shared.example.com, timeout: 10s}\n"},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Config.Redash.Endpoint; got != "https://shared.example.com" {
					t.Errorf("endpoint = %q", got)
				}
				if got := r.Config.Redash.Timeout.Duration(); got != 10*time.Second {
					t.Errorf("timeout = %v, want 10s", got)
				}
			},
		},
		{
			name: "ローカルが共有に勝つ",
			files: layerFiles{
				shared: "version: 1\nredash: {endpoint: https://shared.example.com}\n",
				local:  "version: 1\nredash: {endpoint: https://local.example.com}\n",
			},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Config.Redash.Endpoint; got != "https://local.example.com" {
					t.Errorf("endpoint = %q", got)
				}
			},
		},
		{
			name: "共有がユーザ設定に勝つ",
			files: layerFiles{
				user:   "version: 1\nredash: {endpoint: https://user.example.com}\n",
				shared: "version: 1\nredash: {endpoint: https://shared.example.com}\n",
			},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Config.Redash.Endpoint; got != "https://shared.example.com" {
					t.Errorf("endpoint = %q", got)
				}
			},
		},
		{
			name: "環境変数が全ファイルに勝つ",
			files: layerFiles{
				shared:  "version: 1\nredash: {endpoint: https://shared.example.com}\n",
				local:   "version: 1\nredash: {endpoint: https://local.example.com}\n",
				environ: []string{"SUMIQ_REDASH_ENDPOINT=https://env.example.com"},
			},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Config.Redash.Endpoint; got != "https://env.example.com" {
					t.Errorf("endpoint = %q", got)
				}
			},
		},
		{
			name: "未指定の項目は下のレイヤの値が残る",
			files: layerFiles{
				shared: "version: 1\nredash: {endpoint: https://shared.example.com, timeout: 10s}\n",
				local:  "version: 1\nredash: {timeout: 20s}\n",
			},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Config.Redash.Endpoint; got != "https://shared.example.com" {
					t.Errorf("endpoint = %q, want 共有の値が残る", got)
				}
				if got := r.Config.Redash.Timeout.Duration(); got != 20*time.Second {
					t.Errorf("timeout = %v, want 20s", got)
				}
			},
		},
		{
			// bool を *bool で受けていないと、明示的な false が「未指定」と
			// 区別できず、デフォルトの true に戻ってしまう。
			name: "明示的な false はデフォルトの true を上書きする",
			files: layerFiles{
				shared: "version: 1\nquery: {auto_limit: false}\n",
			},
			check: func(t *testing.T, r *Resolved) {
				if r.Config.Query.AutoLimit == nil || *r.Config.Query.AutoLimit {
					t.Errorf("auto_limit = %v, want false", r.Config.Query.AutoLimit)
				}
			},
		},
		{
			name: "output.format はローカルからも上書きできる",
			files: layerFiles{
				shared: "version: 1\noutput: {format: json}\n",
				local:  "version: 1\noutput: {format: csv}\n",
			},
			check: func(t *testing.T, r *Resolved) {
				if got := r.Config.Output.Format; got != FormatCSV {
					t.Errorf("format = %q", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, mustResolve(t, tt.files))
		})
	}
}

// rules は和集合。どのレイヤのルールも消えない。
func TestResolve_マスクルールは和集合(t *testing.T) {
	res := mustResolve(t, layerFiles{
		user:   "version: 1\nmasking:\n  rules:\n    - patterns: [u]\n      method: hash\n",
		shared: "version: 1\nmasking:\n  rules:\n    - patterns: [s]\n      method: redact\n",
		local:  "version: 1\nmasking:\n  rules:\n    - patterns: [l]\n      method: drop\n",
	})

	want := []MaskRule{
		{Patterns: []string{"u"}, Method: MaskHash},
		{Patterns: []string{"s"}, Method: MaskRedact},
		{Patterns: []string{"l"}, Method: MaskDrop},
	}
	if !reflect.DeepEqual(res.Config.Masking.Rules, want) {
		t.Errorf("rules = %+v\nwant %+v", res.Config.Masking.Rules, want)
	}
}

// 同じパターンをローカルから書いても、共有のルールは消えない。
// ここが「上書き」になっていると、ローカルから弱い method に差し替えられる。
func TestResolve_ローカルは共有ルールを上書きできない(t *testing.T) {
	res := mustResolve(t, layerFiles{
		shared: "version: 1\nmasking:\n  rules:\n    - patterns: [\"*email*\"]\n      method: drop\n",
		local:  "version: 1\nmasking:\n  rules:\n    - patterns: [\"*email*\"]\n      method: partial\n",
	})

	rules := res.Config.Masking.Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %+v, want 2件（共有のルールが残っていること）", rules)
	}
	if rules[0].Method != MaskDrop {
		t.Errorf("rules[0].method = %q, want %q。共有のルールが消えている", rules[0].Method, MaskDrop)
	}
}

func TestResolve_DefaultActionは厳しくする方向のみ(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		want    Action
		wantErr string
	}{
		{
			name:  "none から redact への引き上げは通る",
			files: layerFiles{shared: "version: 1\nmasking: {default_action: redact}\n"},
			want:  ActionRedact,
		},
		{
			name: "ローカルからの引き上げも通る",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: none}\n",
				local:  "version: 1\nmasking: {default_action: redact}\n",
			},
			want: ActionRedact,
		},
		{
			name: "同じ値の再指定は通る",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: redact}\n",
				local:  "version: 1\nmasking: {default_action: redact}\n",
			},
			want: ActionRedact,
		},
		{
			name: "ローカルからの引き下げはエラー",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: redact}\n",
				local:  "version: 1\nmasking: {default_action: none}\n",
			},
			wantErr: "緩めることはできません",
		},
		{
			name: "ユーザ設定からの引き下げもエラー",
			files: layerFiles{
				user:   "version: 1\nmasking: {default_action: redact}\n",
				shared: "version: 1\nmasking: {default_action: none}\n",
			},
			wantErr: "緩めることはできません",
		},
		{
			// 環境変数もファイルと同じ規則を通すこと。ここが素通りだと
			// SUMIQ_MASKING_DEFAULT_ACTION=none がマスク解除の逃げ道になる。
			name: "環境変数からの引き下げもエラー",
			files: layerFiles{
				shared:  "version: 1\nmasking: {default_action: redact}\n",
				environ: []string{"SUMIQ_MASKING_DEFAULT_ACTION=none"},
			},
			wantErr: "緩めることはできません",
		},
		{
			name:  "環境変数からの引き上げは通る",
			files: layerFiles{environ: []string{"SUMIQ_MASKING_DEFAULT_ACTION=redact"}},
			want:  ActionRedact,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr != "" {
				wantError(t, tt.files, tt.wantErr)
				return
			}
			if got := mustResolve(t, tt.files).Config.Masking.DefaultAction; got != tt.want {
				t.Errorf("default_action = %q, want %q", got, tt.want)
			}
		})
	}
}

// method: none は allowlist 運用に穴を開ける唯一の手段なので、
// レビューされるファイルにしか書けない。
func TestResolve_MethodNoneは共有ファイル専用(t *testing.T) {
	const rule = "version: 1\nmasking:\n  rules:\n    - patterns: [\"public_id\"]\n      method: none\n"

	tests := []struct {
		name    string
		files   layerFiles
		wantErr bool
	}{
		{name: "共有ファイルには書ける", files: layerFiles{shared: rule}},
		{name: "--config 指定のファイルにも書ける", files: layerFiles{explicit: rule}},
		{name: "ローカルには書けない", files: layerFiles{local: rule}, wantErr: true},
		{name: "ユーザ設定にも書けない", files: layerFiles{user: rule}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr {
				wantError(t, tt.files, "method: none は共有ファイル")
				return
			}
			res := mustResolve(t, tt.files)
			if got := res.Config.Masking.Rules[0].Method; got != MaskNone {
				t.Errorf("method = %q, want %q", got, MaskNone)
			}
		})
	}
}

func TestResolve_データソースの由来を保持する(t *testing.T) {
	res := mustResolve(t, layerFiles{
		user:   "version: 1\ndata_sources: [{name: from-user, id: 1}]\n",
		shared: "version: 1\ndata_sources: [{name: analytics, id: 3}]\n",
		local:  "version: 1\ndata_sources: [{name: my-sandbox, id: 99}]\n",
	})

	tests := []struct {
		name      string
		wantID    int
		wantLayer Layer
		// wantReviewed が false のデータソースは #7 で警告の対象になる。
		wantReviewed bool
	}{
		{name: "analytics", wantID: 3, wantLayer: LayerShared, wantReviewed: true},
		{name: "my-sandbox", wantID: 99, wantLayer: LayerLocal, wantReviewed: false},
		{name: "from-user", wantID: 1, wantLayer: LayerUser, wantReviewed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds, layer, ok := res.DataSource(tt.name)
			if !ok {
				t.Fatalf("DataSource(%q) が見つかりません", tt.name)
			}
			if ds.ID != tt.wantID {
				t.Errorf("id = %d, want %d", ds.ID, tt.wantID)
			}
			if layer != tt.wantLayer {
				t.Errorf("layer = %v, want %v", layer, tt.wantLayer)
			}
			if layer.Reviewed() != tt.wantReviewed {
				t.Errorf("Reviewed() = %v, want %v", layer.Reviewed(), tt.wantReviewed)
			}
		})
	}

	if _, _, ok := res.DataSource("存在しない"); ok {
		t.Error("DataSource(存在しない) = ok, want 見つからない")
	}
}

// レビューされない設定から共有設定のデータソースを差し替えることはできない。
// 許すと、レビュー済みの id や default_action をローカルから置き換えられる。
func TestResolve_データソースの再定義(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		wantErr string
		// wantID は再定義が通る場合の期待値。
		wantID    int
		wantLayer Layer
	}{
		{
			name: "ローカルは共有の名前を差し替えられない",
			files: layerFiles{
				shared: "version: 1\ndata_sources: [{name: analytics, id: 3, default_action: redact}]\n",
				local:  "version: 1\ndata_sources: [{name: analytics, id: 99}]\n",
			},
			wantErr: "差し替えることはできません",
		},
		{
			name: "ユーザ設定も共有の名前を差し替えられない",
			files: layerFiles{
				user:   "version: 1\ndata_sources: [{name: analytics, id: 99}]\n",
				shared: "version: 1\ndata_sources: [{name: other, id: 1}]\n",
				local:  "version: 1\ndata_sources: [{name: other, id: 2}]\n",
			},
			wantErr: "差し替えることはできません",
		},
		{
			// 逆向きは通す。ADR-0003 §2 の「下が勝つ」そのもので、ここを
			// エラーにすると、ユーザ設定に名前を1つ置いただけで、その名前を
			// 使う全リポジトリが動かなくなる。
			name: "共有はユーザ設定の名前を上書きできる",
			files: layerFiles{
				user:   "version: 1\ndata_sources: [{name: analytics, id: 99}]\n",
				shared: "version: 1\ndata_sources: [{name: analytics, id: 3}]\n",
			},
			wantID:    3,
			wantLayer: LayerShared,
		},
		{
			name:    "同じファイル内での重複はエラー",
			files:   layerFiles{shared: "version: 1\ndata_sources: [{name: a, id: 1}, {name: a, id: 2}]\n"},
			wantErr: "同じ名前が2回定義されています",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr != "" {
				wantError(t, tt.files, tt.wantErr)
				return
			}
			res := mustResolve(t, tt.files)
			ds, layer, ok := res.DataSource("analytics")
			if !ok {
				t.Fatal("DataSource(analytics) が見つかりません")
			}
			if ds.ID != tt.wantID {
				t.Errorf("id = %d, want %d", ds.ID, tt.wantID)
			}
			if layer != tt.wantLayer {
				t.Errorf("layer = %v, want %v", layer, tt.wantLayer)
			}
			// 上書きであって追加ではないので、件数は増えない。
			if n := len(res.Config.DataSources); n != 1 {
				t.Errorf("data_sources = %d件, want 1件", n)
			}
		})
	}
}

// データソースは、レイヤに関わらずグローバル既定より緩い default_action を持てない。
// 共有ファイルの定義も対象（#18）。internal/mask が厳格化方向にしか反映しないため、
// レビュー済みの引き下げを config が許しても実行時には黙って無視されてしまう。
func TestResolve_ローカルデータソースのDefaultAction(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		wantErr string
	}{
		{
			name: "グローバルより緩いローカル定義はエラー",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: redact}\n",
				local:  "version: 1\ndata_sources: [{name: sandbox, id: 9, default_action: none}]\n",
			},
			wantErr: "より緩いため指定できません",
		},
		{
			name: "ユーザ設定の定義も同じ扱い",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: redact}\n",
				user:   "version: 1\ndata_sources: [{name: sandbox, id: 9, default_action: none}]\n",
			},
			wantErr: "より緩いため指定できません",
		},
		{
			name: "同じ強さなら通る",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: redact}\n",
				local:  "version: 1\ndata_sources: [{name: sandbox, id: 9, default_action: redact}]\n",
			},
		},
		{
			name: "未指定ならグローバル既定を継承するので通る",
			files: layerFiles{
				shared: "version: 1\nmasking: {default_action: redact}\n",
				local:  "version: 1\ndata_sources: [{name: sandbox, id: 9}]\n",
			},
		},
		{
			name: "引き上げは通る",
			files: layerFiles{
				local: "version: 1\ndata_sources: [{name: sandbox, id: 9, default_action: redact}]\n",
			},
		},
		{
			// 共有ファイルの定義もグローバルより緩くはできない。internal/mask は
			// データソース単位の指定を厳格化方向にしか反映しないため、レビュー済みの
			// 引き下げを config が許しても実行時には黙って無視される
			// （書いた設定が効かない事故になる。ADR-0015 参照）。
			name:    "共有ファイルの定義も対象",
			files:   layerFiles{shared: "version: 1\nmasking: {default_action: redact}\ndata_sources: [{name: sandbox, id: 9, default_action: none}]\n"},
			wantErr: "より緩いため指定できません",
		},
		{
			// 判定はグローバル既定を全部畳んだ後で行う。ローカルの方が先に
			// 読まれる順序になっていると、後から上がった既定を見落とす。
			name: "後から引き上がったグローバル既定にも照らす",
			files: layerFiles{
				local:   "version: 1\ndata_sources: [{name: sandbox, id: 9, default_action: none}]\n",
				environ: []string{"SUMIQ_MASKING_DEFAULT_ACTION=redact"},
			},
			wantErr: "より緩いため指定できません",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr != "" {
				wantError(t, tt.files, tt.wantErr)
				return
			}
			mustResolve(t, tt.files)
		})
	}
}

// 共有設定は複数ファイルになりうるので、データソースの再定義は項目ごとに畳む。
// 構造体を丸ごと差し替えると、書いていない項目が黙って消える。
func TestResolve_データソースは項目ごとに畳む(t *testing.T) {
	root := t.TempDir()
	mkGitRoot(t, root)
	writeFile(t, root, SharedFileName,
		"version: 1\ndata_sources: [{name: analytics, id: 3, description: 本番, default_action: redact}]\n")
	// ADR-0003 §10 が Oracle / SQL Server 向けに勧めている書き方。
	// auto_limit だけを足すつもりで default_action が消えてはならない。
	sub := filepath.Join(root, "packages", "etl")
	writeFile(t, sub, SharedFileName,
		"version: 1\ndata_sources: [{name: analytics, id: 3, auto_limit: false}]\n")

	res, err := Resolve(testOptions(sub))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	ds, _, ok := res.DataSource("analytics")
	if !ok {
		t.Fatal("DataSource(analytics) が見つかりません")
	}
	if ds.DefaultAction != ActionRedact {
		t.Errorf("default_action = %q, want %q。書いていない項目が消えている", ds.DefaultAction, ActionRedact)
	}
	if ds.Description != "本番" {
		t.Errorf("description = %q, want 本番", ds.Description)
	}
	if ds.AutoLimit == nil || *ds.AutoLimit {
		t.Errorf("auto_limit = %v, want false", ds.AutoLimit)
	}
}

// 緩める指定を弾くとき、厳しい方の出どころをエラーに含めること。
// これが無いと、名前の挙がったファイルを開いても原因が見つからない。
func TestResolve_DefaultActionのエラーは厳しい方の出どころを示す(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	opts := testOptions(dir)
	userPath := writeFile(t, dir, "user-config.yaml", "version: 1\nmasking: {default_action: redact}\n")
	opts.UserConfigPath = userPath
	writeFile(t, dir, SharedFileName, "version: 1\nmasking: {default_action: none}\n")

	_, err := Resolve(opts)
	if err == nil {
		t.Fatal("Resolve() error = nil, want error")
	}
	// 緩い側（共有設定）と厳しい側（ユーザ設定）の両方が分かること。
	if !strings.Contains(err.Error(), SharedFileName) {
		t.Errorf("error = %q, want に緩い側のファイル名を含む", err.Error())
	}
	if !strings.Contains(err.Error(), userPath) {
		t.Errorf("error = %q, want に厳しい側の出どころ %q を含む", err.Error(), userPath)
	}
}

// マスクの強さはデータソースに付けた名前ではなく接続先（id）に紐づく。
// 別名を許すと、名前で照合するルールも default_action も全部すり抜ける。
func TestResolve_データソースの別名(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		wantErr string
	}{
		{
			name: "レビュー済みの id にローカルから別名を付けるのはエラー",
			files: layerFiles{
				shared: "version: 1\ndata_sources: [{name: analytics, id: 3, default_action: redact}]\n",
				local:  "version: 1\ndata_sources: [{name: prod, id: 3}]\n",
			},
			wantErr: "別名を付けることはできません",
		},
		{
			name: "ユーザ設定からの別名もエラー",
			files: layerFiles{
				shared: "version: 1\ndata_sources: [{name: analytics, id: 3}]\n",
				user:   "version: 1\ndata_sources: [{name: prod, id: 3}]\n",
			},
			wantErr: "別名を付けることはできません",
		},
		{
			// 共有ファイル内での別名は許す。レビューされている以上、
			// データソースごとに別のマスク方針を当てる意図的な使い方でありうる。
			name:  "共有ファイル内での別名は許す",
			files: layerFiles{shared: "version: 1\ndata_sources: [{name: a, id: 3}, {name: b, id: 3}]\n"},
		},
		{
			name: "id が違えばローカルからの追加は通る",
			files: layerFiles{
				shared: "version: 1\ndata_sources: [{name: analytics, id: 3}]\n",
				local:  "version: 1\ndata_sources: [{name: sandbox, id: 99}]\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr != "" {
				wantError(t, tt.files, tt.wantErr)
				return
			}
			mustResolve(t, tt.files)
		})
	}
}

// ADR-0003 §7 の要求そのもの。マスクは安全装置であり、レビューされない
// レイヤから弱められる経路が1つでもあれば設計が破れている。
//
// 個々の規則は上のテストで見ているが、それらは実装の都合で分かれているだけで、
// 守りたいのはこの1点。規則を足すときは、まずここに経路を足して落ちることを見る。
func TestResolve_ローカル設定でマスクが弱まらない(t *testing.T) {
	// 共有設定は allowlist 運用（既定 redact）で、email を drop、
	// public_id だけを明示的に通している。この状態を弱める試みを全部落とす。
	const shared = `
version: 1
masking:
  default_action: redact
  rules:
    - patterns: ["*email*"]
      method: drop
    - patterns: ["public_id"]
      method: none
`

	weakenings := []struct {
		name  string
		files layerFiles
		// wantErr が空なら、エラーではなく「弱まっていないこと」を検証する。
		wantErr string
	}{
		{
			name:    "既定を denylist に戻す",
			files:   layerFiles{shared: shared, local: "version: 1\nmasking: {default_action: none}\n"},
			wantErr: "緩めることはできません",
		},
		{
			name:    "環境変数で既定を denylist に戻す",
			files:   layerFiles{shared: shared, environ: []string{"SUMIQ_MASKING_DEFAULT_ACTION=none"}},
			wantErr: "緩めることはできません",
		},
		{
			name:    "ローカルから method: none で列に穴を開ける",
			files:   layerFiles{shared: shared, local: "version: 1\nmasking:\n  rules:\n    - patterns: [\"*email*\"]\n      method: none\n"},
			wantErr: "method: none は共有ファイル",
		},
		{
			name:    "ユーザ設定から method: none で列に穴を開ける",
			files:   layerFiles{shared: shared, user: "version: 1\nmasking:\n  rules:\n    - patterns: [\"*email*\"]\n      method: none\n"},
			wantErr: "method: none は共有ファイル",
		},
		{
			name:    "緩い既定のデータソースをローカルで足す",
			files:   layerFiles{shared: shared, local: "version: 1\ndata_sources: [{name: sandbox, id: 9, default_action: none}]\n"},
			wantErr: "より緩いため指定できません",
		},
		{
			name:    "共有のデータソースをローカルで差し替える",
			files:   layerFiles{shared: shared + "data_sources: [{name: analytics, id: 3}]\n", local: "version: 1\ndata_sources: [{name: analytics, id: 99}]\n"},
			wantErr: "差し替えることはできません",
		},
		{
			// 名前の差し替えを止めても、別名で同じ id を指す経路が残っていた。
			// マスクの強さは名前ではなく接続先に紐づくべきもの。
			name:    "共有のデータソースにローカルから別名を付ける",
			files:   layerFiles{shared: shared + "data_sources: [{name: analytics, id: 3, default_action: redact}]\n", local: "version: 1\ndata_sources: [{name: prod, id: 3}]\n"},
			wantErr: "別名を付けることはできません",
		},
		{
			// これはエラーにならない。ルールが上書きされず和集合になることで、
			// 共有の drop が残ることをもって「弱まっていない」とする。
			name:  "ローカルから同じパターンを弱い method で再定義する",
			files: layerFiles{shared: shared, local: "version: 1\nmasking:\n  rules:\n    - patterns: [\"*email*\"]\n      method: partial\n"},
		},
	}

	for _, tt := range weakenings {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr != "" {
				wantError(t, tt.files, tt.wantErr)
				return
			}
			res := mustResolve(t, tt.files)
			if got := res.Config.Masking.DefaultAction; got != ActionRedact {
				t.Errorf("default_action = %q, want %q", got, ActionRedact)
			}
			var found bool
			for _, r := range res.Config.Masking.Rules {
				if reflect.DeepEqual(r.Patterns, []string{"*email*"}) && r.Method == MaskDrop {
					found = true
				}
			}
			if !found {
				t.Errorf("共有の drop ルールが消えている: rules = %+v", res.Config.Masking.Rules)
			}
		})
	}
}

// レイヤを全部重ねた通しの確認。個々の規則が正しくても、
// 組み合わせたときの順序が違えば結果は変わる。
func TestResolve_全レイヤの通し(t *testing.T) {
	res := mustResolve(t, layerFiles{
		user: `
version: 1
redash: {endpoint: https://user.example.com, poll_interval: 5s}
output: {format: csv}
`,
		shared: `
version: 1
redash: {endpoint: https://shared.example.com, timeout: 60s}
data_sources:
  - name: analytics
    id: 3
    default_action: redact
masking:
  default_action: none
  rules:
    - patterns: ["*email*"]
      method: partial
      keep: domain
`,
		local: `
version: 1
data_sources:
  - name: my-sandbox
    id: 99
masking:
  rules:
    - patterns: ["internal_memo"]
      method: redact
`,
		environ: []string{"SUMIQ_QUERY_MAX_ROWS=500", "SUMIQ_OUTPUT_FORMAT=json"},
	})

	want := Config{
		Version: SchemaVersion,
		Redash: Redash{
			Endpoint:     "https://shared.example.com",
			Timeout:      Duration(60 * time.Second),
			PollInterval: Duration(5 * time.Second),
		},
		DataSources: []DataSource{
			{Name: "analytics", ID: 3, DefaultAction: ActionRedact},
			{Name: "my-sandbox", ID: 99},
		},
		Query: Query{AutoLimit: ptr(true), MaxRows: 500, OnExceed: OnExceedError},
		Masking: Masking{
			DefaultAction: ActionNone,
			Rules: []MaskRule{
				{Patterns: []string{"*email*"}, Method: MaskPartial, Keep: "domain"},
				{Patterns: []string{"internal_memo"}, Method: MaskRedact},
			},
		},
		Output: Output{Format: FormatJSON},
	}
	if !reflect.DeepEqual(res.Config, want) {
		t.Errorf("Config = %+v\nwant %+v", res.Config, want)
	}
}

func TestResolved_Validate(t *testing.T) {
	tests := []struct {
		name    string
		files   layerFiles
		wantErr string
	}{
		{
			name: "endpoint と api_key が揃っていれば通る",
			files: layerFiles{
				shared:  "version: 1\nredash: {endpoint: https://a.example.com}\n",
				environ: []string{EnvAPIKey + "=k"},
			},
		},
		{
			name:    "endpoint が無い",
			files:   layerFiles{environ: []string{EnvAPIKey + "=k"}},
			wantErr: "redash.endpoint が指定されていません",
		},
		{
			name:    "API KEY が無い",
			files:   layerFiles{shared: "version: 1\nredash: {endpoint: https://a.example.com}\n"},
			wantErr: "API KEY がありません",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mustResolve(t, tt.files).Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate() error = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("Validate() error = nil, want %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("Validate() error = %q, want に %q を含む", err.Error(), tt.wantErr)
			}
		})
	}
}
