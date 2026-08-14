# ADR-0012: ポーリング中の一時的な失敗をリトライする

- ステータス: Accepted
- 日付: 2026-08-15
- 関連: [ADR-0008](./0008-redash-client-error-classification.md)（Redash クライアントのエラー分類）、[ADR-0003](./0003-config-file-design.md)（`timeout` / `poll_interval` の設定）

## コンテキスト

[Issue #3](https://github.com/otama-jaccy/sumiq/issues/3)（PR #14）のセルフレビューで出た指摘を、[Issue #15](https://github.com/otama-jaccy/sumiq/issues/15) として切り出した。

`Client.wait` は `GET /api/jobs/{id}` が1回でも失敗すると `Execute` 全体を失敗させる。ADR-0003 §3 の既定（`timeout: 300s` / `poll_interval: 1s`）では最大 300 回ポーリングするため、途中の1回が

- LB / nginx の一時的な 502・503
- アイドルコネクションのリセット
- DNS の一時的な失敗
- 429（レート制限）

で落ちると、**Redash 側で正常に走っているジョブを捨てて**コマンドが失敗する。利用者は同じ重いクエリを投げ直すことになり、その間も元のジョブが Redash のワーカーを占有し続ける。

### なぜポーリングだけ別扱いにできるか（ADR-0008 §「参考」より）

- `GET /api/jobs/{id}` は冪等
- ジョブ ID は検証済みで手元にある
- ジョブは中断しても Redash 側で走り続ける（ADR-0008 の `TimeoutError` の文言もそう説明している）

投入（`POST /api/query_results`）は事情が違う。POST は冪等ではなく、応答が届く前に接続が切れた場合、リクエストが Redash に届いてジョブが積まれた可能性を排除できない。ここで安易に再送すると、同じクエリのジョブが二重に走る恐れがある。

### net/http 自体が持つ自動再送との違い

`net/http` の `Transport` は、**再利用した持続的接続**でネットワークエラーが起きた冪等リクエストを、こちらのコードを介さずに自動で1回だけ再送する（[`persistConn.shouldRetryRequest`](https://cs.opensource.google/go/go/+/refs/tags/go1.25.5:src/net/http/transport.go;l=808) の `!pc.isReused()` の分岐。新規接続では再送しない）。この挙動は「サーバの応答がまだ返っていない」ケースに限られ、**「サーバが 5xx や 429 を返した」ケースは対象にしない**（RoundTrip がエラーなく応答を返しているため）。したがって HTTP レベルのエラー応答に対する再試行は、こちら側で明示的に実装する必要がある。

## 決めること（Issue が挙げたもの）

1. 何回まで許すか（連続失敗数 / 締切まで無条件に再試行）
2. どのエラーを対象にするか
3. バックオフを入れるか、`poll_interval` のままか
4. 投入・取得にも適用するか
5. 再試行したことを利用者に見せるか

## 決定

### 1. 対象は `wait`（`GET /api/jobs/{id}`）のみ

`submit`（POST）には適用しない。冪等でないため、応答が届く前の失敗を再送すると二重投入になりうる。

`fetch`（`GET /api/query_results/{id}`）にも今回は適用しない。`GET` である以上ポーリングと同じ理屈は成り立つが、Issue #15 のタイトルとスコープはポーリングに限定されており、`fetch` 特有の懸念（結果を読み取り始めた後の失敗をどう扱うか等）を検討せずに広げるとスコープが曖昧になる。必要になれば別 Issue で判断する。

### 2. 対象エラーは「接続そのものの失敗」「429」「5xx」

- 接続そのものの失敗（DNS・タイムアウト・コネクションリセット等）
- 429（レート制限）
- 5xx の `APIError`

対象外（即座に失敗させる）:

- `AuthError`（401/403、および 404 のうち未認証扱いのもの）— API KEY や権限の問題は再試行しても直らない
- 429・5xx 以外の `APIError`（400 等）— リクエスト自体の問題であり、再試行しても直らない
- ジョブ自体の失敗（`finished` が返す `JobError`）— クエリの失敗そのものであり、再試行の対象ではない

429 を対象にしたのは、RFC 6585 が定義する意味が「レート制限であり、一定時間後に再試行できる」ことを踏まえた（[RFC 6585 §4](https://www.rfc-editor.org/rfc/rfc6585.html) “The 429 status code indicates that the user has sent too many requests in a given amount of time”）。LB の瞬断と同様、時間を置けば回復する一時的な障害として扱える。

なお RFC は 429 の応答に `Retry-After` を含めてよいとしているが、これは尊重しない（§3 で後述）。

### 3. リトライ回数に専用の上限は設けず、`Execute` の `timeout` まで続ける

連続失敗数のような別カウンタを持たない。理由は2つ。

- `timeout` はすでに `Execute` 全体（投入・ポーリング・取得の合計）の上限として機能している。別の上限を追加すると、「`timeout` はまだ残っているのにリトライ上限で失敗する」ケースが生まれ、利用者から見た失敗理由の変数が増える
- 対象にしているのは元々「300 回中の1回が失敗する」程度の単発の一時障害であり、持続的な障害はどのみち `timeout` で打ち切られる。専用の上限は実質的な安全性を追加しない

打ち切られた場合は通常の `TimeoutError`（Phase: wait）として報告される。「一時的な障害からのリトライに時間を使った」ことは示さない（§5）。

### 4. バックオフは入れず、`poll_interval` のまま再試行する

`Retry-After` ヘッダーも見ない。理由は3つ。

- 対象にしているのは単発の一時障害であり、指数バックオフが効果を発揮する「持続的な輻輳」を想定していない
- `poll_interval`（既定 1s）はすでに Redash への負荷を抑えた値であり、失敗時だけ間隔を変えると「成功時と失敗時で負荷プロファイルが変わる」複雑さが増える
- Redash 自身の応答（ADR-0008 で確認した `error_response` の形）に `Retry-After` は含まれない。汎用プロキシ由来の 429 では付くことがありうるが、それを解釈する経路を新設するコストに見合わない

### 5. 再試行したことは利用者に見せない

`internal/redash` は `io.Writer` を持たない設計（[ADR-0001](./0001-layered-cli-architecture.md) のレイヤ構成）で、進捗表示やログはこのパッケージの責務ではない。ここに観測性を持ち込むと、`internal/cli` を経由しない出力経路を作ることになり、境界が崩れる。

最終的に `timeout` まで再試行しても終わらなかった場合は、通常の `TimeoutError`（Phase: wait、「ジョブは Redash 側で実行され続けます」の文言）として利用者に伝わる。個々のリトライの発生は伝えないが、「諦めていない」ことの結果は伝わる。

## 実装

- 判定は `isRetryableWaitErr`（`internal/redash/execute.go`）が持つ。接続失敗は `connectError` 型（`do` が返す）を `errors.As` で判定し、HTTP エラーは `*APIError` の `StatusCode` を見る
- リトライは `wait` から切り出した `pollNext` が行う。`sleep(poll_interval)` → `jobStatus` の1サイクルを、対象エラーである限り繰り返す。`ctx`（`Execute` が設定した `execCtx`）のキャンセルで抜ける

## 結果

**得られるもの**

- LB の瞬断・DNS の一時的な失敗・429・5xx で、Redash 側では正常に走っているジョブを捨てなくなる
- 追加の設定項目を増やさない（`timeout` / `poll_interval` の意味は変わらない）

**支払うコスト・受け入れる制約**

- **`fetch` は対象外のまま。** `GET /api/query_results/{id}` の一時的な失敗は引き続きジョブを諦める形になる
- **`Retry-After` を無視する。** 汎用プロキシが 429 に妥当な待機時間を示していても従わず、`poll_interval` で機械的に再試行する
- **個々のリトライは可視化されない。** 何度再試行したかはログに残らず、`timeout` に達して初めて失敗として気づく
- **持続的な障害と一時的な障害を区別しない。** 5xx が延々と続く場合も `timeout` まで同じ間隔で叩き続ける。バックオフが無いため、障害の間 Redash への負荷は通常運転時と同じペースで続く

## 未決事項

- `fetch` にも同じ方針を適用するか（本 ADR の対象外とした）
- `GET` の 2xx 応答を受け取った後、本文の読み取り中（`json.Decoder.Decode`）に接続が切れた場合の扱い。現状は非対象（即座に失敗）。`encoding/json` がこの種の I/O エラーをどう伝播するかは公式に保証された挙動ではなく、信頼できる判定を作るには `do` を本文読み取りとデコードに分離するなど、`submit`/`wait`/`fetch` すべてに影響する構造変更が要る

## 参考

- [redash/handlers/query_results.py](https://github.com/getredash/redash/blob/master/redash/handlers/query_results.py) — `error_response` の形（ADR-0008 で確認済み）
- [RFC 6585 §4 — 429 Too Many Requests](https://www.rfc-editor.org/rfc/rfc6585.html) — 429 の定義と `Retry-After` の扱い
- [net/http の `persistConn.shouldRetryRequest`](https://cs.opensource.google/go/go/+/refs/tags/go1.25.5:src/net/http/transport.go;l=808) — Go 標準ライブラリが自動で再送する条件（新規接続では再送しない）
