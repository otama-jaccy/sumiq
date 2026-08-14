# ADR-0005: YAML ライブラリに go.yaml.in/yaml/v3 を採用する

- ステータス: Accepted
- 日付: 2026-08-14
- 関連: [ADR-0003](./0003-config-file-design.md)

## コンテキスト

[ADR-0003](./0003-config-file-design.md) は設定ファイルの形式を YAML と決めたが、**どのライブラリで読むかは決めていない。** #1 の実装で必要になった。

`sumiq` の要件は3つ。

1. **strict デコード（未知キーをエラーにする）。** ADR-0003 の目的は事故防止であり、`patern:` のようなタイポを黙って無視するとマスクルールが1つ消えたまま動く。これは「あると良い」ではなく必須
2. **独自型のデコード。** `300s` のような duration、`method` / `format` のような列挙値を型側で検証したい
3. **行番号を含むエラー。** 設定ファイルは人が手で書く。どの行が悪いか分からないと直せない

ここで前提を1つ確認する必要があった。**Go で最も広く使われてきた `gopkg.in/yaml.v3` は、2025年4月に作者がリポジトリを unmaintained と表明して以降メンテナンスされていない。** 習慣のまま選ぶと、メンテナンスされていないライブラリを設定ファイルの入口に置くことになる。

| 候補 | 状況 |
| --- | --- |
| `gopkg.in/yaml.v3` | 2025年4月に unmaintained と表明。更新停止 |
| `go.yaml.in/yaml/v3` | YAML 組織が引き継いだ後継。API 互換。v3.0.5（2026-07-26） |
| `go.yaml.in/yaml/v4` | 同組織の次期版。v4.0.0-rc.6（2026-06-17）で安定版未リリース |
| [goccy/go-yaml](https://github.com/goccy/go-yaml) | 独立実装。活発に開発中。v1.19.2（2026-01-08） |

## 決定

**`go.yaml.in/yaml/v3` を採用する。**

strict デコードは `Decoder.KnownFields(true)` で有効化する。

```go
dec := yaml.NewDecoder(r)
dec.KnownFields(true)
```

## 根拠

**1. 未メンテナンスの `gopkg.in/yaml.v3` を避けつつ、API が同一**

移行はインポートパスの置換で済む。`gopkg.in/yaml.v3` 向けに書かれた既存の知識・記事・回答がそのまま通用する。

**2. 要件をそのまま満たす**

`KnownFields(true)` が要件1、`Unmarshaler` インターフェースが要件2、`Node.Line` が要件3に対応する。追加の仕組みを自前で書く必要がない。

**3. 単一メンテナではなく組織が引き継いでいる**

`gopkg.in/yaml.v3` が止まった原因は、作者個人がメンテナンスを降りたことにある。同じ構図を再び選ばない。

**4. v4 はまだ rc**

設定ファイルの読み込みは `sumiq` の入口であり、ここで安定版のないメジャーバージョンを踏む理由がない。

## 検討したが採用しなかった選択肢

**`gopkg.in/yaml.v3`** — 実績は最大だが更新が止まっている。設定ファイルのパーサは攻撃面でもあり、セキュリティ修正が来ない前提で選ぶものではない。

**`go.yaml.in/yaml/v4`** — v4.0.0-rc.6 で安定版が出ていない。v3 と同組織・同系統なので、安定版が出た時点で移行を検討する（後述のトリガー）。

**`goccy/go-yaml`** — 活発に開発されており、`Strict()` / `DisallowUnknownField()` を持ち、**エラーに YAML のソース断片を添えて表示できる点はこちらが明確に優れている。** それでも採らなかったのは、乗り換えの決め手がそのエラー表示の良さだけだったため。要件3は行番号で足りており、`sumiq` の設定ファイルは数十行規模でソース断片の表示が効く場面が少ない。API が異なる分、参照できる情報も減る。

ただしこれは僅差の判断であり、エラー表示の質が実際に問題になったら再評価する。

## 結果

**得られるもの**

- 未知キーの検出が標準機能で賄え、自前実装を持たない
- `gopkg.in/yaml.v3` 向けの知識がそのまま通用する
- 組織メンテナンスによりセキュリティ修正が期待できる

**支払うコスト・受け入れる制約**

- **v3 系はセキュリティ修正のみで、機能追加は v4 に行く。** いずれ移行の判断が必要になる。今回はそれを先送りしている
- **エラーがフィールドパスを含まない。行番号のみ。** 実測では `version: "1"` に対して `line 1: cannot unmarshal !!str \`1\` into int` となり、どのフィールドかは書かれない。フィールド名が必要なメッセージは自前で組み立てる
- **null ノードは `Unmarshaler` を経由しない。** `decode.go` の `prepare()` が `nullTag` を見て早期 return するため、独自型の `UnmarshalYAML` は呼ばれない。結果として型側で「値が null」と「未指定」を区別できない。これは `masking.rules[].method` の `null` と正面から衝突する（[ADR-0006](./0006-mask-method-null-quoting.md)）
- インポートパスが `gopkg.in/yaml.v3` ではないため、既存のサンプルコードを写すときに書き換えが要る

## 再検討のトリガー

- **`go.yaml.in/yaml/v4` が安定版になった。** 同組織・同系統であり、移行先の第一候補
- エラーメッセージの分かりにくさが実際に運用の障害になった（`goccy/go-yaml` を再評価する）
- `go.yaml.in` 側のメンテナンスが再び停滞した

## 参考

- [yaml/go-yaml — YAML 組織による引き継ぎの経緯](https://github.com/yaml/go-yaml)
- [go.yaml.in/yaml/v3 v3.0.5 — pkg.go.dev](https://pkg.go.dev/go.yaml.in/yaml/v3)
- [go.yaml.in/yaml/v4 v4.0.0-rc.6 — pkg.go.dev](https://pkg.go.dev/go.yaml.in/yaml/v4)
- [goccy/go-yaml — pkg.go.dev](https://pkg.go.dev/github.com/goccy/go-yaml)
- [go-yaml/yaml（アーカイブ済み）](https://github.com/go-yaml/yaml)
