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
```

依存は `cmd → internal/cli → internal/app` の一方向。逆流させない。

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

### 外部コマンドを起動するなら `WaitDelay` を設定する

`exec.CommandContext` が kill するのは直接の子だけである。孫が標準出力の
パイプを握ったままだと `Output()` は EOF を待って止まり続け、
**タイムアウトを設定しても効かない。**

```go
cmd := exec.CommandContext(ctx, name, args...)
cmd.WaitDelay = 5 * time.Second   // これが無いと締切は守られない
```

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

設計判断を変える場合は既存 ADR を書き換えず、ステータスを `Superseded by ADR-XXXX` にして新しい ADR を立てる。

以下に該当したら ADR-0002（kong 採用）の再検討を提案すること。

- 社外配布が決まり shell 補完が要件になった
- man ページ生成が必要になった
- フラグ / コマンドの動的生成が必要になった
