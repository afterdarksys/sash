package lang

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type kind int

const (
	kEOF kind = iota
	kNL
	kWord
	kPipe
	kRow
	kAnd
	kOr
	kSemi
	kGT
	kLT
	kHere
	kLBrace
	kRBrace
	kLParen
	kRParen
	kEq
)

type token struct {
	kind kind
	word Word
	pos  int
	end  int
}

// Word is one shell word. Expansion happens later, so a value is never parsed as syntax.
// Pos and End are byte offsets into the source, covering quotes.
type Word struct {
	Parts []Part
	Pos   int
	End   int
}

// Part is a literal, a $name reference with an access path ($cfg.server.port,
// $list[0], $m["key"]), or a [ ] expression parsed at lex time (X).
type Part struct {
	Lit string
	Exp bool
	X   Expr
}

func (w Word) empty() bool { return len(w.Parts) == 0 }

func emit(out *[]token, k kind, w Word, at, n int) {
	if k == kWord {
		w.Pos = at
		w.End = at + n
	}
	*out = append(*out, token{kind: k, word: w, pos: at, end: at + n})
}

func lex(src string) ([]token, error) {
	var out []token
	ix := newExprSpans(src)
	i := 0
	for i < len(src) {
		c := src[i]
		if n := ix.span(src, i); n > 0 {
			x, err := parseExpr(src[i+1 : i+n-1])
			if err != nil {
				return nil, err
			}
			emit(&out, kWord, Word{Parts: []Part{{X: x}}}, i, n)
			i += n
			continue
		}
		switch c {
		case ' ', '\t', '\r':
			i++
			continue
		case '\n':
			emit(&out, kNL, Word{}, i, 1)
			i++
			continue
		case '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case ';':
			emit(&out, kSemi, Word{}, i, 1)
			i++
		case '{':
			emit(&out, kLBrace, Word{}, i, 1)
			i++
		case '}':
			emit(&out, kRBrace, Word{}, i, 1)
			i++
		case '(':
			emit(&out, kLParen, Word{}, i, 1)
			i++
		case ')':
			emit(&out, kRParen, Word{}, i, 1)
			i++
		case '|':
			if i+1 < len(src) && src[i+1] == '>' {
				emit(&out, kRow, Word{}, i, 2)
				i += 2
				continue
			}
			if i+1 < len(src) && src[i+1] == '|' {
				emit(&out, kOr, Word{}, i, 2)
				i += 2
				continue
			}
			emit(&out, kPipe, Word{}, i, 1)
			i++
		case ',':
			i++
			continue
		case '&':
			if i+1 < len(src) && src[i+1] == '&' {
				emit(&out, kAnd, Word{}, i, 2)
				i += 2
				continue
			}
			return nil, fmt.Errorf("lex: & is not in this series")
		case '>':
			if i+1 < len(src) && src[i+1] == '=' {
				emit(&out, kWord, Word{Parts: []Part{{Lit: ">="}}}, i, 2)
				i += 2
				continue
			}
			emit(&out, kGT, Word{}, i, 1)
			i++
		case '<':
			if i+2 < len(src) && src[i+1] == '<' && src[i+2] == '<' {
				emit(&out, kHere, Word{}, i, 3)
				i += 3
				continue
			}
			if i+1 < len(src) && src[i+1] == '=' {
				emit(&out, kWord, Word{Parts: []Part{{Lit: "<="}}}, i, 2)
				i += 2
				continue
			}
			emit(&out, kLT, Word{}, i, 1)
			i++
		case '=':
			if i+1 < len(src) && src[i+1] == '=' {
				emit(&out, kWord, Word{Parts: []Part{{Lit: "=="}}}, i, 2)
				i += 2
				continue
			}
			emit(&out, kEq, Word{}, i, 1)
			i++
		case '!':
			if i+1 < len(src) && src[i+1] == '=' {
				emit(&out, kWord, Word{Parts: []Part{{Lit: "!="}}}, i, 2)
				i += 2
				continue
			}
			return nil, fmt.Errorf("lex: bare !")
		case '\'':
			w, n, err := lexRaw(src[i:])
			if err != nil {
				return nil, err
			}
			emit(&out, kWord, w, i, n)
			i += n
		case '"':
			w, n, err := lexDouble(src[i:])
			if err != nil {
				return nil, err
			}
			emit(&out, kWord, w, i, n)
			i += n
		default:
			w, n, err := lexBare(src[i:])
			if err != nil {
				return nil, err
			}
			emit(&out, kWord, w, i, n)
			i += n
		}
	}
	emit(&out, kEOF, Word{}, len(src), 0)
	return out, nil
}

func lexRaw(src string) (Word, int, error) {
	if len(src) < 2 || src[0] != '\'' {
		return Word{}, 0, fmt.Errorf("lex: quote")
	}
	end := strings.IndexByte(src[1:], '\'')
	if end < 0 {
		return Word{}, 0, fmt.Errorf("lex: unclosed quote")
	}
	return Word{Parts: []Part{{Lit: src[1 : 1+end]}}}, 2 + end, nil
}

func lexDouble(src string) (Word, int, error) {
	var parts []Part
	i := 1
	lit := strings.Builder{}
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, Part{Lit: lit.String()})
			lit.Reset()
		}
	}
	for i < len(src) {
		switch src[i] {
		case '"':
			flush()
			return Word{Parts: parts}, i + 1, nil
		case '\\':
			// Same rule as scanQuote: a backslash hides the next byte, so
			// the parser and the expanders end the string at one place.
			if i+1 >= len(src) {
				return Word{}, 0, fmt.Errorf("lex: unclosed quote")
			}
			switch c := src[i+1]; c {
			case '"', '\\', '$':
				lit.WriteByte(c)
			default:
				lit.WriteByte('\\')
				lit.WriteByte(c)
			}
			i += 2
		case '$':
			flush()
			// Inside quotes a path takes [digits] only: a ["key"] there would
			// read past the closing quote the expanders see.
			name, n, err := lexExpandIn(src[i:], true)
			if err != nil {
				return Word{}, 0, err
			}
			parts = append(parts, Part{Lit: name, Exp: true})
			i += n
		default:
			lit.WriteByte(src[i])
			i++
		}
	}
	return Word{}, 0, fmt.Errorf("lex: unclosed quote")
}

// lexBare reads one bare word. $ references and literal text join until a
// stop byte, so $a$b and $a-$b are one word, as "$a$b" is.
func lexBare(src string) (Word, int, error) {
	var parts []Part
	i := 0
	for i < len(src) && !bareStop(src[i]) {
		if src[i] == '$' {
			name, n, err := lexExpand(src[i:])
			if err != nil {
				return Word{}, 0, err
			}
			parts = append(parts, Part{Lit: name, Exp: true})
			i += n
			continue
		}
		j := i
		for j < len(src) && !bareStop(src[j]) && src[j] != '$' {
			j++
		}
		parts = append(parts, Part{Lit: src[i:j]})
		i = j
	}
	if i == 0 {
		return Word{}, 0, fmt.Errorf("lex: empty word")
	}
	return Word{Parts: parts}, i, nil
}

func bareStop(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '#', ';', '{', '}', '(', ')', '|', '&', '>', '<', '=', '!', '\'', '"', ',':
		return true
	default:
		return false
	}
}

func lexExpand(src string) (string, int, error) { return lexExpandIn(src, false) }

func lexExpandIn(src string, quoted bool) (string, int, error) {
	if len(src) < 2 || src[0] != '$' {
		return "", 0, fmt.Errorf("lex: $")
	}
	i := 1
	if !isNameStart(rune(src[i])) {
		return "", 0, fmt.Errorf("lex: bad $")
	}
	i++
	for i < len(src) && isName(rune(src[i])) {
		i++
	}
	for i < len(src) {
		switch src[i] {
		case '.':
			j := i + 1
			if j >= len(src) || !isNameStart(rune(src[j])) {
				return "", 0, fmt.Errorf("lex: bad field")
			}
			j++
			for j < len(src) && isName(rune(src[j])) {
				j++
			}
			i = j
			continue
		case '[':
			n := pathIndexLen(src[i:])
			if quoted && n > 0 && src[i+1] == '"' {
				n = 0
			}
			if n == 0 {
				return src[1:i], i, nil
			}
			i += n
			continue
		}
		break
	}
	return src[1:i], i, nil
}

// pathIndexLen is the length of [123] or ["key"] at s[0], or 0.
func pathIndexLen(s string) int {
	if len(s) < 3 || s[0] != '[' {
		return 0
	}
	if s[1] == '"' {
		end := strings.IndexByte(s[2:], '"')
		if end < 0 || 2+end+1 >= len(s) || s[2+end+1] != ']' || strings.ContainsAny(s[2:2+end], "\\\n") {
			return 0
		}
		return 2 + end + 2
	}
	j := 1
	if s[j] == '-' {
		j++
	}
	start := j
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == start || j >= len(s) || s[j] != ']' {
		return 0
	}
	return j + 1
}

// pathSteps splits a $ reference into its name and access steps. The text
// comes from source, never from a value.
func pathSteps(ref string) (string, []any) {
	i := 0
	for i < len(ref) && ref[i] != '.' && ref[i] != '[' {
		i++
	}
	name := ref[:i]
	var steps []any
	for i < len(ref) {
		if ref[i] == '.' {
			j := i + 1
			for j < len(ref) && ref[j] != '.' && ref[j] != '[' {
				j++
			}
			steps = append(steps, ref[i+1:j])
			i = j
			continue
		}
		n := pathIndexLen(ref[i:])
		inner := ref[i+1 : i+n-1]
		if strings.HasPrefix(inner, `"`) {
			steps = append(steps, strings.Trim(inner, `"`))
		} else {
			k, _ := strconv.ParseInt(inner, 10, 64)
			steps = append(steps, k)
		}
		i += n
	}
	return name, steps
}

func isNameStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isName(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
