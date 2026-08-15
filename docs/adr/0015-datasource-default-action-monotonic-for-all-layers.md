# ADR-0015: データソース単位の default_action は全レイヤでグローバル既定より緩くできない

- ステータス: Accepted
- 日付: 2026-08-15
- 関連: [ADR-0003](./0003-config-file-design.md)（設定ファイルの設計）、[ADR-0007](./0007-layered-merge-guards.md)（レイヤードマージの安全ガード）

## コンテキスト

[#4](../../.claude/rules/go-architecture.md)（マスク適用エンジン）の実装とセルフレビュー中に、`internal/config` と `internal/mask` の間で次の食い違いが見つかった（[#18](https://github.com/otama-jaccy/sumiq/issues/18)）。

`internal/config.checkDataSourceActions` はこれまで、データソース単位の `default_action` がグローバル既定より緩いことを、**レビューされないレイヤ（ユーザ設定・ローカル設定）に限って**弾いていた。共有ファイルの定義はこの検査の対象外で、次のような設定を通していた。

```yaml
# sumiq.yaml（共有ファイル、レビュー済み）
masking:
  default_action: redact
data_sources:
  - name: sandbox
    id: 7
    default_action: none    # レビュー済みなので config は通す
```

このコードには、ADR-0003 §8 の「グローバルを緩く保ったままデータソース単位で引き上げる」という運用を根拠に、意図的な設計として次のコメントが付いていた。

> 共有ファイルの定義は対象外とする。ADR-0003 §8 はグローバルを緩く保ったままデータソース単位で引き上げる運用を前提にしており、レビュー済みの引き下げまで禁じると、その運用に必要な自由度を潰してしまう。

一方、`internal/mask.fallbackMethod`（マスクエンジン側の実装）は次の通り、データソース単位の指定を**厳格化方向にしか反映しない**。

```go
func fallbackMethod(global, perDataSource config.Action) (config.MaskMethod, error) {
    method, err := actionMethod(global)
    // ...
    if strictness(perDataSource) > strictness(global) {
        return perMethod, nil
    }
    return method, nil   // 緩ければ黙って無視し、グローバルのままにする
}
```

このコメントも「レビュー済みの共有ファイルにしか書けない組み合わせ（config 側は共有ファイルの引き下げを通す）で実行そのものができなくなる」と、config 側が引き下げを通す前提で書かれていた。

つまり、上のサンプルは **`config.Resolve` はエラーにせず通すが、`internal/mask.New` が作るエンジンでは `sandbox` を引いてもマッチしないすべての列が `redact` のまま `****` になる**。書いた `default_action: none` が実行時には黙って無視される。これは `.claude/rules/go-architecture.md` が禁じる「ゼロ値の扱い」と同じ形の事故で、利用者から見れば書いた設定が黙って別の値に差し替わる。

### なぜ config 側で直すか

この不整合は、次のどちらかで解消できる。

1. `internal/config` 側で、共有ファイルであってもデータソース単位の引き下げを拒否する
2. `internal/mask` 側で、データソース単位の指定を緩める方向にも反映する

2 は ADR-0003 §7 の「default_action の上書きは厳しくする方向のみ許可する」という原則（マスクは安全装置であり、どの経路からも弱める方向には動かさない）に反する。データソース単位の指定であっても、グローバルより緩い値を実際に効かせてしまうと、グローバルを `redact`（allowlist）にした意味が個別のデータソースで崩れる。

したがって 1 を採る。**`config` が受け付ける範囲を `internal/mask` が実際に反映できる範囲に揃える。**

## 決定

`checkDataSourceActions` から「レビュー済みレイヤは対象外」という例外を外す。データソース単位の `default_action` がグローバル既定より緩いことは、**レイヤに関わらず（共有ファイルの定義も含めて）常にエラーにする。**

- データソース単位の `default_action` は、常にグローバル既定と同じか、より厳しい（`redact`）方向にしか書けない
- 未指定ならグローバル既定をそのまま継承する（緩くはならない）
- この検査は全レイヤを畳み終えた後、最終的なグローバル既定と突き合わせて行う（`checkDataSourceActions` と同じ位置）

`internal/mask.fallbackMethod` 側は変更しない。「データソース単位は厳格化方向にしか効かない」という実装をそのまま正とし、config 側をそれに合わせる。

## 結果

**得られるもの**

- config が受け付ける設定と、実行時に実際に効く設定が一致する。「書いたのに効かない」事故が構造的に無くなる
- `default_action` の弱化経路が、レイヤ・データソースの区別なく1つの検査に集約される

**支払うコスト**

- ADR-0003 §8 が想定していた「グローバルを緩く保ったまま、機微でないデータソースだけ個別に緩める」運用は選べなくなる。**データソース単位の `default_action` は常に「引き上げ」専用**になり、個別に緩めたい場合はグローバル既定そのものを緩くするしかない
- 上記の結果、複数のデータソースのうち一部だけを緩くしたい（他は `redact` のまま）という要求には応えられない。そのデータソース向けに `masking.rules` を `method: none` で個別に許可する運用（allowlist 運用そのもの）に寄せる必要がある

**採用しなかった選択肢**

- **`internal/mask.fallbackMethod` 側でデータソース単位の緩和を実際に反映する。** ADR-0003 §7 の「default_action の上書きは厳しくする方向のみ許可する」という原則に反するため却下した。データソース単位だけ例外にすると、なぜレイヤの `default_action` は緩められないのにデータソースの `default_action` は緩められるのかという非対称を新たに生む
- **`internal/mask` 側で「この指定は効きません」という警告を出すに留める。** 誤りを実行時の警告に頼ると、警告を無視した・見逃した状態のまま allowlist が壊れたまま運用され続けるリスクが残る。また警告の出し先（stderr）は別 Issue（#5）の実装待ちであり、そこに依存させたくない

## 未決事項

- 複数データソースのうち特定の1つだけを実質的に緩くしたいという要求が今後出た場合、`masking.rules` の `data_sources` スコープ＋`method: none` の組み合わせで賄えるかを改めて検討する。今回はその運用で足りると判断し、新しい仕組みは追加していない

## 参考

- [ADR-0003 設定ファイルの設計](./0003-config-file-design.md) §7・§8
- [ADR-0007 レイヤードマージの安全ガード](./0007-layered-merge-guards.md)
- `internal/mask/method.go` の `fallbackMethod`（実装済みの厳格化方向のみの反映ロジック）
