package lang

// A secret (declare -s) is a value that never leaves sash in v4: it renders
// as *** everywhere, anything computed from it is a secret too, and a verb
// argument, a redirect, or a template that would carry one is refused. The
// agent's file.write has no mode, so a rendered secret would land in a file
// readable by others; writing one waits for the agent contract.
//
// A condition on a secret ([ $tok == "x" ]) still decides a branch. That
// reveals what the branch does, as in any language; it does not print the
// value.

type secretVal struct{ d any }

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
