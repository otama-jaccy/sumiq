---
paths:
  - "**/*.go"
---

# Go コードの制約

`sumiq` で Go を書くときに必ず守る制約。判断の背景は `docs/adr/` を参照。
1つでも破る変更は書かない・提案しない。破らざるを得ない事情があれば、実装する前に理由を説明して確認を取る。

## レイヤと依存の向き

```
cmd/sumiq/main.go     エントリポイント。依存の組み立てと os.Exit のみ
internal/cli/         コマンド定義。kong を知る唯一の層
internal/app/         ドメインロジック。CLI フレームワークを import しない
internal/config/      設定の読み込み
internal/redash/      Redash API クライアント
internal/mask/        マスクルールの適用
```

依存は `cmd → internal/cli → internal/app` の一方向。逆流させない。

`internal/config` と `internal/redash` は `internal/app` から使う外側の部品であり、
**互いを import しない。** 設定のレイヤ構造は Redash の都合と無関係で、
持ち込むと API クライアントを設定ファイル抜きにテストできなくなる。
必要な値は呼び出し側が詰め替えて渡す。

`internal/mask` はその2つを import する（ルールの型は `config`、結果の型は `redash`）。
向きは一方向で、`config` と `redash` は `mask` を知らない。**ここで型を詰め替えない**のは、
変換そのものがルールや列を落としうる経路になるため。落ちたことは出力を見ても分からない。

## 禁止

- **`internal/app` 配下で `github.com/alecthomas/kong` を import すること。**
  ここに kong の型が現れたら境界が壊れたサイン。`internal/cli` 側で詰め替える。

- **`cmd/sumiq/main.go` 以外で `os.Exit` を呼ぶこと。**
  `defer` が実行されず、テストからも呼べない関数になる。「一時的に」も不可。
  他の層はエラーを戻り値で返し、終了コードへの変換は `main` で一度だけ行う。

- **`os.Stdout` / `os.Stderr` への直接書き込み、および `fmt.Print*` の使用。**
  `fmt.Fprint*` に注入済みの `io.Writer` を渡す。

- **パッケージレベルの可変変数（`var` によるグローバル状態）。**
  テスト間で状態が漏れる。フラグの格納先はコマンドごとの構造体に閉じる。

- **`init()` に登録・初期化処理を書くこと。**
  依存の組み立ては `main` で明示的に行う。

- **`pkg/` ディレクトリを作ること。**
  外部モジュールから import させたいものが実際に出るまでは全て `internal/` に置く。

- **`cmd/sumiq/main.go` にロジックを置くこと。**
  依存を組み立て、`internal/cli` を呼び、終了コードを返して終わり。

## 必須

### 出力先は注入する

```go
type Deps struct {
    Out io.Writer
    Err io.Writer
    In  io.Reader
}
```

構造体フィールドか引数で受け取る。テストは `bytes.Buffer` に差し替えて出力内容を検証する。

### kong のコマンドは薄く保つ

コマンドは `internal/cli` にタグ付き構造体 + `Run()` メソッドで定義する。

```go
// internal/cli/root.go
type CLI struct {
    Verbose bool    `help:"詳細ログを出力する"`
    Add     AddCmd  `cmd:"" help:"追加する"`
}

// internal/cli/add.go
type AddCmd struct {
    Name string `arg:"" help:"対象の名前"`
}

func (c *AddCmd) Run(deps *app.Deps) error {
    return app.Add(deps, c.Name)   // 詰め替えるだけ。ロジックを書かない
}
```

**`Run()` にロジックを書かない。** 引数を詰め替えて `internal/app` を呼ぶだけに保つこと。
これがフレームワーク差し替えを可逆にしている唯一の担保であり、この制約群の中で最も壊れやすい。

依存は `kong.Parse()` 後に `ctx.Run(deps)` で注入する。

### kong のタグは書いたら実行して確認する

タグは型のない文字列 DSL で、検証は実行時。`requred` のようなタイポはコンパイルを通り、
`kong.Parse()` で panic する。タグを追加・変更したら必ず一度起動して確認する。

### 安全側の検査は「判定できなかった」を「問題なし」に倒さない

マスク・秘密の混入・行数のように、通してしまうと事故になる判定では、
**確認できなかった場合をエラーにする。**

```go
// 悪い: git が落ちた理由を問わず「追跡外」にしてしまう
if errors.As(err, &exitErr) {
    return false, nil
}

// 良い: 「追跡外」を意味する終了コードだけを false にする
if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
    return false, nil
}
return false, fmt.Errorf("判定できませんでした: %w", err)
```

同じ理由で、**検査は「その値が実際に使われるか」と切り離して走らせる。**
負けたレイヤの設定も検査対象にする。「使われないから見なくてよい」は、
最もありふれた構成でだけ検査が消える状態を作る。

### ゼロ値を「未指定」として扱うなら、ゼロ値になる指定を受け付けない

`internal/config` は各フィールドのゼロ値を「未指定」として扱い、マージの
上書き対象から外している。この設計では `timeout: 0s` と書いても下のレイヤの値が
残るため、**利用者から見ると書いた設定が黙って別の値に差し替わる。**

ゼロ値を意味のある値として受け取りたい場合はポインタで受ける（`*bool` の
`auto_limit` がその例）。ポインタにしないなら、ゼロ値の指定を検証で弾く。

### JSON の数値を `any` で受けるなら `UseNumber` を立てる

`encoding/json` の既定では `any` に入る数値がすべて `float64` になる。
`sumiq` が運ぶのは `user_id` のような ID であり、**2^53 を超えた時点で
静かに桁が落ちる。** 出力もマスク後のハッシュも元の値と対応しなくなる。

```go
dec := json.NewDecoder(r)
dec.UseNumber()   // これが無いと 9007199254740993 が 9007199254740992 になる
```

型を決めずに受ける場所（クエリ結果のセル等）では必ず立てる。
立てたことに依存するコメントを書くなら、**どの関数が立てているかを名指しする。**

### 外部から来た値を `url.JoinPath` にそのまま渡さない

`url.JoinPath` は要素の中の `/` と `%` をエスケープせず、`..` を辿って詰める。

```go
u.JoinPath("api", "jobs", "../../evil")   // => /evil
u.JoinPath("api", "jobs", "a%2Fb")        // => /api/jobs/a%2Fb → サーバ側で a/b
```

応答から受け取った ID をパス要素にする前に、`/` `%` と `.` `..` を弾く。
自分で組み立てた数値や定数はそのままでよい。

`url.URL` の正規化で `RawPath` を消すのも同じ種類の事故になる。消すと
`EscapedPath` が `Path` を再エンコードし、`%2F` が本物の区切りに変わる。

### リダイレクトを追う前に、認証情報の行き先を確かめる

`http.Client` の既定はリダイレクトを追う。`Authorization` を転送するかの
判定は**ホスト名の比較だけ**で、スキームもポートも見ない
（`shouldCopyHeaderOnRedirect` → `isDomainOrSubdomain`）。
`https://host` から `http://host` へのリダイレクトで、秘密が平文で流れる。

加えて Go は 301 / 302 / 303 で POST を GET に落とす。追う必要が無いなら
`CheckRedirect` で拒否する。

`CheckRedirect` が返したエラーは `*url.Error` に包まれ、その URL フィールドに
**リダイレクト先がクエリごと**入る。文言側で URL を伏せても包みから出るため、
専用の型を返して呼び出し側で包みを捨てる。

### 列名の照合に `path.Match` / `filepath.Match` を使わない

どちらも `/` を区切りとして扱い、`*` と `?` が `/` をまたがない。加えて `\` を
打ち消しとして読む。

```go
path.Match("*", "payload/user/email")        // => false
path.Match(`payload\user`, `payload\user`)   // => false（\u が u に化ける）
```

列名は `sumiq` が決めるものではなく、`SELECT ... AS "payload/user/email"` と
書けばどうにでもなる。**`patterns: ["*"]` が列にマッチしない状態は、マスクが
黙って外れることを意味する。** `filepath.Match` は Windows で `\` の扱いが変わり、
同じ設定が実行環境で違う結果になる。

パターンは正規表現に直して照合する（[ADR-0010](../../docs/adr/0010-mask-pattern-dialect.md)）。

同じ理由で、**照合器を組み立てられないパターンはエラーにする。**「何にもマッチしない
パターン」として読み飛ばすと、そのルールが消えたまま実行される。

### 値の一部を残す処理は「残す条件」として書く

マスクで値の一部を残すとき、条件を満たさない入力は**全て伏せる側に倒す。**
残す量を入力に合わせて調整しない。

```go
// 悪い: 残す指定が値より長いと、値がそのまま出る
if prefix < len(rs) { ... }
return s

// 良い: 残せないなら全部伏せる
if prefix >= len(rs) || suffix >= len(rs) || prefix+suffix >= len(rs) {
    return redacted
}
```

`prefix + suffix` だけを見ると、極端な値で桁溢れして負になり検査をすり抜ける。
片方ずつ先に比べること。

### 外部コマンドを起動するなら `WaitDelay` を設定する

`exec.CommandContext` が kill するのは直接の子だけである。孫が標準出力の
パイプを握ったままだと `Output()` は EOF を待って止まり続け、
**タイムアウトを設定しても効かない。**

```go
cmd := exec.CommandContext(ctx, name, args...)
cmd.WaitDelay = 5 * time.Second   // これが無いと締切は守られない
```

### `text/tabwriter` に渡すセルは制御文字をエスケープする

`text/tabwriter` は `\t` `\v` をセルの区切り、`\n` `\f` を行の区切りとして
構造的に解釈する（パッケージのドキュメントコメントより）。自由記述の列の値に
これらの文字が含まれると、値の一部が列・行の境界と誤読され、以降のセルが
隣の行や列にずれて出力が壊れる。切り詰めではなく、`strings.NewReplacer` で
バックスラッシュ付きの可視表記（`\t` を書き起こした `\` + `t` の2文字、等）に
置き換えてから渡すこと（[ADR-0004](../../docs/adr/0004-output-formats.md)、
`internal/output`）。

同じ理由で、tabwriter のセルに ANSI エスケープシーケンスで色を付けない。
tabwriter は全 Unicode コードポイントを同じ幅として列幅を計算するため、
不可視のエスケープバイトもそのままセル幅に数えられ、列がずれる。TTY 向けの
装飾は `tabwriter.Debug`（列の境界に `|` を挿む標準ライブラリの機能）のように
セルの中身を変えない形で行う。

## テスト

- `internal/app` は kong に依存しないので、素の関数としてテーブルドリブンで検証する。
  外部プロセスを起動する E2E は書かない。
- 出力の検証は `bytes.Buffer` で行う。ゴールデンファイルが必要になったら相談する。

### プロセスの状態はテストから差し替えられる形で受け取る

カレントディレクトリ・環境変数・ホームディレクトリ・外部コマンドの有無に
依存するコードは、それらを引数か構造体のフィールドで受け取る。
`os.Setenv` や `os.Chdir` で書き換えるテストは状態が漏れる。

環境変数はスライス（`os.Environ()` 形式）で受け取り、`nil` のときだけ
プロセスの環境に落ちる形にする。テストは空スライスを渡す。
**`nil` と空スライスを同じ扱いにしない。** 同じにすると、実行環境に変数が
あるかどうかでテストの結果が変わる。

### 応答を止めるテストサーバは、締切と競争させない

`httptest` のハンドラを止めて打ち切りを再現するとき、2つ踏みやすい。

**`t.Cleanup` は後入れ先出しで走る。** `srv.Close()` は実行中のハンドラの
終了を待つため、ハンドラを解放する `close(ch)` を先に登録すると、
`Close()` が待ち、ハンドラは `ch` を待って**テストごと止まる。**

```go
srv := httptest.NewServer(h)
t.Cleanup(srv.Close)              // 先に登録 = 後に走る
t.Cleanup(func() { close(ch) })   // 後に登録 = 先に走る
```

**どの段で締切が切れるかを壁時計に賭けない。** 前の段が実行環境の負荷で
遅れると、狙っていない段で打ち切られて別の理由で落ちる。止めたい段だけを
無限に待たせ、締切はそこでしか切れない長さにする。到達したことは
チャネルで確認する。

### 安全側の検査には、壊して落ちることを確かめたテストを置く

「弱化を防ぐ」種類の検査は、実装を1行変えれば黙って無効化できる。
検査を追加したら、**その検査を外すと落ちるテストがあることを確認する。**
通っていることの確認だけでは、検査が効いているかは分からない。

## 構成の育て方

最初から全ディレクトリを切らない。サブコマンドが増えるまで `internal/cli` 内はフラットでよい。
ただし上記の禁止・必須は規模に関係なく最初から適用する。

## ADR

- [ADR-0001](../../docs/adr/0001-layered-cli-architecture.md): レイヤ構成
- [ADR-0002](../../docs/adr/0002-adopt-kong-as-cli-framework.md): CLI フレームワークに kong を採用
- [ADR-0007](../../docs/adr/0007-layered-merge-guards.md): レイヤードマージの安全ガード
  （上の「安全側の検査」「ゼロ値」「`WaitDelay`」の背景）
- [ADR-0008](../../docs/adr/0008-redash-client-error-classification.md): Redash クライアントの
  エラー分類と応答の読み方（上の「`UseNumber`」「`url.JoinPath`」「リダイレクト」の背景）
- [ADR-0009](../../docs/adr/0009-mask-null-strength.md): マスク方法 `null` の強度
- [ADR-0010](../../docs/adr/0010-mask-pattern-dialect.md): 列名パターンの方言
  （上の「`path.Match` を使わない」の背景）
- [ADR-0011](../../docs/adr/0011-partial-keep-options.md): `partial` の `keep` の仕様
  （上の「残す条件として書く」の背景）

設計判断を変える場合は既存 ADR を書き換えず、ステータスを `Superseded by ADR-XXXX` にして新しい ADR を立てる。

以下に該当したら ADR-0002（kong 採用）の再検討を提案すること。

- 社外配布が決まり shell 補完が要件になった
- man ページ生成が必要になった
- フラグ / コマンドの動的生成が必要になった
