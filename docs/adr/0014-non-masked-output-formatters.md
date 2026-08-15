# ADR-0014: マスク不要なコマンドは `Render` を経由しない独立した formatter を持つ

- ステータス: Accepted
- 日付: 2026-08-15
- 関連: [ADR-0001](./0001-layered-cli-architecture.md), [ADR-0004](./0004-output-formats.md)

## コンテキスト

[ADR-0004](./0004-output-formats.md) は `internal/output.Render(out, errW, format, res *redash.Result, sum mask.Summary, tty bool) error`
を唯一の出力経路として定め、stdout にデータのみ・stderr にマスクサマリという分離を
シグネチャのレベルで強制していた（§5）。これは `query` のようにマスク対象の
ユーザーデータを返すコマンドを前提にした設計である。

Issue #33（`sumiq data-sources list`）で、Redash 上のデータソース一覧
（id/name/type/paused）を出力するコマンドを追加するにあたり、この前提が
成り立たないケースに当たった。

- データソース一覧はマスク対象のユーザーデータではない。`Masked: --\nDropped: --\nRows: N`
  という文言をそのまま出すと、利用者にとって無関係な情報になる
- `Render` を呼ぶには空の `mask.Summary{}` を渡す必要があり、マスクと無関係な
  コマンドが `internal/mask` の型に依存することになる
- `redash.Column.Type` は「Redash がクエリ結果から推定した型」という前提の
  ドキュメントが付いており、データソースの `type`（`pg`/`mysql` 等）を
  同じフィールド名に詰めると意味の違う値が混ざる

`Render` のシグネチャを変えて `mask.Summary` を省略可能にする案（例: `*mask.Summary`
を受けて `nil` ならマスクサマリを書かない）も検討したが、`query` が依存し
ADR-0004 §5 で仕様化された「stdout はデータのみ・マスクサマリは必ず3行」という
契約に触る変更になり、本 Issue のスコープを超える。

## 決定

`internal/output` に、`Render` とは独立した `RenderDataSources(out io.Writer, format Format, ds []redash.DataSource, tty bool) error`
を追加する。

- `mask.Summary` を引数に取らない。`errW` も取らない
- table/json/csv の3形式は ADR-0004 の規則（0件の扱い・NULL の表現方針等）に揃えるが、
  マスクサマリの stderr 出力は行わない
- tabwriter の初期化など、`Render` 側と共通化できる実装の一部（`newTabwriter`）は
  ヘルパー関数として共有する。ただし公開関数としての入口は分ける

今後、マスク対象ではない一覧系コマンド（例: 将来の `data-sources show`）を
追加する場合も、この `RenderDataSources` と同じ形（`mask.Summary` を持たない
専用の `Render*` 関数）に倣う。

## 結果

**得られるもの**

- マスクと無関係なコマンドが `internal/mask` の型に依存しなくなる
- 出力に無関係なマスクサマリの文言が混ざらない
- `redash.Column.Type`（クエリ結果の推定型）と、データソースの `type`
  （接続先の種類）という意味の異なる値を同じ構造体・フィールド名で
  扱わずに済む

**支払うコスト**

- `internal/output` の公開入口が `Render` と `RenderDataSources` の2つに増える。
  ADR-0004 §5 が前提にしていた「出力は必ず `Render` を通り、必ずマスクサマリが
  付随する」という単一経路の保証は、パッケージ全体では成り立たなくなった。
  保証が及ぶ範囲は「`redash.Result` を返すコマンド（`query` 系）」に限定される
- table 形式のセル整形・0件時の扱いなど、ADR-0004 の規則を守る責務が
  2つの実装（`renderTable`/`renderDataSourcesTable` 等）に分かれ、
  仕様変更時は両方を確認する必要がある
- 今後マスク不要な出力コマンドが増えるたびに `Render*` 関数が増える。
  共通化できる部分（tabwriter 初期化等）は都度ヘルパーに括り出す判断が要る

## 未決事項

- マスク不要な出力コマンドが3つ以上に増えた場合、`Render` と `RenderDataSources`
  に共通する「列名 + セルの並びを form に落とす」部分をより一般化した
  共通関数に括り出すか（現時点では2種類しかなく時期尚早と判断）

## 参考

- [Issue #33 — internal/redash + internal/cli: データソース一覧取得サブコマンド (data-sources list)](https://github.com/otama-jaccy/sumiq/issues/33)（本 ADR はこの Issue の「決定3」を ADR として記録したもの）
- [PR #34](https://github.com/otama-jaccy/sumiq/pull/34)
