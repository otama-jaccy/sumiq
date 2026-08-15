# ADR-0013: fetch の取得を打ち切り可能にする

- ステータス: Accepted
- 日付: 2026-08-15
- 関連: [ADR-0003](./0003-config-file-design.md) §10、[ADR-0007](./0007-layered-merge-guards.md)、Issue #16、PR #36

## コンテキスト

Issue #16（#3 / PR #14 のセルフレビューで切り出したもの）が指摘した問題:

`Client.fetch` は `GET /api/query_results/{id}` の応答を丸ごとデコードして `Result` に載せていた。ADR-0003 §10 は `max_rows` を「取得後にクライアント側で判定する、必ず効く安全装置」と位置づけているが、これは判定に**辿り着けること**を前提にしている。`auto_limit: false`（Oracle / SQL Server では必須）で大きなテーブルを引くと、`rows` を全部メモリに載せる時点でプロセスが OOM で落ち、`max_rows` の判定そのものに辿り着かない。

Issue はあわせて次の制約も指摘していた。`Result` の形（`Columns []Column` + 全行の `[]Row`）は、Issue 起票の時点で #4（マスク）・#5（出力）・#6（行数ガード）が前提にする**予定**だった。「契約が固まる前に決めた方がよい」という含みだった。

しかし実装が先行して進んだ結果、本 ADR を書いている時点で #4・#5・#6 はすでに実装済みであり、3つとも現在の `Result` の形にすでに依存している。つまり「契約が固まる前に」という Issue の前提はすでに崩れている。本 ADR はその現実を踏まえた上での決定を記録する。

## 決定

### 1. `Result` は streaming にしない。行数だけ有限にする

`Result` は `Columns []Column` + `[]Row` という全行保持の形のまま変えない。真の streaming（呼び出し側がイテレータで1行ずつ受け取る形）への作り替えは、`mask` / `output` / `rowguard` の3パッケージすべての書き直しを要求する。Issue #16 の実害は「OOM で落ちる」ことであり、`Result` の形を変えなくても解決できる。作り替えは実害に対して不釣り合いに大きいと判断した。

代わりに、`Client.fetch`（`internal/redash/execute.go`・`result.go`）が応答本文を `encoding/json` の `Decoder.Token` で1件ずつ読み進め、`rows` を `RowLimit+1` 件までしか保持しないようにした。超えた要素は `json.RawMessage` として読み捨てる（`map[string]any` へ完全にデコードすると、保持しない行にまでネストした値の割り当てを払うことになるため）。

`+1` 件を保持するのは、呼び出し側（`rowguard.Check`）が既存の `len(Result.Rows) > max_rows` という比較のまま超過を判定できるようにするため。`rowguard` 側のロジックは無変更で済んだ。

`RowLimit` は `redash.Query` の bare な `int` フィールドとして持つ。`internal/redash` は `internal/config` を import しない制約があるため（ADR-0001）、`internal/app/query.go` が `resolved.Config.Query.MaxRows` を渡す。既存の `Query.AutoLimit bool` と同じパターン。

### 2. `rows` の上限に達しても、応答の読み取り自体は最後まで続ける

`rows` が `RowLimit+1` 件に達した時点で接続を切り、残りを読まずに済ませる案を検討したが、採用しなかった。

JSON オブジェクトのキー順（`columns` と `rows` のどちらが先に現れるか）は仕様として保証されていない。実測では Redash は `columns` を先に返すが、これはこの ADR を書く上で一次情報（Redash のソース）を確認して初めて言えることではなく、**確認していない。** 確認していないものに実装を依存させない。

もし `rows` 側で上限に達した時点で読むのをやめて接続を切る実装にした場合、その前提（`rows` が `columns` より後に来る）が崩れた応答では、まだ読んでいない `columns` に永遠に辿り着けないまま「`columns` がありません」という `toResult` の不整合チェックに引っかかり、正当な大きな結果を誤って拒否することになる。安全側に倒し、`rows` の要素は保持しない分も含めて JSON としては最後まで読み切ることにした。

### 3. 応答本文そのものにもバイト数の上限を置く

行数の上限は `rows` 配列の要素数だけを絞る。1行あたりの値が極端に大きい応答（巨大な1セル）や、`rows` 以外のフィールドが肥大化した応答までは防げない。行数と無関係にかける最後の防御線として、応答本文全体にバイト数の上限（`Options.MaxResponseBytes`、既定 `DefaultMaxResponseBytes = 512MiB`）を設けた。

実装は自前で持たず `net/http.MaxBytesReader` をそのまま使う（`internal/redash/limits.go`）。第1引数の `http.ResponseWriter` はサーバ側の接続クローズ通知にのみ使われ、クライアント側で `nil` を渡すのは安全な使い方であることを Go 本体のソース（`net/http/request.go` の `maxBytesReader.Read`、コメント "The server code and client code both use maxBytesReader."）を読んで確認した。上限超過は `*http.MaxBytesError` として `errors.As` で判定できる。

`GET /api/query_results/{id}`（`fetch`）に限らず、`POST /api/query_results`（投入）・`GET /api/jobs/{id}`（ポーリング）を含む全リクエストの応答に一律で適用する（`Client.roundTrip`）。行数の上限と独立な仕組みであり、`fetch` だけの特殊扱いにする理由がないため。

具体的な既定値（512MiB）に「これだけあれば十分」という根拠は無い。実運用でこの上限に当たる正当な結果が出た時点で、config への露出を含めて見直す。

### 4. `rowguard` の超過メッセージは「〜行あり」から「〜行以上あり」に変える

`fetch` が `max_rows+1` 件で打ち切った場合、`rowguard.ExceededError.Got` および `on_exceed: truncate` の警告に載る行数は、実際の総行数ではなく「少なくともこの数だけある」という観測値になる。これまでの「結果が %d 行あり」という言い切りは、打ち切りが起きたケースでは正確でなくなるため、「%d 行以上あり」に変えた。

## 結果

**得られるもの**

- `auto_limit: false` で巨大なテーブルを引いても、`rows` の保持は `max_rows+1` 件に収まり、`max_rows` の判定（`rowguard.Check`）に必ず辿り着けるようになった。ADR-0003 §10 が「必ず効く」としていた安全装置が、実際に必ず効くようになった。
- 行数と無関係な肥大化（巨大な1セル、`rows` 以外のフィールド）にも `MaxResponseBytes` が防御線になる。
- `mask` / `output` / `rowguard` の既存実装・既存テストは無変更で済んだ。

**支払うコスト・受け入れる制約**

- `rows` の上限を超えた場合、超過分もバイトとしては読み切る（決定2）。ネットワーク転送量は削減できない。メモリ使用量の削減のみ。
- `Result` は真の streaming ではないため、`max_rows` を極端に大きく設定した場合（例: `auto_limit: false` かつ `max_rows: 10_000_000`）、その件数分のメモリは依然として使う。上限を「メモリに載せてよい件数」として利用者が選ぶ責務は変わらない。
- `MaxResponseBytes` の既定値に実測に基づく根拠は無い。
- `decodeQueryResult` 系の手書きトークン走査は、`queryResult` 構造体タグが表す形を人手で重複して表現している。Go の `encoding/json` に部分 streaming デコードの標準機構が無いための現実的な妥協として受け入れる。
- 超過時のメッセージが「正確な行数」から「少なくともこの行数」に弱まる。

## 未決事項

- `MaxResponseBytes` を `sumiq.yaml` の設定項目として露出するか。実運用でこの上限に当たる正当な結果が出た時点で判断する。

## 参考

- [net/http.MaxBytesReader — pkg.go.dev](https://pkg.go.dev/net/http#MaxBytesReader)
- [net/http.MaxBytesError — pkg.go.dev](https://pkg.go.dev/net/http#MaxBytesError)
- Go 本体 `net/http/request.go` の `maxBytesReader.Read` 実装（`nil` `ResponseWriter` での client 側利用が安全であることの根拠。型アサーション `l.w.(requestTooLarger)` は `l.w == nil` でも panic せず `ok == false` になる）
- [encoding/json.Decoder.Token — pkg.go.dev](https://pkg.go.dev/encoding/json#Decoder.Token)
- [encoding/json.Decoder.More — pkg.go.dev](https://pkg.go.dev/encoding/json#Decoder.More)
