# sumiq

Redash に ad-hoc クエリを投げ、設定に従ってマスクした結果を出力する CLI。

生の SQL 結果をそのまま画面や CI ログに流すのではなく、あらかじめレビューした
マスク方針（`sumiq.yaml`）を通してから出力する。マスク方針は git 上のレビュー対象になる。

**`sumiq` はセキュリティ境界ではない。** API KEY を直接使えば設定を迂回できる。
目的はアクセス制御ではなく、事故防止とマスク方針のレビュー可能化にある
（設計判断の背景は [ADR-0003](docs/adr/0003-config-file-design.md) を参照）。

## インストール

```bash
go install github.com/otama-jaccy/sumiq/cmd/sumiq@latest
```

または、リポジトリを clone してビルドする。

```bash
git clone https://github.com/otama-jaccy/sumiq.git
cd sumiq
go build -o sumiq ./cmd/sumiq
```

## 基本的な使い方

1. 設定ファイルを用意する（後述「設定」を参照。最短では次の2つをコピーするだけ）

   ```bash
   cp sumiq.yaml.example sumiq.yaml
   cp sumiq.local.yaml.example sumiq.local.yaml
   ```

2. API KEY を用意する（後述「API KEY を共有ファイルに書くとエラーで停止する」節を参照。
   最短では環境変数を使う）

   ```bash
   export SUMIQ_REDASH_API_KEY=xxxxxxxx
   ```

3. クエリを実行する

```bash
sumiq query -d analytics "SELECT id, email FROM users LIMIT 10"
```

- `-d` / `--data-source` は必須。設定ファイルに定義した名前のみ指定でき、
  数値 ID を直接渡すことはできない。デフォルトのデータソースは無い
- `--format` で `table`（既定） / `json` / `csv` を選べる

```bash
sumiq query -d analytics --format json "SELECT id, email, memo FROM users" | jq .
```

マスク・打ち切りの情報は stderr に出る。stdout はデータのみで、パイプ先を壊さない。

```
Masked: email (partial), memo (redact)
Dropped: --
Rows: 342
```

## 設定

設定は複数のファイルをレイヤードにマージして決まる（下が勝つ、スカラーは上書き）。

```
1. 埋め込みデフォルト
2. ~/.config/sumiq/config.yaml    ユーザ全体
3. <repo>/sumiq.yaml              リポジトリ共有（コミットする）
4. <repo>/sumiq.local.yaml        ローカル（.gitignore に入れる）
5. 環境変数 SUMIQ_*
6. コマンドライン引数
```

3・4 はカレントディレクトリから git リポジトリのルートまで遡って探索するため、
サブディレクトリから実行しても読まれる。`--config` を明示した場合はこの探索
（2〜4）をまとめてスキップする。詳細は [ADR-0003](docs/adr/0003-config-file-design.md) を参照。

`sumiq.yaml.example` / `sumiq.local.yaml.example` をコピーして使い始める
（クイックスタート参照）。

- **`sumiq.yaml`（共有設定）** — Redash のエンドポイント、データソースの定義、
  マスクルールを書く。git にコミットし、チームでレビューする
- **`sumiq.local.yaml`（ローカル設定）** — API KEY と、個人の手元だけで使う
  データソース・追加のマスクルールを書く

### `sumiq.local.yaml` を `.gitignore` に入れること

**必須。** リポジトリの `.gitignore` には既に `sumiq.local.yaml` が登録されている
（先頭に `/` を付けていないのは、サブディレクトリに置かれたローカル設定も
除外対象にするため）。フォークや別リポジトリでこの構成を流用する場合は、
必ず自分の `.gitignore` にも同じ行を足すこと。

### API KEY を共有ファイルに書くとエラーで停止する

`redash.api_key` / `redash.api_key_command` を **git の管理下にあるファイル**
（典型的には `sumiq.yaml`）に書くと、`sumiq` はロード時にエラーで起動を拒否する。
「書かないでください」という規約ではなく、構造で防ぐ。

API KEY の取得元は3経路。詳細は `sumiq.local.yaml.example` を参照。

1. 環境変数 `SUMIQ_REDASH_API_KEY`
2. `sumiq.local.yaml` の `redash.api_key`（`${env:VAR}` 展開に対応）
3. `sumiq.local.yaml` の `redash.api_key_command`（1Password CLI 等の外部コマンドの
   標準出力を使う）

`api_key` と `api_key_command` は同時に指定できない。

### マスク設定はローカルから弱められない

マスクは安全装置であり、`sumiq.local.yaml` で `sumiq.yaml` のマスクを弱める
ことはできない構造になっている。

- ルールは全レイヤの**和集合**として適用される（上書きではなく追加）。
  ローカルは追加のみでき、共有ルールの削除・弱化はできない
- 1つの列に複数のルールがマッチしたら、**最も強い方法が勝つ**
  （`drop > redact > null > hash > partial > none`）
- `default_action`（マッチしなかった列の既定の扱い）の上書きは
  **厳しくする方向のみ**許可される
- **`method: none`（明示的な許可）は共有ファイル（`sumiq.yaml`）にのみ書ける。**
  allowlist 運用（`default_action: redact`）で特定の列だけを通すための指定であり、
  ローカルから書けると弱化そのものになるため

マージ規則の詳細（強度順序・厳格化のみ許可される項目 等）は
[ADR-0003](docs/adr/0003-config-file-design.md) §7、強度順序に `null` を加えた
経緯は [ADR-0009](docs/adr/0009-mask-null-strength.md) を参照。

列名パターンは既定でグロブ（`*` は任意の並び、`?` は任意の1文字、大文字小文字を
無視）。`[` を含むパターンはエラーになる。列名の一部だけを対象にしたい場合は
`regex:` 接頭辞で正規表現に切り替える（`regex:` も大文字小文字を無視する）。
詳細は [ADR-0010](docs/adr/0010-mask-pattern-dialect.md) を参照。

### `hash` の salt は実行ごとにランダムで、実行をまたいだ突き合わせはできない

`hash` は `sha256(salt + value)` の先頭12文字に置換する。**salt は実行のたびに
ランダムに生成される。** 1回の実行内では一貫するため、同じ実行結果の中での
件数集計や JOIN はできる。しかし**別の実行（別のコマンド呼び出し）が出した
ハッシュ値とは、値が同じでも突き合わせられない。** これは仕様であり、固定・共有
salt にする予定はない（低カーディナリティ値の総当たり復元を防ぐため）。詳細は
[ADR-0003](docs/adr/0003-config-file-design.md) §9 を参照。

### `auto_limit` は効かないケースがあり、実際の防御線は `max_rows`

`query.auto_limit`（既定 `true`）は Redash 側の「LIMIT 1000」相当の機能を呼び出す
**最適化**にすぎず、CTE を含むクエリや SQL 以外のデータソースでは**黙って効かない**
（Oracle / SQL Server では逆にクエリが壊れるため個別に `auto_limit: false` にする）。

**実際に効く安全装置は `query.max_rows` である。** 取得後にクライアント側で判定する
ため、`auto_limit` が効かない状況でも必ず効く。超過時の挙動は `query.on_exceed`
で決める（既定 `error`）。`truncate` は部分結果を全件と誤認する事故につながりうる
ため、必要な場合のみ明示的に選ぶこと。詳細は
[ADR-0003](docs/adr/0003-config-file-design.md) §10 を参照。

### 許可リストはセキュリティ境界ではない

`data_sources` に定義したデータソース名だけを `-d` で受け付ける仕組みは、
指定ミス防止とレビュー範囲の明示のためであり、**アクセス制御ではない。**
利用者が Redash の API KEY を直接使って `curl` 等で叩けば、この許可リストは
迂回できる。同様に、ローカル定義のデータソース（`sumiq.local.yaml` の
`data_sources`）を使った実行では、マスク方針がチームレビューを通っていないことを
示す警告が毎回出るが、実行自体は止めない。

設計判断の詳細は [ADR-0003](docs/adr/0003-config-file-design.md) を参照。

## 出力形式

`--format table`（既定） / `json` / `csv` の3種。

| method | table | json | csv |
| --- | --- | --- | --- |
| `redact` | `****` | `"****"` | `****` |
| `partial` | 残した部分 | 同左（文字列） | 同左 |
| `hash` | ハッシュ先頭12文字 | 同左（文字列） | 同左 |
| `null` | `NULL` | `null` | 空フィールド |
| `drop` | 列を表示しない | キーを含めない | ヘッダからも除く |

マスクされた列の値は常に文字列として出力される（元の型は保持しない）。
マスクされていない列は元の型を保つ。

### `csv` は `null` と空文字列を区別できない

CSV 形式の制約であり、`sumiq` 側では回避しない。値が `NULL` だったのか
空文字列だったのかを区別する必要がある場合は `json` 形式を使うこと。

詳細は [ADR-0004](docs/adr/0004-output-formats.md) を参照。

## 参考

- [ADR-0003: 設定ファイルの設計](docs/adr/0003-config-file-design.md)
- [ADR-0004: 出力形式](docs/adr/0004-output-formats.md)
- [ADR-0009: マスク方法 `null` の強度](docs/adr/0009-mask-null-strength.md)
- [ADR-0010: 列名パターンの方言](docs/adr/0010-mask-pattern-dialect.md)
- [ADR-0011: `partial` の `keep` の仕様](docs/adr/0011-partial-keep-options.md)
