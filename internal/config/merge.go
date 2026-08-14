package config

import (
	"fmt"
)

// マージ規則の一覧。項目ごとに規則が違うのは ADR-0003 の意図した設計であり、
// 「全部上書き」に均そうとするとマスクがローカルから弱められる。
//
//	項目                              規則
//	--------------------------------  --------------------------------------------------
//	redash.*                          上書き（後のレイヤが勝つ。ゼロ値は未指定として無視）
//	query.*                           上書き
//	output.format                     上書き
//	redash.api_key / api_key_command  組で上書き。同一レイヤで両方指定はエラー
//	masking.rules                     和集合（追加のみ。削除・上書きは不可）
//	masking.default_action            厳しくする方向のみ上書き可。緩める指定はエラー
//	data_sources                      名前で追加。既出の名前の再定義はエラー。由来を保持する
//
// これに加えて、レビューされないレイヤ（ユーザ設定 / ローカル設定）には次の制約がかかる。
//
//	masking.rules の method: none     共有ファイルにのみ書ける。それ以外にあればエラー
//	data_sources の default_action    グローバル既定より緩くできない
//
// いずれも「ローカル設定でマスクが弱まらないこと」を構造で担保するためのもの。
// 規則を足すときは、それが単調（fail-closed）かどうかを先に確かめること。

// layered は1レイヤ分の設定と、その出どころ。
type layered struct {
	layer Layer
	// path はファイル由来なら実パス。埋め込みデフォルトと環境変数では空。
	path string
	cfg  *Config
}

// origin はエラーメッセージ用に、レイヤ名とファイルパスを人が読める形にする。
func (l layered) origin() string {
	if l.path == "" {
		return l.layer.String()
	}
	return fmt.Sprintf("%s (%s)", l.layer.String(), l.path)
}

// Resolved はマージと解決を終えた設定。
type Resolved struct {
	// Config はマージ後の設定。
	//
	// Redash.APIKey と Redash.APIKeyCommand は意図的に空のまま残す。
	// 生の指定（${env:VAR} の展開前、経路の優先順位の適用前）を読める場所に
	// 置くと、解決を経ずにそれを使う実装がいずれ現れるため。API KEY は
	// APIKey フィールドからのみ取れる。
	Config Config
	// APIKey は3経路から解決した Redash の API KEY。
	// どの経路にも指定が無ければ空。実行時に必要かどうかは呼び出し側が判断する。
	APIKey string

	// keySource は API KEY の指定がどのレイヤ・どのファイル由来かを保持する。
	// git 管理下のファイル由来かを判定するためにパスが要る。
	keySource apiKeySource

	// dataSourceLayers はデータソース名 → 定義したレイヤ。
	// ローカル定義のデータソースを使う実行で警告を出す（#7）ために保持する。
	dataSourceLayers map[string]Layer
	// files は実際に読んだファイルを弱いレイヤ順に並べたもの。
	files []SourceFile
}

// SourceFile は実際に読み込んだ設定ファイル1枚。
type SourceFile struct {
	Layer Layer
	Path  string
}

// Files は実際に読み込んだ設定ファイルを弱いレイヤ順に返す。
func (r *Resolved) Files() []SourceFile {
	return append([]SourceFile(nil), r.files...)
}

// DataSource は name のデータソースと、その定義がどのレイヤ由来かを返す。
//
// レイヤを一緒に返すのは、レビューされていない定義（Layer.Reviewed() が false）を
// 使う実行で警告を出せるようにするため。値だけ返すとその判断ができない。
func (r *Resolved) DataSource(name string) (DataSource, Layer, bool) {
	for _, ds := range r.Config.DataSources {
		if ds.Name == name {
			return ds, r.dataSourceLayers[name], true
		}
	}
	return DataSource{}, LayerDefault, false
}

// merge はレイヤを弱い順に畳み込む。layers は弱いレイヤから並んでいること。
func merge(layers []layered) (*Resolved, error) {
	res := &Resolved{dataSourceLayers: map[string]Layer{}}
	var keySrc apiKeySource

	for _, l := range layers {
		if l.cfg == nil {
			continue
		}
		if l.path != "" {
			res.files = append(res.files, SourceFile{Layer: l.layer, Path: l.path})
		}
		mergeRedash(&res.Config.Redash, l.cfg.Redash)
		if err := keySrc.absorb(l); err != nil {
			return nil, err
		}
		mergeQuery(&res.Config.Query, l.cfg.Query)
		mergeOutput(&res.Config.Output, l.cfg.Output)
		if err := mergeMasking(&res.Config.Masking, l); err != nil {
			return nil, err
		}
		if err := res.mergeDataSources(l); err != nil {
			return nil, err
		}
	}

	// データソースの default_action はグローバル既定と突き合わせて初めて
	// 緩いかどうかが決まる。全レイヤを畳んだ後でなければ判定できない。
	if err := res.checkDataSourceActions(); err != nil {
		return nil, err
	}
	res.Config.Version = SchemaVersion
	res.keySource = keySrc
	return res, nil
}

// mergeRedash は接続設定を畳む。api_key と api_key_command は経路の優先順位が
// 別にあるため、ここでは扱わない（apiKeySource が持つ）。
func mergeRedash(dst *Redash, src Redash) {
	if src.Endpoint != "" {
		dst.Endpoint = src.Endpoint
	}
	if src.Timeout != 0 {
		dst.Timeout = src.Timeout
	}
	if src.PollInterval != 0 {
		dst.PollInterval = src.PollInterval
	}
}

func mergeQuery(dst *Query, src Query) {
	if src.AutoLimit != nil {
		v := *src.AutoLimit
		dst.AutoLimit = &v
	}
	if src.MaxRows != 0 {
		dst.MaxRows = src.MaxRows
	}
	if src.OnExceed != "" {
		dst.OnExceed = src.OnExceed
	}
}

func mergeOutput(dst *Output, src Output) {
	if src.Format != "" {
		dst.Format = src.Format
	}
}

// mergeMasking はマスク方針を畳む。ここがこのパッケージで最も壊すと危険な箇所。
func mergeMasking(dst *Masking, l layered) error {
	src := l.cfg.Masking

	// default_action は厳しくする方向にしか動かさない。
	if src.DefaultAction != "" {
		if src.DefaultAction.strictness() < dst.DefaultAction.strictness() {
			return fmt.Errorf("%s: masking.default_action を %q から %q に緩めることはできません。"+
				"マスクの既定は厳しくする方向にしか上書きできません",
				l.origin(), dst.DefaultAction, src.DefaultAction)
		}
		dst.DefaultAction = src.DefaultAction
	}

	// rules は和集合。上書きも削除もせず、後ろに足すだけ。
	for i, r := range src.Rules {
		// method: none は「この列は素通ししてよい」という明示的な許可であり、
		// allowlist 運用に穴を開ける唯一の手段。レビューされないファイルから
		// 書けると弱化そのものになるため、共有ファイル以外では受け付けない。
		if r.Method == MaskNone && !l.layer.Reviewed() {
			return fmt.Errorf("%s: masking.rules[%d] %v: method: none は共有ファイル（%s）にのみ書けます。"+
				"マスクを外す指定はレビューの対象でなければなりません",
				l.origin(), i, r.Patterns, SharedFileName)
		}
		dst.Rules = append(dst.Rules, r)
	}
	return nil
}

// mergeDataSources は名前をキーにデータソースを足す。
//
// ローカルからの追加は許可するが、既に定義済みの名前の再定義は許可しない。
// ADR-0003 が認めているのは「追加」であって差し替えではなく、再定義を許すと
// 共有設定でレビューされた default_action や id をローカルから置き換えられる。
func (r *Resolved) mergeDataSources(l layered) error {
	for i, ds := range l.cfg.DataSources {
		if prev, ok := r.dataSourceLayers[ds.Name]; ok {
			return fmt.Errorf("%s: data_sources[%d] (%s): この名前は %s で定義済みです。"+
				"データソースは追加のみできます。定義を変えたい場合は定義元を直してください",
				l.origin(), i, ds.Name, prev.String())
		}
		r.dataSourceLayers[ds.Name] = l.layer
		r.Config.DataSources = append(r.Config.DataSources, ds)
	}
	return nil
}

// checkDataSourceActions はレビューされないレイヤで定義されたデータソースが、
// グローバル既定より緩い default_action を持っていないことを確かめる。
//
// 未指定ならグローバル既定をそのまま継承するので緩くはならない。
// 共有ファイルの定義は対象外とする。ADR-0003 §8 はグローバルを緩く保ったまま
// データソース単位で引き上げる運用を前提にしており、レビュー済みの引き下げまで
// 禁じると、その運用に必要な自由度を潰してしまう。
func (r *Resolved) checkDataSourceActions() error {
	global := r.Config.Masking.DefaultAction
	for _, ds := range r.Config.DataSources {
		layer := r.dataSourceLayers[ds.Name]
		if layer.Reviewed() || ds.DefaultAction == "" {
			continue
		}
		if ds.DefaultAction.strictness() < global.strictness() {
			return fmt.Errorf("%s で定義された data_sources (%s): default_action: %q は"+
				"グローバル既定 %q より緩いため指定できません",
				layer.String(), ds.Name, ds.DefaultAction, global)
		}
	}
	return nil
}

// strictness は Action の厳しさを返す。大きいほど厳しい。
//
// 未指定（空）は「弱める指定ではない」として最も緩い扱いにするが、
// 呼び出し側は空を上書き対象から除いているため、この値が比較に使われることはない。
func (a Action) strictness() int {
	if a == ActionRedact {
		return 1
	}
	return 0
}
