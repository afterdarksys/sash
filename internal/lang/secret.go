package lang

import (
	"fmt"
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
