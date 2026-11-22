package lang

import (
	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// MoveTo is cd to dir for the prompt's z and jump (line/dirs.go): the same
// path invariants as a path value, the same grant check and the same stat
// as cd, without making a line of source from a stored value. A session
// that is recording refuses, since its replay runs lines and this is not
// one.
func MoveTo(dir string, g grant.Grant, p parent.Parent, opt Options) Outcome {
	fail := func(status int, msg string) Outcome {
		return Outcome{Status: status, Stderr: msg + "\n", Prefix: opt.Prefix}
	}
	if opt.State != nil && opt.State.Recording() {
		return fail(exitType, "z: not while recording")
	}
	full, err := makePath("", dir)
	if err != nil {
		return fail(exitType, err.Error())
	}
	if !g.Has("file.read") || !g.AllowsRead(string(full)) {
		return fail(exitDenied, "denied file.read")
	}
	m := newMachine(g, p, opt.State)
	m.prompt = opt.Prompt
	if opt.Prefix != "" {
		m.stack = []string{opt.Prefix}
	}
	_, code := m.move(expStage{argv: []string{"cd", string(full)}, path: string(full), key: "file.read"})
	out := Outcome{Status: code, Stderr: m.err.String(), Prefix: m.prefix()}
	return out
}
