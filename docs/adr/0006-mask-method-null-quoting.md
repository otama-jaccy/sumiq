# ADR-0006: マスク方法 `null` は引用符付きで書く

- ステータス: Accepted
- 日付: 2026-08-14
- 関連: [ADR-0003](./0003-config-file-design.md), [ADR-0004](./0004-output-formats.md), [ADR-0005](./0005-yaml-library.md)

## コンテキスト

[ADR-0003](./0003-config-file-design.md) §9 はマスク方法の一つとして `null`（`NULL` に置換。型を保ちたいときに使う）を定義し、[ADR-0004](./0004-output-formats.md) §3 も出力形式ごとの表現表に載せている。

**しかし、そのまま書くと動かない。**

```yaml
- patterns: ["age"]
  method: null      # NULL 置換のつもり → YAML の空値になる
```

YAML では引用符なしの `null` は文字列ではなく空値である。`go.yaml.in/yaml/v3` の `resolve.go` は次のように解決する。

```go
{nil, nullTag, []string{"", "~", "null", "Null", "NULL"}},
```

`null` だけでなく `~` / `Null` / `NULL` も同じく空値になる。

さらに厄介なことに、**この空値は独自型の `UnmarshalYAML` を経由しない。** `decode.go` の `prepare()` が `nullTag` を見て早期 return し、`Unmarshaler` の呼び出しに到達しないため。

```go
func (d *decoder) prepare(n *Node, out reflect.Value) (newout reflect.Value, unmarshaled, good bool) {
	if n.ShortTag() == nullTag {
		return out, false, false      // ← ここで返る
	}
	...
			if u, ok := outi.(Unmarshaler); ok {
				good = d.callUnmarshaler(n, u)   // ← ここに来ない
```

つまり `MaskMethod` 側では、`method: null` と「`method` を書き忘れた」を区別できない。**取りこぼすと、マスクするつもりだった列がマスクされないまま出力される。**

これは設定ファイルの書式に関わるため、後から変えると利用者の設定を壊す。#1 の時点で決める。

## 決定

**名前は `null` のまま据え置き、設定ファイルには引用符付きで書くことを必須とする。**

```yaml
- patterns: ["age"]
  method: "null"
```

あわせて次を課す。

1. **`method` の未指定はエラーにする。** 既定値で補わない
2. **そのエラーメッセージで引用符を促す。** 空値になったのか本当に書き忘れたのかを型側で区別できない以上、メッセージで両方を案内する
3. **ドキュメント・サンプル・README では常に引用形で示す**

## 根拠

**1. 引用を忘れても事故にならない**

`method` を必須にしてあるため、`method: null` は空値 → 未指定 → **エラーで停止**する。黙って通ってマスクが外れる経路がない。ADR-0003 の fail-closed 方針は保たれる。

コストは利用者の手戻り1回であり、データが漏れることではない。

**2. 語彙を SQL に合わせられる**

`null` は「`NULL` に置換する」という動作の名前として正確である。`nullify` のような造語に替えると、ADR-0003 §9 と ADR-0004 §3 の記述から離れ、SQL を書く利用者の語彙からも離れる。

**3. 既存 ADR の表を古くしない**

改名すると ADR-0003 §9 と ADR-0004 §3 の2つの表が同時に古くなる。据え置けばどちらも有効なまま残る。

## 検討したが採用しなかった選択肢

**`nullify` などへの改名** — 引用符が不要になり、書式の非対称が消える。それでも採らなかった理由は3つ。

- ADR-0003 §9 と ADR-0004 §3 の記述が同時に古くなる
- **改名しても救えるのはエラーメッセージの文面だけ。** ADR-0003 を読んで `method: null` と書いた利用者は、改名後も（空値になるため）エラーになる。「不正な値」と言われるか「引用してください」と言われるかの違いしかなく、体験の差は小さい
- 語彙が SQL から離れる

**未指定を `null` 扱いとして解釈する** — 採らない。`method` の書き忘れが「`NULL` 置換」として通ってしまい、設定ミスが安全側に倒れない。ADR-0003 の方針に真正面から反する。

**引用なしの `null` を `null` 方法として解釈する** — 実装できない。上記のとおり yaml が `Unmarshaler` を呼ばないため、型側に「null が書かれていた」という情報が届かない。`yaml.Node` を生で受けて自前で走査すれば理論上は可能だが、そのためにスキーマ全体のデコードを手書きに落とすのは割に合わない。

## 結果

**得られるもの**

- 引用忘れが黙って通らず、必ずエラーで落ちる
- ADR-0003 / ADR-0004 の語彙と表をそのまま維持できる
- スキーマのデコードを yaml のタグ機構に任せたままにできる

**支払うコスト・受け入れる制約**

- **`method: "null"` だけ引用符が要るという非対称が残る。** 他の方法（`redact` / `hash` / `drop` / `partial` / `none`）は引用符なしで書ける。書式の一貫性を失う
- **ドキュメント・サンプル・エラーメッセージの全てで引用形を示し続ける必要がある。** 1箇所でも引用なしで書くと、それを写した利用者がエラーに当たる。README（#8）とサンプル設定はこの点を守ること
- `Null` / `NULL` / `~` も同様に空値になる。エラーメッセージはこれらにも同じ案内を返す
- 「`method` が未指定」というエラーが、書き忘れと引用忘れの両方を指す。メッセージが1つ長くなる

## 未決事項

**ADR-0003 §7 の強度順序に `null` の位置がない。**

```
drop > redact > hash > partial > none
```

この並びに `null` が含まれていない。1つの列に複数のルールがマッチしたとき、`null` と他の方法のどちらが勝つかが決まらない。

失われる情報量で言えば `null` は `redact` に近い（値は完全に失われ、型だけが残る）が、`redact` との優劣は実装の文脈がないと決められない。**#4（マスクルールの適用エンジン）で決める。**

#1 は enum 値の検証しか行わないため、この未決は #1 の実装には影響しない。

## 参考

- [go.yaml.in/yaml/v3 v3.0.5 `resolve.go` — null に解決される字句](https://github.com/yaml/go-yaml/blob/v3.0.5/resolve.go)
- [go.yaml.in/yaml/v3 v3.0.5 `decode.go` — `prepare()` の nullTag 早期 return](https://github.com/yaml/go-yaml/blob/v3.0.5/decode.go)
- [YAML 1.2.2 spec — 10.3.2. Core Schema Tag Resolution](https://yaml.org/spec/1.2.2/#1032-tag-resolution)（`null | Null | NULL | ~ | 空` が `tag:yaml.org,2002:null` に解決される）
- [Issue #1 — internal/config: 設定スキーマの構造体定義と単一ファイルのロード](https://github.com/otama-jaccy/sumiq/issues/1)
