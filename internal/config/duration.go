package config

import (
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration は "300s" / "1s" のような文字列で書ける time.Duration。
//
// ゼロ値は未指定を意味する。
type Duration time.Duration

// Duration は time.Duration に戻す。
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

func (d Duration) String() string {
	return time.Duration(d).String()
}

// UnmarshalYAML は time.ParseDuration が受け付ける文字列をデコードする。
//
// n が null の場合この関数は呼ばれず、値は未指定のまま残る。
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration は \"300s\" のような文字列で指定してください: %w", n.Line, err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: duration として読めません %q。\"300s\" / \"1s\" / \"1m30s\" のように単位を付けてください", n.Line, s)
	}
	if v < 0 {
		return fmt.Errorf("line %d: duration に負の値は指定できません %q", n.Line, s)
	}
	*d = Duration(v)
	return nil
}
