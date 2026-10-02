# ADR-0017: `--save` で SQL を Redash の draft 保存クエリにし、人間が生の結果を画面で見られるようにする

- ステータス: Accepted
- 日付: 2026-10-02
- 関連: [ADR-0003](./0003-config-file-design.md), [ADR-0004](./0004-output-formats.md), [ADR-0008](./0008-redash-client-error-classification.md), Issue #47

## コンテキスト

`sumiq query` は ad-hoc クエリ（`POST /api/query_results`）として実行しており、
Redash 上に保存クエリが残らない。エージェントにはマスク済みの結果だけを渡したいが、
人間は同じ SQL の結果をマスク無しで確かめたいことがある。そのたびに SQL を
コピーして Redash に貼り直す手作業が要っていた。

sumiq 自身が生データを出す案（`--unmasked`、ファイル出力など）は採らない。
エージェントも同じコマンドを叩けるうえ、Claude Code の `!` 実行では出力が
そのまま会話に入る。生の値を見せる場所は、sumiq の外（Redash の画面）に置く。

## 決定

### 1. 保存はオプトイン（`--save`）にする

既定では保存しない。毎回保存すると Redash がクエリで溢れ、他の利用者の一覧も
汚れる。`--save` 無しでは保存系のリクエストを一切送らない。

### 2. 保存クエリは draft で作り、`sumiq` タグを付ける

- `POST /api/queries` に `is_draft: true` と `tags: ["sumiq"]` を渡す
- 作成時の `is_draft` を無視して公開状態で作る Redash がある
  （[Discourse](https://discuss.redash.io/t/api-for-importing-queries-doesnt-respect-is-draft-and-seems-to-lack-update-ability/1808)）。
  応答の `is_draft` が `false` なら `POST /api/queries/{id}` で `is_draft: true` に更新する。
  現行の `QueryListResource.post` は `is_draft` を `True` に固定しているため、
  更新が要るのは古い版だけ
- 作成後の更新が失敗・打ち切りになっても、作成済みのクエリの URL をエラーに残す。
  この段の打ち切りは専用の段（`PhaseMarkDraft`）として報告し、「作られたかどうか分からない」とは言わない
- 後片付けは人間が `sumiq` タグで絞ってアーカイブする。sumiq は削除もアーカイブもしない

### 3. 重複キーは `sha256(data_source_id + "\n" + SQL)` の先頭 12 桁とし、名前に入れる

クエリ名を `sumiq: <hash>` にする。SQL の空白差は正規化しない。正規化を誤って
別の SQL を同一視するより、重複した保存クエリができる方が安全なため。

### 4. 重複検出は `GET /api/queries/my?tags=sumiq` を新しい順に読み、手元で完全一致を取る

Issue #47 の未決事項 1 への回答。Redash のソースを確認して決めた。

- `/api/queries/my` は `Query.by_user` で、自分のクエリのうちアーカイブ済みを除いたものを返す。
  自分の draft も含まれる。人間がアーカイブしたクエリは再利用せず作り直す
- 名前・SQL・`data_source_id`・`options.apply_auto_limit`・タグがすべて一致したものだけを再利用する。
  SQL まで比べるのは、人間が画面で SQL を書き換えた保存クエリを、別の SQL の URL として返さないため
- `page_size` は paginate の上限の 250。`count` を超えるページは読まない（範囲外は 400）。
  読むのは最大 4 ページ（1,000 件）までで、それを超えたら見つからなかったものとして作り直す

採らなかった案:

- **全文検索（`?q=<hash>`）で引く。** `search_vector` は tsvector（`TSVectorType`）で、
  名前に入れた 16 進のハッシュが 1 語として索引されるとは限らない。さらに組織設定
  `multi_byte_search_enabled` が有効だと、照合方法が name / description への `ilike` に変わる。
  外れると重複が増えるだけで誤りにはならないが、Redash の設定次第で挙動が変わる経路には頼らない
- **`/api/queries?tags=sumiq` で引く。** 他人の保存クエリも返る。他人のクエリは編集できず、
  再利用すると draft の扱いも持ち主の判断になるため、自分のものに限る

### 5. 結果の取得は ad-hoc のままにし、保存は「URL を作るため」だけの追加ステップにする

- 保存は `client.Execute` が成功し、`engine.Apply` と出力まで済んだ後に行う。
  実行に失敗した SQL や、`alias_guard: strict` で止まった SQL は保存しない
- 保存に失敗しても、マスク済みの結果は出力済み。失敗はエラーとして返し、
  終了コードを非 0 にする（成功したように見せない）
- 保存クエリの `options.apply_auto_limit` には、ad-hoc 実行に渡したのと同じ値を入れる。
  `Query.query_hash` は `options.apply_auto_limit` を適用した SQL から作られ
  （`Query.update_query_hash`）、作成時には同じハッシュとデータソースの最新結果が紐付く
  （`QueryListResource.post` の `update_latest_result_by_query_hash`）。
  値を揃えておけば、人間が画面で Execute したときも、エージェントと同じ LIMIT で実行される

### 6. URL は stderr に出す

`Saved: <endpoint>/queries/<id> (draft)` をマスクサマリの後ろに出す。
再利用したときは `(draft, 既存を再利用)`、再利用したクエリが公開済みなら
`(公開済み, 既存を再利用)` と出す。stdout はデータだけという
[ADR-0004](./0004-output-formats.md) の方針は変えない。

### 7. この機能はセキュリティ境界ではない

README の「sumiq is not a security boundary」は変わらない。API キーを持つ
エージェントは URL から `/api/queries/{id}/results` を直接叩ける。この機能は
事故防止と人間の手間削減のためのものであり、アクセス制御ではない。

## 結果

### 得られるもの

- エージェントにはマスク済みの結果と URL だけが渡り、人間は URL を開けば生の結果を見られる
- 誰が見られるかの判断を Redash の権限（グループ × データソース）に委ねられる。
  sumiq 側に権限モデルを持たずに済む
- 同じ SQL を何度実行しても、保存クエリは 1 つにまとまる

### 支払うコスト

- **`--save` 1 回あたり、一覧の取得が最大 4 往復増える。** 自分の `sumiq` タグ付きクエリが
  1,000 件を超えると、古いクエリは見つからず重複が増える。人間がタグで掃除する前提に依存する
- **保存には、データソースへの書き込み権限が要る。** 閲覧のみのデータソースでは
  `require_access(..., not_view_only)` が 403 を返し、保存できない。結果は出るが終了コードは非 0 になる
- **draft でも、URL を知っていてそのデータソースのグループに属していれば他人も見られる。**
  共有したい場面もあるため制限はしない。URL を貼る先には注意が要る
- **ad-hoc の結果が保存クエリに紐付く保証はない。** 紐付くのは `query_hash` が一致した場合だけで、
  保存クエリ側でパラメータを使う場合などはずれる。そのときは人間が Execute する必要があり、
  重いクエリでは二重に実行される
- 空白の違いだけで別の保存クエリができる

## 未決事項

1. **ad-hoc の結果が実機で保存クエリに紐付くか。** ソース上は `query_hash` が一致すれば紐付くが、
   実機の Redash では確認していない。紐付かず二重実行が問題になるようなら、
   保存クエリ経由で実行する方式（`POST /api/queries/{id}/results`）を別 Issue で検討する
2. **再利用したクエリの最新結果も更新されるか。** 結果の保存時に同じハッシュのクエリへ
   最新結果を付け直す `Query.update_latest_result` があるが、ad-hoc 実行の経路で
   呼ばれるかどうかは確認していない

## 参考

- Redash `redash/handlers/queries.py`（`QueryListResource.post` / `MyQueriesResource.get` / `QueryResource.post`）:
  https://github.com/getredash/redash/blob/master/redash/handlers/queries.py
- Redash `redash/models/__init__.py`（`Query.by_user` / `Query.search` / `search_vector` / `update_query_hash` / `update_latest_result_by_query_hash`）:
  https://github.com/getredash/redash/blob/master/redash/models/__init__.py
- Redash `redash/handlers/base.py`（`paginate` / `filter_by_tags`）:
  https://github.com/getredash/redash/blob/master/redash/handlers/base.py
- Redash `redash/serializers/__init__.py`（`serialize_query`）:
  https://github.com/getredash/redash/blob/master/redash/serializers/__init__.py
- Redash Discourse「API for importing queries doesn't respect is_draft」:
  https://discuss.redash.io/t/api-for-importing-queries-doesnt-respect-is-draft-and-seems-to-lack-update-ability/1808
