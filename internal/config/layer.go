package config

// Layer は設定値の出どころ。値が大きいほど後から読まれ、スカラーの上書きで勝つ。
//
// 順序は ADR-0003 §2 の探索順そのもの。
//
//  1. 埋め込みデフォルト
//  2. ~/.config/sumiq/config.yaml
//  3. <repo>/sumiq.yaml          共有（コミットされる）
//  4. <repo>/sumiq.local.yaml    ローカル（gitignore）
//  5. 環境変数 SUMIQ_*
//  6. コマンドライン引数
//
// 6 は internal/cli 側で組み立てるためこのパッケージでは扱わないが、
// レイヤの強弱を1か所で定義するために定数だけ置いている。
type Layer int

const (
	// LayerDefault は埋め込みデフォルト。
	LayerDefault Layer = iota
	// LayerUser は ~/.config/sumiq/config.yaml。ユーザの持ち物でありレビューされない。
	LayerUser
	// LayerShared は <repo>/sumiq.yaml。コミットされ、レビューされる唯一のファイル。
	LayerShared
	// LayerLocal は <repo>/sumiq.local.yaml。gitignore されるためレビューされない。
	LayerLocal
	// LayerExplicit は --config で明示指定されたファイル。
	//
	// 探索（2〜4）を丸ごと置き換えるものであり、共有ファイルを直接指す使い方
	// （CI から --config ./sumiq.yaml を渡す等）が主用途になる。そのため
	// LayerShared と同じ信頼度、すなわちレビュー済みとして扱う。
	// ADR-0003 の前提どおりこれはセキュリティ境界ではなく、防いでいるのは
	// 事故であって、自分で書いたパスを渡す利用者ではない。
	LayerExplicit
	// LayerEnv は環境変数 SUMIQ_*。
	LayerEnv
	// LayerFlag はコマンドライン引数。internal/cli が組み立てる。
	LayerFlag
)

// Reviewed はこのレイヤが git 上のレビュー対象かを返す。
//
// レビューされないレイヤには、マスクを弱める方向の指定を書かせない。
// どの制約が効くかの判断はすべてこの1つの述語に集約する。
func (l Layer) Reviewed() bool {
	return l == LayerShared || l == LayerExplicit
}

func (l Layer) String() string {
	switch l {
	case LayerDefault:
		return "埋め込みデフォルト"
	case LayerUser:
		return "ユーザ設定"
	case LayerShared:
		return "共有設定"
	case LayerLocal:
		return "ローカル設定"
	case LayerExplicit:
		return "--config 指定"
	case LayerEnv:
		return "環境変数"
	case LayerFlag:
		return "コマンドライン引数"
	}
	return "不明なレイヤ"
}
