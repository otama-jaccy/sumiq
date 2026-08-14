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
//	data_sources                      名前で追加。同一ファイル内の重複はエラー。由来を保持する
//
// これに加えて、レビューされないレイヤ（ユーザ設定 / ローカル設定）には次の制約がかかる。
//
//	masking.rules の method: none     共有ファイルにのみ書ける。それ以外にあればエラー
//	data_sources の default_action    グローバル既定より緩くできない
//	data_sources の差し替え           共有設定で定義済みの名前は上書きできない
//	data_sources の id                共有設定で定義済みの id に別名を付けられない
//	redash.api_key / api_key_command  git 管理下のファイルに書けない
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
	// APIKey メソッドからのみ取れる。
	Config Config

	// opts は APIKey の遅延解決に要る。
	opts Options
	// apiKey / apiKeyErr / apiKeyDone は APIKey の結果を1度だけ計算するためのもの。
	apiKey     string
	apiKeyErr  error
	apiKeyDone bool

	// keySource は API KEY の指定がどのレイヤ由来かを保持する。
	keySource apiKeySource
	// apiKeySpecs は API KEY の指定を書いた全ファイル。勝ち負けに関係なく
	// git 管理下かを検査するため、負けたレイヤの分も残す。
	apiKeySpecs []apiKeySpec

	// dataSourceOrigins はデータソース名 → 定義したレイヤとファイル。
	// ローカル定義のデータソースを使う実行で警告を出す（#7）ために保持する。
	//
	// レイヤだけでなくファイルまで持つのは、1つのレイヤが複数ファイルに
	// なりうるため。共有設定はリポジトリルートとサブディレクトリの両方に
	// 置けるので、「同じファイル内の重複」と「近いファイルによる上書き」を
	// レイヤの比較だけでは区別できない。
	dataSourceOrigins map[string]layered
	// defaultActionOrigin は現在の masking.default_action を指定したレイヤ。
	// 緩める指定を弾くとき、厳しい方がどこ由来かをエラーに書くために持つ。
	defaultActionOrigin layered
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

// APIKey は Redash の API KEY を解決して返す。どの経路にも指定が無ければ空を返す。
//
// 解決を Resolve から遅らせているのは、api_key_command が外部コマンドの実行を
// 伴うため。設定を表示するだけのコマンドが 1Password のプロンプトで止まるのは
// おかしい。必要になった時点で初めて実行し、結果は使い回す。
//
// 並行に呼ぶことは想定していない。設定の解決はコマンドの開始時に一度行う。
func (r *Resolved) APIKey() (string, error) {
	if !r.apiKeyDone {
		r.apiKey, r.apiKeyErr = r.keySource.resolve(r.opts)
		r.apiKeyDone = true
	}
	return r.apiKey, r.apiKeyErr
}

// DataSource は name のデータソースと、その定義がどのレイヤ由来かを返す。
//
// レイヤを一緒に返すのは、レビューされていない定義（Layer.Reviewed() が false）を
// 使う実行で警告を出せるようにするため。値だけ返すとその判断ができない。
func (r *Resolved) DataSource(name string) (DataSource, Layer, bool) {
	for _, ds := range r.Config.DataSources {
		if ds.Name == name {
			return ds, r.dataSourceOrigins[name].layer, true
		}
	}
	return DataSource{}, LayerDefault, false
}

// merge はレイヤを弱い順に畳み込む。layers は弱いレイヤから並んでいること。
func merge(layers []layered) (*Resolved, error) {
	res := &Resolved{dataSourceOrigins: map[string]layered{}}
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
		if l.path != "" {
			if l.cfg.Redash.APIKey != "" {
				res.apiKeySpecs = append(res.apiKeySpecs, apiKeySpec{path: l.path, field: "api_key"})
			}
			if len(l.cfg.Redash.APIKeyCommand) > 0 {
				res.apiKeySpecs = append(res.apiKeySpecs, apiKeySpec{path: l.path, field: "api_key_command"})
			}
		}
		mergeQuery(&res.Config.Query, l.cfg.Query)
		mergeOutput(&res.Config.Output, l.cfg.Output)
		if err := res.mergeMasking(&res.Config.Masking, l); err != nil {
			return nil, err
		}
		if err := res.mergeDataSources(l); err != nil {
			return nil, err
		}
	}

	// データソースの検証は全レイヤを畳んだ後でなければできない。
	// グローバル既定との比較も、レビュー済み定義との突き合わせも、
	// 全部が出揃って初めて判定できる。
	if err := res.checkDataSourceActions(); err != nil {
		return nil, err
	}
	if err := res.checkDataSourceIDs(); err != nil {
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
func (r *Resolved) mergeMasking(dst *Masking, l layered) error {
	src := l.cfg.Masking

	// default_action は厳しくする方向にしか動かさない。
	if src.DefaultAction != "" {
		if src.DefaultAction.strictness() < dst.DefaultAction.strictness() {
			// 厳しい方の出どころを必ず添える。これが無いと、利用者は
			// 名前の挙がったファイルを開いて緩い値を見つけ、なぜ怒られたのか
			// 分からなくなる。厳しい値が自分のユーザ設定由来のときが最悪で、
			// リポジトリの中を探しても原因が見つからない。
			return fmt.Errorf("%s: masking.default_action を %q に緩めることはできません。"+
				"%q は %s で指定されています。マスクの既定は厳しくする方向にしか上書きできません",
				l.origin(), src.DefaultAction, dst.DefaultAction, r.defaultActionOrigin.origin())
		}
		dst.DefaultAction = src.DefaultAction
		r.defaultActionOrigin = l
	}

	// rules は和集合。上書きも削除もせず、後ろに足すだけ。
	for i, rule := range src.Rules {
		// method: none は「この列は素通ししてよい」という明示的な許可であり、
		// allowlist 運用に穴を開ける唯一の手段。レビューされないファイルから
		// 書けると弱化そのものになるため、共有ファイル以外では受け付けない。
		if rule.Method == MaskNone && !l.layer.Reviewed() {
			return fmt.Errorf("%s: masking.rules[%d] %v: method: none は共有ファイル（%s）にのみ書けます。"+
				"マスクを外す指定はレビューの対象でなければなりません",
				l.origin(), i, rule.Patterns, SharedFileName)
		}
		dst.Rules = append(dst.Rules, rule)
	}
	return nil
}

// mergeDataSources は名前をキーにデータソースを足す。
//
// 名前がぶつかったときの扱いは3通りに分かれる。
//
//   - 同じファイル内での重複はエラー。1枚のファイルに同じ名前が2回あるのは
//     書き間違いであり、黙って後勝ちにすると片方が消えたまま動く
//   - レビューされる設定（共有）が、レビューされない設定（ユーザ / ローカル）の
//     定義を上書きするのは許す。これは ADR-0003 §2 の「下が勝つ」そのもので、
//     禁じるとユーザ設定に名前を1つ置いただけでリポジトリ全体が動かなくなる
//   - 逆向き、つまりレビューされない設定が共有設定の定義を差し替えるのはエラー。
//     許すと、レビュー済みの id や default_action をローカルから置き換えられる
func (r *Resolved) mergeDataSources(l layered) error {
	for i, ds := range l.cfg.DataSources {
		prev, exists := r.dataSourceOrigins[ds.Name]
		switch {
		case !exists:
			// 新規の名前。
		case prev.layer == l.layer && prev.path == l.path:
			return fmt.Errorf("%s: data_sources[%d] (%s): 同じ名前が2回定義されています",
				l.origin(), i, ds.Name)
		case prev.layer.Reviewed() && !l.layer.Reviewed():
			return fmt.Errorf("%s: data_sources[%d] (%s): この名前は %s で定義済みです。"+
				"レビューされない設定から共有設定のデータソースを差し替えることはできません。"+
				"別の名前で追加してください",
				l.origin(), i, ds.Name, prev.origin())
		}
		r.dataSourceOrigins[ds.Name] = l
		r.setDataSource(ds)
	}
	return nil
}

// setDataSource は同じ名前があれば項目ごとに畳み、無ければ末尾に足す。
//
// 構造体を丸ごと差し替えてはならない。共有設定は複数ファイルになりうるため、
// リポジトリルートで analytics に default_action: redact を付け、
// packages/etl/sumiq.yaml で同じ名前に auto_limit: false だけを足す
// （ADR-0003 §10 が Oracle / SQL Server 向けに勧めている書き方）と、
// 書いていない default_action が黙って消えてマスクが外れる。
func (r *Resolved) setDataSource(ds DataSource) {
	for i := range r.Config.DataSources {
		if r.Config.DataSources[i].Name == ds.Name {
			r.Config.DataSources[i] = mergeDataSource(r.Config.DataSources[i], ds)
			return
		}
	}
	r.Config.DataSources = append(r.Config.DataSources, ds)
}

// mergeDataSource は src で指定された項目だけを dst に上書きする。
func mergeDataSource(dst, src DataSource) DataSource {
	if src.ID != 0 {
		dst.ID = src.ID
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.DefaultAction != "" {
		dst.DefaultAction = src.DefaultAction
	}
	if src.AutoLimit != nil {
		v := *src.AutoLimit
		dst.AutoLimit = &v
	}
	return dst
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
		origin := r.dataSourceOrigins[ds.Name]
		if origin.layer.Reviewed() || ds.DefaultAction == "" {
			continue
		}
		if ds.DefaultAction.strictness() < global.strictness() {
			return fmt.Errorf("%s: data_sources (%s): default_action: %q は"+
				"グローバル既定 %q より緩いため指定できません",
				origin.origin(), ds.Name, ds.DefaultAction, global)
		}
	}
	return nil
}

// checkDataSourceIDs はレビューされないデータソースが、レビュー済みの
// データソースと同じ id を指していないことを確かめる。
//
// 名前の差し替えは mergeDataSources が止めるが、それだけでは
// 「別名で同じ id を指す」経路が残る。共有設定が analytics (id: 3) を
// default_action: redact に上げていても、ローカルから prod (id: 3) を足せば、
// グローバル既定のまま同じ接続先を引ける。analytics に紐づけたルールも
// 名前で照合するため効かない。
//
// マスクの強さは、データソースに付けた名前ではなく接続先で決まるべきもの。
// 別名を許すのは差し替えを許すのと同じ弱化になる。
func (r *Resolved) checkDataSourceIDs() error {
	reviewed := make(map[int]string)
	for _, ds := range r.Config.DataSources {
		if r.dataSourceOrigins[ds.Name].layer.Reviewed() {
			reviewed[ds.ID] = ds.Name
		}
	}
	for _, ds := range r.Config.DataSources {
		origin := r.dataSourceOrigins[ds.Name]
		if origin.layer.Reviewed() {
			continue
		}
		if name, ok := reviewed[ds.ID]; ok {
			return fmt.Errorf("%s: data_sources (%s): id: %d は共有設定の %s と同じです。"+
				"レビューされない設定から、レビュー済みのデータソースに別名を付けることはできません。"+
				"マスク方針は名前ではなく接続先に紐づくため、%s をそのまま使ってください",
				origin.origin(), ds.Name, ds.ID, name, name)
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
