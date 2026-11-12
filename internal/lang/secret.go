package lang

import (
	"fmt"
	"sort"
	"strings"
)

// A secret (declare -s) is a value that never leaves sash in v4: it renders
// as *** everywhere, anything computed from it is a secret too, and a verb
// argument, a redirect, or a template that would carry one is refused. The
// agent's file.write has no mode, so a rendered secret would land in a file
// readable by others; writing one waits for the agent contract.
//
// A secret decides nothing: a condition that reads one (if, while, do,
// filter, check, a template if) is refused, since a branch per guess would
// spell the value out. An error in an evaluation that read a secret ends
// the line and try cannot catch it, so errors cannot probe it either.

// errSecretFault ends the line: an evaluation that read a secret failed.
var errSecretFault = fmt.Errorf("a secret ended the line")

// errSecretBranch refuses a condition computed from a secret.
var errSecretBranch = fmt.Errorf("a secret cannot decide a branch")

// DeclaresSecret is true when line may bind a declare -s literal. History
// never carries such a line: !! and !$ refuse it, and the editor does not
// keep it. The answer comes from what a run binds: the line after brace
// expansion, parsed, with each declare's flags as the parser reads them, so
// -{s,r}, "-s", and -rs all count. A line that does not parse alone (a
// fragment of a block) falls back to matching words, with and without
// braces as separators. A false match only refuses; it never leaks.
func DeclaresSecret(line string) bool {
	text := line
	if exp, prob := expandBraces(line); prob == nil && len(exp) <= expandMax {
		text = exp
	}
	if prog, err := parse(text); err == nil {
		found := false
		needsEachStmt(prog, func(s Stmt) {
			if d, ok := s.(*Declare); ok && strings.Contains(d.Flags, "s") {
				found = true
			}
		})
		if found {
			return true
		}
	}
	for _, t := range []string{text, line} {
		if declaresSecretWords(t, "|&;<>()=") || declaresSecretWords(t, "|&;<>(){}=") {
			return true
		}
	}
	return false
}

func declaresSecretWords(line, seps string) bool {
	line = strings.NewReplacer("'", "", "\"", "", "\\", "").Replace(line)
	words := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || strings.ContainsRune(seps, r)
	})
	declare := false
	for _, w := range words {
		if w == "declare" {
			declare = true
			continue
		}
		if declare && strings.HasPrefix(w, "-") && strings.ContainsRune(w, 's') {
			return true
		}
	}
	return false
}

type secretVal struct{ d any }

// decide is truthy for a condition, refusing a secret.
func decide(d any) (bool, error) {
	if isSecret(d) {
		return false, errSecretBranch
	}
	return truthy(d), nil
}

const redacted = "***"

func isSecret(d any) bool {
	_, ok := d.(secretVal)
	return ok
}

// unsecret is the inner value, for computing with it. The caller marks its
// result secret.
func unsecret(d any) (any, bool) {
	if s, ok := d.(secretVal); ok {
		return s.d, true
	}
	return d, false
}

// PathWords are the literal words of line that name a path: a plain word
// or redirect target with a "/" and no "*", as written. The editor keeps
// them as seen paths for Tab. It lexes only; a line that does not lex has
// none.
func PathWords(line string) []string {
	toks, err := lex(line)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range toks {
		if t.kind != kWord {
			continue
		}
		if w := plain(t.word); strings.Contains(w, "/") && !strings.Contains(w, "*") {
			out = append(out, w)
		}
	}
	return out
}

// LexOK reports whether src lexes: no open quote, no bad $ form. Tab
// completes only when the line up to the cursor does, so it never works
// inside a quote whose escaping it would have to guess.
func LexOK(src string) bool {
	_, err := lex(src)
	return err == nil
}

// InsertedWords is how line reads over [from, to), the span Tab just
// filled: the word tokens there, each as its literal or as $name. ok is
// false unless the line lexes and the span is exactly whole word tokens,
// each one plain literal or one bare $name, so an insert that opened a
// quote, a comment, a pipe, a stage, a redirect, an expression, or glued
// to a neighbour is refused.
func InsertedWords(line string, from, to int) ([]string, bool) {
	toks, err := lex(line)
	if err != nil {
		return nil, false
	}
	var out []string
	first, last := -1, -1
	for _, t := range toks {
		if t.end <= from || t.pos >= to || t.kind == kEOF {
			continue
		}
		if t.kind != kWord || len(t.word.Parts) != 1 || t.word.Parts[0].X != nil {
			return nil, false
		}
		if first < 0 {
			first = t.pos
		}
		last = t.end
		part := t.word.Parts[0]
		if part.Exp {
			out = append(out, "$"+part.Lit)
			continue
		}
		// A literal that spells $, a comment, a stage, or a glob is
		// refused even quoted: it reads as one of those to a person, and
		// "$x" here must only ever mean a reference.
		if strings.ContainsAny(part.Lit, "$#;|*?`\n\r") || needsCell(part.Lit) != part.Lit {
			return nil, false
		}
		out = append(out, part.Lit)
	}
	if first != from || last != to {
		return nil, false
	}
	return out, true
}

// Reserved reports a name the language keeps: a keyword, a builtin, or a
// row tool. The editor refuses it as an abbreviation name.
func Reserved(name string) bool { return reserved(name) }

// LiteralWords is line as plain literal words, or ok false when it does
// not lex or holds anything else: a $ reference, a [ ] expression, an
// operator, a redirect, or a separator. The editor reads abbr through it.
func LiteralWords(line string) ([]string, bool) {
	toks, err := lex(line)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, t := range toks {
		switch {
		case t.kind == kEOF:
		case t.kind == kWord && len(t.word.Parts) == 1 && !t.word.Parts[0].Exp && t.word.Parts[0].X == nil:
			out = append(out, t.word.Parts[0].Lit)
		default:
			return nil, false
		}
	}
	return out, true
}

// CommandWords are the source spans [start, end) of every stage's
// command word in line, in source order, fn bodies included. A line that
// does not parse has none.
func CommandWords(line string) [][2]int {
	prog, err := parse(line)
	if err != nil {
		return nil
	}
	var out [][2]int
	needsEachPipe(prog, func(p Pipeline) {
		for _, st := range p.Stages {
			if len(st.Argv) > 0 {
				out = append(out, [2]int{st.Argv[0].Pos, st.Argv[0].End})
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
