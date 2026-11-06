package lang

import (
	"fmt"
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

// Part is a literal or a $name / $name.field reference.
type Part struct {
	Lit string
	Exp bool
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
	i := 0
	for i < len(src) {
		c := src[i]
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
		case '$':
			flush()
			name, n, err := lexExpand(src[i:])
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

func lexBare(src string) (Word, int, error) {
	if src[0] == '$' {
		name, n, err := lexExpand(src)
		if err != nil {
			return Word{}, 0, err
		}
		return Word{Parts: []Part{{Lit: name, Exp: true}}}, n, nil
	}
	i := 0
	for i < len(src) && !bareStop(src[i]) {
		i++
	}
	if i == 0 {
		return Word{}, 0, fmt.Errorf("lex: empty word")
	}
	return Word{Parts: []Part{{Lit: src[:i]}}}, i, nil
}

func bareStop(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '#', ';', '{', '}', '(', ')', '|', '&', '>', '<', '=', '!', '\'', '"', ',':
		return true
	default:
		return false
	}
}

func lexExpand(src string) (string, int, error) {
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
	if i < len(src) && src[i] == '.' {
		j := i + 1
		if j >= len(src) || !isNameStart(rune(src[j])) {
			return "", 0, fmt.Errorf("lex: bad field")
		}
		j++
		for j < len(src) && isName(rune(src[j])) {
			j++
		}
		i = j
	}
	return src[1:i], i, nil
}

func isNameStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isName(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
