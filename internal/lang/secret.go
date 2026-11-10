package lang

import "fmt"

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
