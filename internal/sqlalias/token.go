package sqlalias

import (
	"fmt"
	"strings"
)

// kind は token の種類。
type kind int

const (
	// kindIdent は引用符の無い識別子。キーワード・関数名・型名もここに入る。
	kindIdent kind = iota
	// kindQuoted は引用識別子（"..." / `...` / [...]）。キーワードとしては読まない。
	kindQuoted
	// kindLiteral は文字列リテラルと数値。中身は使わないため保持しない。
	kindLiteral
	// kindPunct は記号1文字。:: や <= のような複数文字の演算子は1文字ずつに分かれる。
	// 見るのは , ( ) . * ; だけなので差し支えない。
	kindPunct
)

// token は tokenize が返す字句1つ。
type token struct {
	kind kind
	// text は kindIdent なら綴りそのまま、kindQuoted なら引用符を外して
	// エスケープを解いた中身、kindPunct なら記号1文字。kindLiteral では空。
	//
	// リテラルの中身を捨てるのは、値が列名の由来になることはなく、
	// 持ち回ると SQL 本文の一部（機微な値を含みうる）がエラー文言や
	// ログに出る経路になるため。
	text string
	// depth はこの token を囲む括弧の深さ。開き括弧と閉じ括弧はどちらも
	// 外側の深さを持つ。f(a, b) なら f と ( と ) が 0、a と , と b が 1。
	depth int
}

// tokenize は SQL を字句に分解する。
//
// 読めない構文（閉じていない引用符・コメント・括弧、方言で終端が変わる
// 文字列リテラル）はエラーにする。読み飛ばして続けると、以降の AS や
// カンマの位置がずれた別名マップができ、マスクが外れたことに気付く
// 手掛かりが無くなる（.claude/rules/go-architecture.md の
// 「判定できなかった」を「問題なし」に倒さない）。
func tokenize(sql string) ([]token, error) {
	var out []token
	depth := 0

	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case isSpace(c):
			i++

		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			// 行コメント。改行か終端まで。
			if nl := strings.IndexByte(sql[i:], '\n'); nl >= 0 {
				i += nl + 1
			} else {
				i = len(sql)
			}

		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			// ブロックコメント。入れ子は扱わない方言が多数派なので最初の */ で閉じる。
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("ブロックコメント /* が閉じていません（位置 %d）", i)
			}
			i += 2 + end + 2

		case c == '\'':
			next, err := scanString(sql, i)
			if err != nil {
				return nil, err
			}
			// E'...' や r'...' の接頭辞は、直前の1文字の識別子ではなく
			// リテラルの一部として扱う。列名の候補に E が混ざらないようにする。
			out = append(trimLiteralPrefix(out, sql, i), token{kind: kindLiteral, depth: depth})
			i = next

		case c == '"' || c == '`' || c == '[':
			text, next, err := scanQuoted(sql, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: kindQuoted, text: text, depth: depth})
			i = next

		case c == '$':
			tag, ok := dollarTag(sql, i)
			if !ok {
				// $1 のようなプレースホルダ。記号として扱う。
				out = append(out, token{kind: kindPunct, text: "$", depth: depth})
				i++
				break
			}
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				return nil, fmt.Errorf("dollar-quote %s が閉じていません（位置 %d）", tag, i)
			}
			out = append(out, token{kind: kindLiteral, depth: depth})
			i += len(tag) + end + len(tag)

		case isIdentStart(c):
			j := i + 1
			for j < len(sql) && isIdentPart(sql[j]) {
				j++
			}
			out = append(out, token{kind: kindIdent, text: sql[i:j], depth: depth})
			i = j

		case isDigit(c):
			// 数値。1.5 / 1e10 / 0x1f をまとめて1つのリテラルにする。
			// 中身は使わないため、指数の符号が別の記号に分かれても構わない。
			j := i
			for j < len(sql) && (isDigit(sql[j]) || isLetter(sql[j]) || sql[j] == '.') {
				j++
			}
			out = append(out, token{kind: kindLiteral, depth: depth})
			i = j

		case c == '(':
			out = append(out, token{kind: kindPunct, text: "(", depth: depth})
			depth++
			i++

		case c == ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("閉じ括弧 ) が多すぎます（位置 %d）", i)
			}
			out = append(out, token{kind: kindPunct, text: ")", depth: depth})
			i++

		default:
			out = append(out, token{kind: kindPunct, text: string(c), depth: depth})
			i++
		}
	}

	if depth != 0 {
		return nil, fmt.Errorf("開き括弧 ( が %d 個閉じていません", depth)
	}
	return out, nil
}

// trimLiteralPrefix は文字列リテラルの直前に接している1文字の識別子を落とす。
//
// E（PostgreSQL のエスケープ文字列）/ r（raw）/ b, x（バイト列）/ n, u
// （Unicode）は、方言によってリテラルの接頭辞になる。引用符に接した1文字
// でなければ落とさないため、列名 e と 'x' が並ぶ形（e = 'x' 等）には効かない。
func trimLiteralPrefix(out []token, sql string, quote int) []token {
	if quote == 0 || len(out) == 0 {
		return out
	}
	last := out[len(out)-1]
	if last.kind != kindIdent || len(last.text) != 1 || last.text[0] != sql[quote-1] {
		return out
	}
	switch fold(last.text) {
	case "e", "r", "b", "n", "u", "x":
		return out[:len(out)-1]
	}
	return out
}

// scanString は単一引用符で始まる文字列リテラルを読み、終端の次の位置を返す。
//
// 引用符を2つ並べたものが引用符そのものを表す（標準 SQL）。バックスラッシュを打ち消しとして
// 読むかは方言で分かれる（PostgreSQL の standard_conforming_strings は読まず、
// MySQL / BigQuery は読む）。sumiq は Redash が繋ぐ方言を知らないため、
// PostgreSQL が明示的にエスケープ文字列を表す E'...' の形でだけ打ち消しとして扱う。
//
// それ以外で「奇数個のバックスラッシュの直後の '」に出会ったら、どちらの
// 方言かで文字列の終端が変わる。読めたことにせずエラーにする。偶数個
// （'a\\' のような打ち消し済みの並び）はどちらの方言でも同じ位置で終わるため通す。
func scanString(sql string, i int) (int, error) {
	// E'...' / e'...' は直前の1文字だけを見る。E が識別子の末尾（table_e など）で
	// ないことを、その前の文字が識別子の一部でないことで確かめる。
	escapes := i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') &&
		(i == 1 || !isIdentPart(sql[i-2]))

	backslashes := 0
	for j := i + 1; j < len(sql); {
		switch sql[j] {
		case '\\':
			backslashes++
			j++
		case '\'':
			switch {
			case backslashes%2 == 1 && escapes:
				// E'\'' の打ち消し。引用符は中身の一部。
				backslashes = 0
				j++
			case backslashes%2 == 1:
				return 0, fmt.Errorf("文字列リテラルの \\' を解釈できません（位置 %d）。"+
					"バックスラッシュを打ち消しとして読むかは方言で違うため、"+
					"引用符は '' で表すか、E'...' の形で書いてください", j)
			case j+1 < len(sql) && sql[j+1] == '\'':
				// 引用符2つは引用符そのもの。
				j += 2
			default:
				return j + 1, nil
			}
		default:
			backslashes = 0
			j++
		}
	}
	return 0, fmt.Errorf("文字列リテラル ' が閉じていません（位置 %d）", i)
}

// scanQuoted は引用識別子を読み、中身と終端の次の位置を返す。
//
// 閉じ記号を2つ並べたものが閉じ記号そのものを表す。
//
// MySQL で ANSI_QUOTES が無効なとき "..." は文字列リテラルだが、識別子として
// 読む方に倒す。中身が列名の候補に増えるだけで、伝播は過剰側に振れる。
func scanQuoted(sql string, i int) (string, int, error) {
	open := sql[i]
	closer := open
	if open == '[' {
		closer = ']'
	}

	var b strings.Builder
	for j := i + 1; j < len(sql); {
		if sql[j] != closer {
			b.WriteByte(sql[j])
			j++
			continue
		}
		if j+1 < len(sql) && sql[j+1] == closer {
			b.WriteByte(closer)
			j += 2
			continue
		}
		return b.String(), j + 1, nil
	}
	return "", 0, fmt.Errorf("引用識別子 %c が閉じていません（位置 %d）", open, i)
}

// dollarTag は sql[i] == '$' から始まる dollar-quote の開始タグを返す。
//
// PostgreSQL のタグは省略可能で、書く場合は文字か _ で始まる。$1 のような
// 数値のプレースホルダはタグにならない。
func dollarTag(sql string, i int) (string, bool) {
	j := i + 1
	if j < len(sql) && sql[j] == '$' {
		return "$$", true
	}
	if j >= len(sql) || !(isLetter(sql[j]) || sql[j] == '_') {
		return "", false
	}
	for j < len(sql) && (isLetter(sql[j]) || isDigit(sql[j]) || sql[j] == '_') {
		j++
	}
	if j < len(sql) && sql[j] == '$' {
		return sql[i : j+1], true
	}
	return "", false
}

func isSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\f', '\v':
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isIdentStart は識別子の先頭になれる文字かを返す。
//
// 0x80 以上を通すのは、列名に非 ASCII が入るため（顧客メールアドレス 等）。
// UTF-8 の後続バイトも識別子の一部として読み進めるだけなので、
// コードポイント単位に分解する必要はない。
func isIdentStart(c byte) bool {
	return isLetter(c) || c == '_' || c >= 0x80
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}
