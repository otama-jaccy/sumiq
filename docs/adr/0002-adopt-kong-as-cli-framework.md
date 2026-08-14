# ADR-0002: CLI フレームワークに kong を採用する

- ステータス: Accepted
- 日付: 2026-08-14
- 関連: [ADR-0001](./0001-layered-cli-architecture.md)

## コンテキスト

ADR-0001 で CLI フレームワークを `internal/cli` に閉じ込める方針を決めた。その `internal/cli` で何を使うかを決める必要がある。

候補は以下。

| 候補 | 特徴 |
| --- | --- |
| 標準 `flag` | 依存ゼロ。サブコマンドは自前実装 |
| [spf13/cobra](https://github.com/spf13/cobra) | デファクト。kubectl / gh / docker / hugo で採用。35k+ stars |
| [urfave/cli](https://github.com/urfave/cli) | ミニマルで composable。20k+ stars |
| [alecthomas/kong](https://github.com/alecthomas/kong) | 構造体タグで宣言的にコマンド構造を定義 |

`sumiq` は現時点でサブコマンドを持つ想定であり、標準 `flag` だけでは階層の自前実装が必要になるため対象外とした。実質 cobra と kong の比較になる。

## 決定

**kong を採用する。**

`internal/cli` にタグ付き構造体と `Run()` メソッドを置き、`Run()` は引数を詰め替えて `internal/app` を呼ぶだけに保つ。

```go
// internal/cli/root.go
type CLI struct {
    Verbose bool    `help:"詳細ログを出力する"`
    Add     AddCmd  `cmd:"" help:"追加する"`
    List    ListCmd `cmd:"" help:"一覧表示する"`
}

// internal/cli/add.go
type AddCmd struct {
    Name string `arg:"" help:"対象の名前"`
}

func (c *AddCmd) Run(deps *app.Deps) error {
    return app.Add(deps, c.Name)   // ロジックは持たない
}
```

依存は `kong.Parse()` 後に `ctx.Run(deps)` で注入する。`internal/app` 側は kong を一切知らない。

## 根拠

**1. ボイラープレートが実際に少ない**

cobra はフラグごとに変数を宣言し、型別の `XxxVar` で紐付け、`Run` で読む、という 3 箇所の対応関係を手で維持する必要がある。kong は構造体フィールドがそのままインターフェース仕様になり、対応関係が 1 箇所に収まる。

**2. サブコマンド追加のコストが最小**

kong は選択されたコマンドノードから root まで遡って `Run()` を呼ぶ。cobra の `AddCommand()` によるツリー組み立てが不要で、構造体のネストがそのままコマンド階層になる。サブコマンド追加はフィールド 1 行。

**3. ADR-0001 のレイヤ構成と噛み合う**

`Run()` メソッドがコマンド構造体に付くため、「コマンド定義とその入口」が 1 ファイルに自然に収まる。`kong.Bind()` / `ctx.Run(deps)` による依存注入は、GitHub CLI の `NewCmdXxx(f *cmdutil.Factory, ...)` パターンと実質同じことを、Factory 構造体を明示的に引き回さずに実現できる。

**4. cobra の主な批判は本体ではなくジェネレータ由来だが、それを避ける知識が前提になる**

「cobra は肥大で書きにくい」という評の多くは `cobra-cli` が生成するコード（パッケージレベル変数 + `init()` へのフラグ登録）に向いている。これは cobra の必然ではなく、`NewCmdXxx(f *Factory, runF func(*Options) error) *cobra.Command` の形にすればグローバル変数はゼロにできる。

つまり cobra を安全に使うには「正しい書き方を知っていて、ジェネレータを使わない」という規律が要る。kong はその落とし穴が構造的に存在しない。個人〜小規模チームのツールでは、規律に依存しない方を選ぶ。

**5. 想定していた懸念は解消済み**

kong の「`-1` などの負数をフラグと誤認する」問題（[issue #315](https://github.com/alecthomas/kong/issues/315), #478）は **v1.11.0** で修正され、ハイフン始まりのフラグ値のパースがオプトインで可能になった。この点を理由に kong を避ける必要はない。

## 検討したが採用しなかった選択肢

**cobra** — エコシステムは最強だが、上記 4 の規律が前提になる。また `sumiq` の現時点の要件では cobra 固有の強み（後述の shell 補完・man 生成）が効かない。

**urfave/cli** — cobra と kong の中間で、明確に選ぶ決め手がなかった。

**標準 `flag`** — サブコマンド階層の自前実装コストが要件に見合わない。

## 結果

**得られるもの**

- コマンド追加のコストが低く、CLI 仕様が構造体定義として 1 箇所で読める
- `env:""` / XOR グループ / `embed:""` による共通フラグ群の使い回しがタグで完結し、viper 等の追加依存や手書きバリデーションが不要
- グローバル変数を持たない形が自然に導かれる

**支払うコスト・受け入れるリスク**

- **タグは型のない文字列 DSL であり、検証は実行時。** `requred` のようなタイポはコンパイルを通り、`kong.Parse()` で panic する。起動時に必ず落ちるので発見は早いが、コンパイラのチェックは効かない
- **`Bind()` は型ベースのリフレクション解決。** 同じ型を複数 bind すると暗黙の挙動になる。依存の出所が cobra + 明示 Factory より追いにくい
- **shell 補完・man 生成が本体に同梱されていない。** cobra は bash/zsh/fish/powershell の補完と `cobra/doc` を同梱するが、kong は [kongplete](https://pkg.go.dev/github.com/willabides/kongplete) / [kong-completion](https://github.com/jotaen/kong-completion) / [king](https://github.com/miekg/king) / [komplete](https://abhinav.github.io/komplete/) から選ぶ。4 つ乱立している = 決定版がない
- **事例・情報量が cobra より少ない。** 詰まったときに参照できる実装が限られる
- フラグを動的に組み立てる要件が出た場合、宣言的な構造と相性が悪い（`kong.DynamicCommand()` はあるが素直ではない）

**リスク緩和**

ADR-0001 の方針により、kong への依存は `internal/cli` に閉じている。`Run()` にロジックを書かず詰め替えのみに保つ限り、フレームワーク差し替えの影響範囲は 1 パッケージに収まる。この不変条件をレビューで守る。

## 再検討のトリガー

以下のいずれかが発生した時点でこの決定を見直す。

- `sumiq` を社外・不特定多数に配布することになり、shell 補完が要件になった
- man ページ生成が必要になった
- フラグ/コマンドの動的生成が要件になった
- kong 起因の不具合や、上流のメンテナンス停滞が実害を出した

いずれも該当時は cobra を第一候補として再評価する。

## 参考

- [alecthomas/kong](https://github.com/alecthomas/kong)
- [Cobra and Kong CLI comparison（コード対比）](https://gist.github.com/andreykaipov/3006701e3ee57df397db827b18716b45)
- [Kong is an amazing CLI for Go apps — Daniel Michaels](https://danielms.site/zet/2023/kong-is-an-amazing-cli-for-go-apps/)
- [HN: Cobra の edge case と各ライブラリの評価](https://news.ycombinator.com/item?id=43453459)
- [kong issue #43 — Shell completions](https://github.com/alecthomas/kong/issues/43)
- [cli/cli AGENTS.md — cobra を安全に使う場合の参照実装](https://github.com/cli/cli/blob/trunk/AGENTS.md)
