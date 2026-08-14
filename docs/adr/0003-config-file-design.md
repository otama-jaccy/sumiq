# ADR-0003: 設定ファイルの設計

- ステータス: Accepted
- 日付: 2026-08-14
- 関連: [ADR-0001](./0001-layered-cli-architecture.md), [ADR-0002](./0002-adopt-kong-as-cli-framework.md)

## コンテキスト

`sumiq` は「設定ファイルを読み、引数で渡された SQL を Redash 経由で実行し、設定に応じて結果をマスクして出力する」CLI である。

設定できる必要があるもの:

- Redash のエンドポイント
- API KEY
- マスク対象のカラムパターンとマスク方法
- 取得する最大行数

加えて、**リポジトリ共有用とローカル用の複数ファイルを読めること**が要件。API KEY のような秘密情報と、チームでレビューすべきマスク方針を、同じファイルに置くことはできない。

### Redash API の制約

ad-hoc クエリは `POST /api/query_results` に `query` / `data_source_id` / `max_age` / `apply_auto_limit` を渡し、返ったジョブ ID を `/api/jobs/{id}` でポーリングし、完了後に結果を取得する3段構え。ここから2つの帰結がある。

1. **`data_source_id` の指定が必須。** どのデータソースに対して実行するかを決める仕組みが要る。
2. **ジョブ待ちのタイムアウトとポーリング間隔が設定項目になる。**

また、UI の「LIMIT 1000」トグルに相当する `apply_auto_limit` は API からも指定できるが、安全装置としては使えない。

```python
# BaseQueryRunner
limit_query = " LIMIT 1000"

# BaseSQLQueryRunner
def apply_auto_limit(self, query_text, should_apply_auto_limit):
    queries = split_sql_statements(query_text)
    if should_apply_auto_limit:
        last_query = queries[-1]
        if self.query_is_select_no_limit(last_query):
            queries[-1] = self.add_limit_to_query(last_query)
    return combine_sql_statements(queries)
```

- 行数は **1000 固定**。API から数値を渡せない
- 最後の1文が「LIMIT なしの SELECT」のときだけ付く。CTE を含むクエリでは効かない報告がある
- `supports_auto_limit` は `BaseQueryRunner` で `False`。SQL 系以外のデータソースでは黙って no-op
- Oracle / SQL Server では逆にクエリが壊れる既知問題がある

### 前提: これはセキュリティ境界ではない

利用者は Redash の API KEY を保持しており、`curl` を直接叩けば `sumiq` の設定を迂回して任意のデータソースを引ける。**`sumiq` の設定に強制力はない。**

したがって設定の目的は「アクセスを禁止すること」ではなく、以下の2点に置く。

1. **事故防止** — データソースの指定ミス、マスク漏れ、想定外の大量取得を防ぐ
2. **マスク方針をレビュー可能にすること** — 「どのデータソースのどの列をどう隠すか」を git 上のレビュー対象にする

## 決定

### 1. 形式は YAML

マスクルールがネストしたリストになること、および**共有ファイルに「なぜこの列を隠すのか」をコメントで残せること**を重視する。JSON はコメントを書けないため不可。

### 2. ファイルの探索順とマージ

```
1. 埋め込みデフォルト
2. ~/.config/sumiq/config.yaml    ユーザ全体
3. <repo>/sumiq.yaml              リポジトリ共有（コミットする）
4. <repo>/sumiq.local.yaml        ローカル（.gitignore に入れる）
5. 環境変数 SUMIQ_*
6. コマンドライン引数
```

下が勝つ。`--config` を明示した場合は 2〜4 の探索をスキップする。
3・4 はカレントディレクトリから git root まで遡って探索し、サブディレクトリからの実行に対応する。

ファイル名はドットなし（`sumiq.yaml`）とする。共有ファイルは読まれ、レビューされるべきチームの成果物であり、可視である方がよい。

スカラー値は上書き、リストは項目ごとの規則に従う（後述）。

### 3. 共有ファイルのスキーマ

```yaml
version: 1

redash:
  endpoint: https://redash.example.com
  timeout: 300s          # ジョブ完了までの待ち上限
  poll_interval: 1s
  # api_key はここに書かない

data_sources:
  - name: analytics
    id: 3
    description: "本番リードレプリカ"
    default_action: redact    # このデータソースは allowlist 運用
  - name: sandbox
    id: 7
    auto_limit: false         # データソース単位で上書き可

query:
  auto_limit: true
  max_rows: 1000
  on_exceed: error            # error | truncate

masking:
  default_action: none        # none | redact
  rules:
    - patterns: ["*email*"]
      method: partial
      keep: domain
      note: "ドメインのみ残す。流入元の切り分けに使うため"

    - patterns: ["*phone*", "*tel"]
      method: redact

    - patterns: ["user_id", "customer_id"]
      method: hash

    - patterns: ["regex:^(first|last)_name$"]
      method: drop

    - patterns: ["memo", "note"]
      method: redact
      data_sources: [analytics]   # 特定データソースにのみ追加適用
```

`patterns` は既定でグロブ（大文字小文字を無視）。`regex:` 接頭辞で正規表現に切り替える。

### 4. ローカルファイルのスキーマ

```yaml
version: 1

redash:
  api_key: ${env:REDASH_API_KEY}
  # api_key_command: ["op", "read", "op://Private/redash/credential"]

data_sources:
  - name: my-sandbox
    id: 99

masking:
  rules:
    - patterns: ["internal_memo"]
      method: redact
```

### 5. API KEY は共有ファイルに置けない構造にする

取得元は3経路を用意する。

1. 環境変数 `SUMIQ_REDASH_API_KEY`
2. ローカルファイルの `redash.api_key`（`${env:VAR}` 展開に対応）
3. `redash.api_key_command` による外部コマンド（1Password CLI、Keychain 等）

**ロード時に「git の管理下にあるファイル由来の `api_key`」を検出したらエラーで停止する。** `git ls-files --error-unmatch <path>` で判定できる。「書かないでください」という規約では事故るため、構造で防ぐ。

### 6. データソースは CLI 引数で指定し、設定に定義されたものだけを受け付ける

```bash
sumiq query -d analytics "SELECT id, email FROM users"
```

- **CLI は名前のみを受け付ける。数値 ID を直接渡すことはできない。** `-d 3` を許すと設定の定義を迂回できるため、名前解決のみとする
- 未定義の名前はエラー
- **デフォルトのデータソースは設けない。`-d` は必須。** 「省略したら本番に飛んだ」を構造的に潰す

**ローカルファイルからのデータソース追加は許可する。** ただし以下を伴う。

- ローカル定義のデータソースを使う実行では、毎回警告を出す
  （例: `Warning: my-sandbox はローカル定義です。マスク方針はレビューされていません。`）
- グローバルのマスクルールは必ず適用される
- ローカル定義の `default_action` はグローバル既定より緩くできない

許可リストの位置づけは「アクセス制御」ではなく「事故防止とレビュー範囲の明示」に留まる。上述のとおり `sumiq` に強制力はなく、厳格に禁止しても API KEY を直接使えば迂回できるため、実用性を優先する。

### 7. マスクルールのマージは単調（fail-closed）

マスクは安全装置であり、**ローカル設定で共有設定を弱められる構造にしてはならない。**

- ルールは全レイヤの**和集合**（上書きではなく追加）
- ローカルは**追加のみ可能。共有ルールの削除・弱化は不可**
- 1つの列に複数ルールがマッチしたら**最も強い方法が勝つ**

```
drop > redact > hash > partial > none
```

- `default_action` の上書きは**厳しくする方向のみ**許可する
- **`method: none`（明示的な許可）は共有ファイルにのみ書ける。** allowlist 運用（`default_action: redact`）で特定の列を通すための指定であり、ローカルから書けると弱化そのものになる
- マスク解除の逃げ道（`--unmask` 相当）は設けない

### 8. `default_action` のグローバル既定は `none`（denylist）

マッチした列だけをマスクする。データソース単位で `redact`（allowlist）に引き上げられるため、グローバルは緩く保つ。

新規カラムが素通りするリスクは残るが、機微なデータソースは個別に `redact` へ上げる運用で対応する。

### 9. マスク方法

| method | 挙動 |
| --- | --- |
| `redact` | `****` 固定文字列に置換 |
| `partial` | 一部を残す（`keep: domain` / `keep_prefix: N` / `keep_suffix: N`） |
| `hash` | `sha256(salt + value)` の先頭 N 文字 |
| `null` | `NULL` に置換（型を保ちたいとき） |
| `drop` | 列ごと出力しない |

**`hash` の salt は実行ごとにランダム生成する。** 1回の実行内では一貫するため件数集計や JOIN は可能だが、実行をまたいだ突き合わせはできない。

salt を共有ファイルに置くとコミットされ、`user_id` のような低カーディナリティ値は総当たりで復元できてしまう。かといってローカル固有の salt にすると人によって値が変わり、結果を突き合わせられない。どちらも中途半端であるため、**安全側に倒して「突き合わせは仕様上できない」と割り切る。**

### 10. `auto_limit` と `max_rows` は役割が異なる

- **`auto_limit`** — Redash に `apply_auto_limit` を渡す。効けばフルスキャン回避と転送量削減になる**最適化**。既定 `true`
- **`max_rows`** — 取得後にクライアント側で判定する**安全装置**。必ず効く

`auto_limit` は上述のとおり CTE や非 SQL データソースで黙って効かないため、単独では信頼できない。両方を持つ。

検証規則:

| 設定 | 挙動 |
| --- | --- |
| `auto_limit: true`, `max_rows: 1000` | 推奨 |
| `auto_limit: true`, `max_rows: > 1000` | **設定エラー。** サーバが 1000 で切るため到達不能 |
| `auto_limit: false`, `max_rows: N` | 全件転送後に判定。大量転送の可能性を警告 |

`on_exceed` の既定は **`error`**。黙って切り詰めると「1000 件で切られた結果を全件だと誤認する」事故が起きる。

Oracle / SQL Server は `apply_auto_limit` でクエリが壊れるため、該当するデータソースでは `auto_limit: false` を個別に指定する。データソース単位の上書きを用意しているのはこのため。

## 結果

**得られるもの**

- 秘密情報が共有ファイルに入らないことが構造的に保証される
- マスク方針が git 上のレビュー対象になり、ローカルから弱められない
- データソースの指定ミスと想定外の大量取得を、実行前・実行後の二段で防げる

**支払うコスト・受け入れる制約**

- ハッシュ値を実行をまたいで突き合わせられない。これは仕様として受け入れる
- 許可リストは強制力を持たない。API KEY を直接使えば迂回できる
- `auto_limit` が効かないケースがあり、その分は全件転送が発生する
- ローカル定義のデータソースは未レビューのマスク方針で動く。警告で可視化するに留まる
- マージ規則が項目ごとに異なる（スカラーは上書き、マスクルールは和集合、`method: none` は共有専用）ため、実装とドキュメントで明示が必要

## 未決事項

以下は本 ADR の範囲外とし、必要になった時点で決める。

- 出力形式（table / json / csv）とその既定値
- `partial` の `keep` オプションの詳細仕様
- 接続先が増えた場合のプロファイル機能（endpoint 自体を切り替える必要が出たとき）
- クエリ結果のキャッシュ（`max_age` の扱い）

## 参考

- [Redash API ドキュメント](https://redash.io/help/user-guide/integrations-and-api/api)
- [redash/handlers/query_results.py](https://github.com/getredash/redash/blob/master/redash/handlers/query_results.py)
- [redash/query_runner/\_\_init\_\_.py](https://github.com/getredash/redash/blob/master/redash/query_runner/__init__.py)
- [Writing a New Query Runner — Redash Docs](https://redash.io/help/open-source/dev-guide/write-a-query-runner/)
- [AUTO LIMIT not working with queries containing CTE](https://discuss.redash.io/t/auto-limit-not-working-with-queries-containing-cte/9487)
- [Auto limit of 1000 breaks Oracle queries #5180](https://github.com/getredash/redash/issues/5180)
