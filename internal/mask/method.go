package mask

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/otama-jaccy/sumiq/internal/config"
)

const (
	// redacted は値を伏せるときの置換文字列（ADR-0004 §3）。
	//
	// 元の長さに合わせて伸縮させない。長さそのものが手掛かりになる。
	redacted = "****"

	// hashLength は hash が出すハッシュの文字数。
	//
	// 16 進で 12 文字 = 48 ビット。max_rows が 1000 前後である以上、
	// 実行内で衝突する確率は無視できる。全 64 文字を出しても読み手には
	// 使い道が無く、表が崩れるだけなので短く切る。
	hashLength = 12

	// keepDomain は partial の keep に書ける唯一の値（ADR-0003 §9）。
	keepDomain = "domain"
)

// strength は method の強さを返す。大きいほど強い。
//
//	drop > redact > null > hash > partial > none
//
// ADR-0003 §7 の並びに null が無かったため、redact と hash の間に置いた。
// 判断は docs/adr/0009-mask-null-strength.md にある。
//
// 未知の値は 0 を返すが、New が弾くのでここには来ない。method を増やすときは
// この関数と knownMethod の両方を直すこと。片方だけ直すと、新しい method が
// 最も弱い扱いになって他のルールに負ける。
func strength(m config.MaskMethod) int {
	switch m {
	case config.MaskDrop:
		return 5
	case config.MaskRedact:
		return 4
	case config.MaskNull:
		return 3
	case config.MaskHash:
		return 2
	case config.MaskPartial:
		return 1
	case config.MaskNone:
		return 0
	}
	return 0
}

// knownMethod は strength が強さを決められる method かを返す。
func knownMethod(m config.MaskMethod) bool {
	switch m {
	case config.MaskDrop, config.MaskRedact, config.MaskNull,
		config.MaskHash, config.MaskPartial, config.MaskNone:
		return true
	}
	return false
}

// stronger は同じ列にマッチした2つのマスクのうち強い方を返す。
func stronger(a, b columnMask) columnMask {
	switch {
	case strength(a.method) > strength(b.method):
		return a
	case strength(a.method) < strength(b.method):
		return b
	}
	// 同じ強さでも partial だけは残す範囲が違いうる。片方を選ぶと、
	// もう片方が隠すつもりだった部分が出る。両方が残す部分だけを残す。
	a.partial = a.partial.tighten(b.partial)
	return a
}

// partialSpec は partial が残す範囲。
type partialSpec struct {
	// domain は @ 以降を残すか。
	domain bool
	// prefix / suffix は残す文字数（ルーン単位）。
	prefix int
	suffix int
}

// keepsNothing は何も残さない指定かを返す。
func (s partialSpec) keepsNothing() bool {
	return !s.domain && s.prefix == 0 && s.suffix == 0
}

// tighten は2つの指定を、どちらも残す部分だけに狭める。
//
// 弱い方に合わせるとルールを足すたびに出る情報が増え、和集合であるはずの
// マージが弱化の経路になる。狭める方向にしか動かさない。
func (s partialSpec) tighten(o partialSpec) partialSpec {
	return partialSpec{
		domain: s.domain && o.domain,
		prefix: min(s.prefix, o.prefix),
		suffix: min(s.suffix, o.suffix),
	}
}

// partialSpecOf はルールから partial の指定を取り出して検証する。
//
// partial 以外の method でも keep 系の指定は検証する。書いてある以上は
// 意味を持つと読まれるため、綴り間違いを黙って通さない。
func partialSpecOf(r config.MaskRule) (partialSpec, error) {
	spec := partialSpec{prefix: r.KeepPrefix, suffix: r.KeepSuffix}
	switch r.Keep {
	case "":
	case keepDomain:
		spec.domain = true
	default:
		return partialSpec{}, fmt.Errorf("keep: %q は扱えません。指定できるのは %q だけです", r.Keep, keepDomain)
	}
	if r.KeepPrefix < 0 || r.KeepSuffix < 0 {
		return partialSpec{}, fmt.Errorf("keep_prefix / keep_suffix に負の値は指定できません: %d / %d",
			r.KeepPrefix, r.KeepSuffix)
	}
	if r.Method == config.MaskPartial && spec.keepsNothing() {
		return partialSpec{}, fmt.Errorf("method: partial には keep: %q / keep_prefix / keep_suffix の"+
			"いずれかが必要です", keepDomain)
	}
	return spec, nil
}

// maskValue は値1つにマスクを適用する。
//
// 値が NULL かどうかは見ない。見て分岐すると、その列で NULL だった行だけ
// 出力が変わり、値が無いという事実が漏れる。
func (e *Engine) maskValue(m columnMask, v any) any {
	switch m.method {
	case config.MaskNone:
		return v
	case config.MaskNull:
		return nil
	case config.MaskRedact:
		return redacted
	case config.MaskHash:
		return e.hash(renderValue(v))
	case config.MaskPartial:
		return applyPartial(renderValue(v), m.partial)
	}
	// drop は Apply が列ごと落とすためここには来ない。未知の method も
	// New が弾く。どちらにせよ素通りさせず伏せる。
	return redacted
}

// hash は salt 付きの sha256 の先頭 hashLength 文字を返す。
//
// salt は Engine が実行ごとに生成する。同じ実行内では同じ値が同じ
// ハッシュになるため件数集計や突き合わせができ、実行をまたぐとできない
// （ADR-0003 §9）。
func (e *Engine) hash(s string) string {
	h := sha256.New()
	h.Write(e.salt)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:hashLength]
}

// applyPartial は残す範囲だけを残して伏せる。
func applyPartial(s string, spec partialSpec) string {
	if spec.domain {
		// メールアドレスの @ より後ろを残す。@ が無ければドメインは残さない。
		// 「ドメインらしき部分」を推測すると、推測が外れた分だけ値が出る。
		if at := strings.LastIndex(s, "@"); at >= 0 {
			return keepEnds(s[:at], spec.prefix, spec.suffix) + s[at:]
		}
	}
	return keepEnds(s, spec.prefix, spec.suffix)
}

// keepEnds は先頭 prefix 文字と末尾 suffix 文字を残し、間を伏せる。
//
// 残す文字数が値の長さ以上なら全体を伏せる。keep_prefix: 4 に 3 文字の値が
// 来たときに素の値がそのまま出るのを防ぐ。
func keepEnds(s string, prefix, suffix int) string {
	if prefix == 0 && suffix == 0 {
		return redacted
	}
	rs := []rune(s)
	if prefix+suffix >= len(rs) {
		return redacted
	}
	return string(rs[:prefix]) + redacted + string(rs[len(rs)-suffix:])
}

// renderValue は値をマスクに掛ける前の文字列にする。
//
// 数値が json.Number で来るのは redash.Client が json.Decoder の UseNumber を
// 立てているため。float64 に落ちると 2^53 を超える id の桁が落ち、
// 同じ値が同じハッシュにならなくなる。
func renderValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprintf("%v", v)
}
