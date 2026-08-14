package config

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaskMethod は列に適用するマスク方法。
type MaskMethod string

const (
	// MaskNone はマスクしない。allowlist 運用で特定の列を通すための明示指定。
	MaskNone MaskMethod = "none"
	// MaskPartial は一部を残す。
	MaskPartial MaskMethod = "partial"
	// MaskHash は sha256(salt + value) の先頭 N 文字に置換する。
	MaskHash MaskMethod = "hash"
	// MaskNull は NULL に置換する。
	//
	// YAML では引用符なしの null は空値として解釈されるため、
	// 設定ファイルには method: "null" と引用して書く必要がある。
	MaskNull MaskMethod = "null"
	// MaskRedact は **** に置換する。
	MaskRedact MaskMethod = "redact"
	// MaskDrop は列ごと出力しない。
	MaskDrop MaskMethod = "drop"
)

func maskMethods() []MaskMethod {
	return []MaskMethod{MaskNone, MaskPartial, MaskHash, MaskNull, MaskRedact, MaskDrop}
}

// UnmarshalYAML は不正な値を行番号付きで弾く。
func (m *MaskMethod) UnmarshalYAML(n *yaml.Node) error {
	return unmarshalEnum(n, "method", maskMethods(), m)
}

// Action はどのルールにもマッチしなかった列の扱い。
type Action string

const (
	// ActionNone はマッチした列だけをマスクする denylist 運用。
	ActionNone Action = "none"
	// ActionRedact はマッチしなかった列も **** にする allowlist 運用。
	ActionRedact Action = "redact"
)

func actions() []Action {
	return []Action{ActionNone, ActionRedact}
}

// UnmarshalYAML は不正な値を行番号付きで弾く。
func (a *Action) UnmarshalYAML(n *yaml.Node) error {
	return unmarshalEnum(n, "default_action", actions(), a)
}

// OnExceed は max_rows を超えたときの挙動。
type OnExceed string

const (
	// OnExceedError はエラーにして何も出力しない。
	OnExceedError OnExceed = "error"
	// OnExceedTruncate は max_rows 件で切り詰めて出力する。
	OnExceedTruncate OnExceed = "truncate"
)

func onExceeds() []OnExceed {
	return []OnExceed{OnExceedError, OnExceedTruncate}
}

// UnmarshalYAML は不正な値を行番号付きで弾く。
func (o *OnExceed) UnmarshalYAML(n *yaml.Node) error {
	return unmarshalEnum(n, "on_exceed", onExceeds(), o)
}

// Format は出力形式。
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
)

func formats() []Format {
	return []Format{FormatTable, FormatJSON, FormatCSV}
}

// UnmarshalYAML は不正な値を行番号付きで弾く。
func (f *Format) UnmarshalYAML(n *yaml.Node) error {
	return unmarshalEnum(n, "format", formats(), f)
}

// unmarshalEnum は列挙値のデコードと検証をまとめて行う。
//
// n が null の場合この関数は呼ばれない。yaml は null をデコード対象の
// ゼロ値として扱い、Unmarshaler を経由しないため。空値の検証は validate で行う。
func unmarshalEnum[T ~string](n *yaml.Node, field string, allowed []T, dst *T) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: %s: 文字列として読めません: %w", n.Line, field, err)
	}
	for _, a := range allowed {
		if T(s) == a {
			*dst = a
			return nil
		}
	}
	return fmt.Errorf("line %d: %s: 不正な値 %q。指定できるのは %s", n.Line, field, s, quotedList(allowed))
}

func quotedList[T ~string](values []T) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", string(v))
	}
	return strings.Join(quoted, " / ")
}
