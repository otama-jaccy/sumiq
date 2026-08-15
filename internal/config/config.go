// Package config は sumiq の設定ファイルを読み込む。
//
// スキーマの根拠は docs/adr/0003-config-file-design.md（設定ファイルの設計）と
// docs/adr/0004-output-formats.md（出力形式）にある。
//
// このパッケージが扱うのはファイル1枚のデコードと、その1枚だけで判定できる検証まで。
// 複数ファイルの探索とレイヤードマージは別に実装する。
// そのため各フィールドのゼロ値は「未指定」を意味し、既定値の充填は行わない。
// bool だけは false が有効な指定と区別できないため *bool で受ける。
package config

import "fmt"

// SchemaVersion はこのパッケージが扱える設定ファイルの version。
const SchemaVersion = 1

// Config は設定ファイル1枚分の内容。
type Config struct {
	Version     int          `yaml:"version"`
	Redash      Redash       `yaml:"redash"`
	DataSources []DataSource `yaml:"data_sources"`
	Query       Query        `yaml:"query"`
	Masking     Masking      `yaml:"masking"`
	Output      Output       `yaml:"output"`
}

// Redash は Redash への接続設定。
//
// APIKey と APIKeyCommand は共有ファイルに書かれてはならないが、その検出は
// ファイルが git 管理下にあるかどうかの判定を伴うため、このパッケージでは行わない。
type Redash struct {
	Endpoint      string   `yaml:"endpoint"`
	APIKey        string   `yaml:"api_key"`
	APIKeyCommand []string `yaml:"api_key_command"`
	// Timeout はジョブ完了を待つ上限。
	Timeout Duration `yaml:"timeout"`
	// PollInterval は /api/jobs/{id} をポーリングする間隔。
	PollInterval Duration `yaml:"poll_interval"`
}

// DataSource は -d で名前指定できるデータソース。
//
// ID は Redash の data_source_id。CLI からは名前でしか指定できず、
// 数値 ID への解決はこの定義を経由する。
type DataSource struct {
	Name        string `yaml:"name"`
	ID          int    `yaml:"id"`
	Description string `yaml:"description"`
	// DefaultAction はこのデータソースに限ったマスクの既定動作。
	DefaultAction Action `yaml:"default_action"`
	// AutoLimit はデータソース単位の auto_limit 上書き。
	// Oracle / SQL Server のように apply_auto_limit でクエリが壊れる
	// データソースを個別に false にするためのもの。
	AutoLimit *bool `yaml:"auto_limit"`
}

// Query はクエリ実行時の行数の扱い。
type Query struct {
	// AutoLimit は Redash に apply_auto_limit を渡すかどうか。効けば転送量が減る最適化であり、
	// CTE や非 SQL データソースでは黙って効かないため、これ単独では安全装置にならない。
	AutoLimit *bool `yaml:"auto_limit"`
	// MaxRows は取得後にクライアント側で判定する安全装置。
	MaxRows int `yaml:"max_rows"`
	// OnExceed は MaxRows を超えたときの挙動。
	OnExceed OnExceed `yaml:"on_exceed"`
}

// Masking はマスク方針。
type Masking struct {
	// DefaultAction はどのルールにもマッチしなかった列の扱い。
	DefaultAction Action `yaml:"default_action"`
	// Rules は全レイヤの和集合として適用される。ローカル設定から共有設定を
	// 弱められてはならないため、マージは追加のみを許す。
	Rules []MaskRule `yaml:"rules"`
}

// MaskRule は列パターンとマスク方法の組。
type MaskRule struct {
	// Patterns は既定でグロブ（大文字小文字を無視）。"regex:" 接頭辞で正規表現に切り替わる。
	// 接頭辞の解釈はマスクエンジン側で行う。
	Patterns []string   `yaml:"patterns"`
	Method   MaskMethod `yaml:"method"`
	// Keep / KeepPrefix / KeepSuffix は Method が partial のときの残し方。
	// 詳細仕様は ADR-0003 の未決事項として残っているため、ここでは値を検証しない。
	Keep       string `yaml:"keep"`
	KeepPrefix int    `yaml:"keep_prefix"`
	KeepSuffix int    `yaml:"keep_suffix"`
	// Note はなぜこの列を隠すのかを共有ファイルに残すための欄。
	Note string `yaml:"note"`
	// DataSources が空でなければ、そのデータソースにのみ追加適用される。
	DataSources []string `yaml:"data_sources"`
	// Origin はこのルールの由来（レイヤ・ファイル・ファイル内での位置）。
	// YAML には現れず、マージ時に config パッケージが設定する。
	//
	// internal/mask がルールの検証エラーを出すとき、和集合後の union index
	// ではなく「利用者が開くファイルの何番目か」を示せるようにするために持つ。
	Origin RuleOrigin `yaml:"-"`
}

// RuleOrigin はルール1件がどのレイヤのどのファイルの何番目に書かれたかを示す。
type RuleOrigin struct {
	layer Layer
	path  string
	// index はこのルールが由来するファイル内での0始まりの位置。
	index int
}

// String は "共有設定 (path) の masking.rules[N]" の形式で出どころを表す。
func (o RuleOrigin) String() string {
	origin := o.layer.String()
	if o.path != "" {
		origin = fmt.Sprintf("%s (%s)", origin, o.path)
	}
	return fmt.Sprintf("%s の masking.rules[%d]", origin, o.index)
}

// Output は出力形式。
type Output struct {
	Format Format `yaml:"format"`
}
