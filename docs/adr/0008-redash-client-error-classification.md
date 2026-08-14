# ADR-0008: Redash クライアントのエラー分類と応答の読み方

- ステータス: Accepted
- 日付: 2026-08-14
- 関連: [ADR-0003](./0003-config-file-design.md)（Redash API の制約）

## コンテキスト

[Issue #3](https://github.com/otama-jaccy/sumiq/issues/3) で `internal/redash` を実装するにあたり、Redash の実装を読んで確認したところ、**Issue の受け入れ条件が前提にしていた挙動と実際が食い違っていた**。

受け入れ条件は「401/403（KEY 不正・権限不足）、4xx、ジョブ失敗、SQL エラーをそれぞれ区別できるメッセージにする」と書かれていたが、この4つは Redash の応答からはそのままの形では区別できない。

以下はすべて Redash のソースで確認した（リンクは「参考」節）。

### 1. 不正な API KEY に返るのは 401 ではなく 404

`/api/` 配下のリソースは `BaseResource.decorators = [login_required]` を持つ。認証に失敗すると flask_login の `unauthorized_handler` が呼ばれ、`redirect_to_login` が返すのは **404** と `{"message": "Couldn't find resource. Please login and try again."}` である。

```python
@login_manager.unauthorized_handler
def redirect_to_login():
    is_xhr = request.headers.get("X-Requested-With") == "XMLHttpRequest"
    if is_xhr or "/api/" in request.path:
        return {"message": "Couldn't find resource. Please login and try again."}, 404
```

「API KEY が間違っている」に対して「見つかりません」と表示すると、利用者はエンドポイントやデータソース名を疑い、原因に辿り着けない。

### 2. 401 は認証の失敗を意味しない

逆に、`POST /api/query_results` が 401 を返すのは**データソース側の問題**のときである。

```python
error_messages = {
    "no_permission": error_response("You do not have permission to run queries with this data source.", 403),
    "select_data_source": error_response("Please select data source to run this query.", 401),
    "no_data_source": error_response("Target data source not available.", 401),
}
```

401 = 認証、という HTTP の通例が成り立っていない。

### 3. エラー応答もジョブと同じ形で返る

`error_response` は `{"job": {"status": 4, "error": message}}` を返す。つまり**本文の形では成功と失敗を区別できない**。`job` キーがあることは成功を意味しない。

### 4. ジョブ失敗と SQL エラーは応答上まったく同じ

`serialize_job` は3つの異なる原因を同じ形に畳む。

```python
if job.is_cancelled:
    error = "Query cancelled by user."; status = 4
elif isinstance(job.result, Exception):      # ワーカー側の例外
    error = str(job.result); status = 4
elif isinstance(job.result, dict) and "error" in job.result:   # クエリランナーが返したエラー = SQL エラー
    error = job.result["error"]; status = 4
```

いずれも `status: 4` + `error` 文字列で、**しかも HTTP 200 で返る**。型として分離する材料が応答に存在しない。

### 5. 列順は `columns` にしかない

結果の `data.rows` は列名をキーにしたオブジェクトの配列で、JSON のオブジェクトは順序を持たない。順序の情報は `data.columns` の並びにしかない。

なお `columns[].name` の一意性はサーバ側で保証されている。`fetch_columns` が重複を `name`, `name1`, `name2`... と振り直す。

## 決定

### 1. エラーは4つの型に分ける

| 型 | 対象 |
| --- | --- |
| `AuthError` | 401 / 403、および 404 のうち未認証の文言を含むもの |
| `APIError` | それ以外の 4xx / 5xx、プロキシからの非 JSON 応答 |
| `JobError` | ジョブの失敗。SQL エラーもここに入る |
| `TimeoutError` | `timeout` 以内に終わらなかった |

受け入れ条件が挙げた4分類とは対応しない。**応答に存在しない区別は作らない。**

### 2. 404 のうち未認証の文言を含むものを `AuthError` に倒す

判定は `Couldn't find resource` という文字列の一致で行う。文字列に依存する判定は本来避けたいが、ここでは他に手がかりが無い。

この文字列は Redash のソースにハードコードされた英語で、`gettext` を通っていない（バックエンドは i18n していない）ことを確認している。

外れた場合も `APIError` として Redash のメッセージ自体は表示されるため、**判定が壊れても情報は失われない。** 壊れ方が安全側であることを確認したうえで採用する。

### 3. ジョブ失敗と SQL エラーは1つの型にまとめ、Redash の文言をそのまま通す

`JobError.Message` に Redash の `error` を入れる。区別できる材料は文言だけなので、利用者に判断してもらう。

### 4. ジョブの成否は `error` を先に見る。HTTP ステータスでは判定しない

```go
if j.Error != "" {
    return false, &JobError{...}   // status が 3 でも失敗として扱う
}
```

`serialize_job` は `error` が入る経路で `status` も 4 に落とすが、依存するのは「エラー文言があれば失敗」という一段強い条件の方にする。ステータスの対応表が変わっても失敗を取りこぼさない。

知らない `status` の値はエラーにする。「まだ動いている」に倒すと、`timeout` まで待った末にタイムアウトとして報告することになり、原因に辿り着けない。

### 5. `max_age` は 0 固定にする

`max_age` を省略するか -1 にすると、Redash は期限を問わずキャッシュされた結果を返す。0 だけが「必ず実行する」を意味する。

マスクの対象を決めるのは実行時の列であり、いつ取得されたか分からない結果を返されると、それが現在のスキーマに対応する保証が無い。

これは ADR-0003 の未決事項に残っていた「クエリ結果のキャッシュ（`max_age` の扱い）」に対する決定でもある。**キャッシュは使わない。**

### 6. 行は `columns` の順に射影する

`rows` のオブジェクトを `columns` の順で引き直し、`[]any` に並べ替える。

この射影は副次的に「**出力される列は `columns` に宣言されたものだけ**」を保証する。`rows` 側にだけ現れるキーは落ちる。マスク（#4）は列名で対象を決めるため、宣言されていない列が素通りする方が危険であり、落とす側に倒すのが安全側。

`columns` が空なのに `rows` がある応答はエラーにする。射影すると全ての値が消えるため、空の結果として返すと「0 件だった」と「全部捨てた」を区別できない。

### 7. タイムアウトは段ごとに違う説明をする

3段構えのどこで打ち切ったかで、ジョブの状態も利用者が取るべき手も違う。1つの文言でまとめると、どれかの段で必ず嘘になる。

| 段 | ジョブの状態 | 伝えること |
| --- | --- | --- |
| 投入 | 積まれたかどうか不明 | 投入されたか分からない。接続を確認 |
| 完了待ち | Redash 側で動き続けている | ジョブは残る。`timeout` を延ばすかクエリを軽く |
| 取得 | 完了済み | 遅いのは転送。取得行数を減らす |

とくに投入中は「投入されていません」と言い切ってはならない。POST が届いてジョブが積まれた後に応答だけが失われた可能性が残るためで、言い切ると実際には走っているクエリを放置してよいと誤解させる。

### 8. API KEY はヘッダだけで運ぶ

`Authorization: Key <api_key>` を使う。Redash は先頭の `"Key "` だけを剥がす実装であり、`Bearer` ではない。

`?api_key=` でも渡せるが使わない。URL はリダイレクト先やプロキシのアクセスログに残る。同じ理由で `redash.endpoint` にクエリ・fragment・`user:password` を書くことを禁止する。

エラーに載る Redash 由来の文字列は、秘密が混ざりうる経路（HTTP の本文とジョブの `error`）をすべて伏せ字処理に通す。含まれないことを経路ごとに確かめ続けるより、出口を1か所にする方が確実である。

### 9. リダイレクトは追わない

Go の `http.Client` は既定でリダイレクトを追うが、これは2つの理由で使えない。

**API KEY が平文で流れる。** `Authorization` を転送するかの判定は `shouldCopyHeaderOnRedirect` → `isDomainOrSubdomain` によるホスト名の比較だけで、スキームもポートも見ない。`https://redash.example.com` から `http://redash.example.com` へのリダイレクトで API KEY がそのまま送られる（実測で確認）。

**メソッドが変わる。** Go は 301 / 302 / 303 で POST を GET に落とす。`http://` のエンドポイントが `https://` へリダイレクトする構成では、ジョブ投入の POST が黙って GET になり、POST しか受けない `QueryResultListResource` が 405 を返す。利用者にはリダイレクトが起きたことが見えない。

追わずにエラーにし、リダイレクト先を `redash.endpoint` に書き直してもらう。

## 結果

**得られるもの**

- 「API KEY が違う」が「見つかりません」として表示されなくなる
- ジョブの失敗を HTTP 200 の裏で取りこぼさない
- 列順が保証され、宣言されていない列が出力に混ざらない
- API KEY がリダイレクト先やエラー文言に出ない
- タイムアウト時に、ジョブが残っているかどうかを正しく伝えられる

**支払うコスト・受け入れる制約**

- **未認証の判定が Redash のメッセージ文字列に依存する。** Redash がこの文言を変えると 404 が `APIError` に落ちる。壊れ方は安全側（メッセージ自体は表示される）だが、判定は壊れる
- **ジョブ失敗と SQL エラーを型で分けられない。** 呼び出し側が終了コードを分けたくなっても、文言を見る以外の手段が無い
- **キャッシュを一切使わない。** 同じクエリを繰り返し実行すると毎回データソースに当たる。`max_age` を設定可能にする選択肢は捨てた
- リダイレクトを追わないため、リダイレクトを前提にした構成では `redash.endpoint` の書き直しが要る
- 結果を全てメモリに載せるため、`auto_limit: false` で大きなテーブルを引くと `max_rows` の判定前に OOM しうる（[#16](https://github.com/otama-jaccy/sumiq/issues/16) に切り出した）

## 採用しなかった選択肢

**HTTP ステータスだけでエラーを分類する。** 401 が認証を意味せず、ジョブの失敗が 200 で返る以上、ステータスは分類の軸として使えない。

**404 を一律「見つかりません」として扱う。** 文字列に依存する判定を避けられるが、最も多い設定ミス（API KEY の誤り）が最も分かりにくいエラーになる。判定が壊れても安全側に倒れることを確認したうえで、文字列一致を採る方を選んだ。

**`rows` をマップのまま持ち回る。** 射影のコードが不要になるが、列順が失われ、宣言されていない列がマスクを経ずに出力へ流れる。#5 の JSON 出力で列順が要求されている以上どこかで射影は要るため、最も早い段階で確定させる。

**リダイレクトを追ったうえで、スキームが下がる場合だけ拒否する。** 挙動としては親切だが、追ってよい条件を自前で判断することになり、Go の転送規則との差分を保守し続ける必要が出る。Redash の API は通常リダイレクトしないため、一律で拒否する方が単純で安全。

## 未決事項

- ~~ポーリング中の一時的な失敗をどう扱うか~~ → [ADR-0012](./0012-poll-transient-retry.md) で決定
- 結果のサイズ上限と streaming の要否 → [#16](https://github.com/otama-jaccy/sumiq/issues/16)
- 中断時に `DELETE /api/jobs/{id}` でジョブを止めるか（現状は止めない）

## 参考

- [redash/handlers/query_results.py](https://github.com/getredash/redash/blob/master/redash/handlers/query_results.py) — `error_response` / `error_messages` / `run_query` の `max_age == 0`
- [redash/serializers/\_\_init\_\_.py](https://github.com/getredash/redash/blob/master/redash/serializers/__init__.py) — `serialize_job` の status 対応表とエラーの畳み方
- [redash/authentication/\_\_init\_\_.py](https://github.com/getredash/redash/blob/master/redash/authentication/__init__.py) — `get_api_key_from_request`（`Key ` 接頭辞）と `redirect_to_login`（404）
- [redash/handlers/base.py](https://github.com/getredash/redash/blob/master/redash/handlers/base.py) — `BaseResource.decorators = [login_required]`
- [redash/query_runner/\_\_init\_\_.py](https://github.com/getredash/redash/blob/master/redash/query_runner/__init__.py) — `fetch_columns` による列名の重複解消
- [net/http の shouldCopyHeaderOnRedirect](https://cs.opensource.google/go/go/+/refs/tags/go1.25.5:src/net/http/client.go) — リダイレクト時の `Authorization` 転送判定
