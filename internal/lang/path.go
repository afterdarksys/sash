package lang

// The path type (docs/plans/2026-11-22-sash-language-v7.md, item 1).
//
// A path is a value kind of its own: absolute, clean (no ".", "..", "//"
// or trailing "/", except "/" itself), without NUL or control characters,
// at most pathMax bytes. Those invariants are checked once, when the path
// is made (let p = path WORD, declare -P, path(...)), and every way of
// deriving a new path keeps them: a join adds exactly one component, which
// cannot be empty, ".", ".." or hold a "/", so a join never climbs out of
// the path it starts from.
//
// Threats: traversal by string concatenation ("/srv/" + $user_input), a
// relative path that changes meaning after cd, and a string from host data
// used as a file verb's target. The first two are type errors here; the
// third is refused in a script by bindPaths (exit 2 type path), since a
// path argument there must be literal text or a path value. A path is
// never a grant: every file verb still checks its resolved path against
// the grant's roots, as before.

import (
	"fmt"
	"path"
	"strings"
)

// pathMax is the longest path, in bytes (Linux PATH_MAX).
const pathMax = 4096

type pathV string

func pathErr(reason string) error { return fmt.Errorf("path: %s", reason) }

// pathTextOK refuses NUL, control characters and over-long text.
func pathTextOK(s string) error {
	if len(s) > pathMax {
		return pathErr("too long")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return pathErr("control character")
		}
	}
	return nil
}

// makePath makes a path from text. Absolute text must already be clean.
// Relative text (optionally after one "./") is joined to prefix, and must
// be clean and not climb with "..". Without a prefix it is prefix_unset.
func makePath(prefix, s string) (pathV, error) {
	if s == "" {
		return "", pathErr("empty")
	}
	if err := pathTextOK(s); err != nil {
		return "", err
	}
	if strings.HasPrefix(s, "/") {
		if path.Clean(s) != s {
			return "", pathErr("not clean")
		}
		return pathV(s), nil
	}
	rel := strings.TrimPrefix(s, "./")
	if rel == "" || path.Clean(rel) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", pathErr("not clean")
	}
	if prefix == "" {
		return "", fmt.Errorf("prefix_unset")
	}
	full := path.Join(prefix, rel)
	if err := pathTextOK(full); err != nil {
		return "", err
	}
	return pathV(full), nil
}

// joinPath adds one component to p.
func joinPath(p pathV, part any) (pathV, error) {
	s, ok := part.(string)
	if !ok || s == "" || s == "." || s == ".." || strings.Contains(s, "/") {
		return "", pathErr("join component")
	}
	if err := pathTextOK(s); err != nil {
		return "", pathErr("join component")
	}
	full := string(p) + "/" + s
	if p == "/" {
		full = "/" + s
	}
	if len(full) > pathMax {
		return "", pathErr("too long")
	}
	return pathV(full), nil
}

// pathField is $p.name, $p.dir, $p.ext or $p.parts.
func pathField(p pathV, key any) (any, error) {
	k, _ := key.(string)
	switch k {
	case "name":
		if p == "/" {
			return "", nil
		}
		return path.Base(string(p)), nil
	case "dir":
		return pathV(path.Dir(string(p))), nil
	case "ext":
		if p == "/" {
			return "", nil
		}
		return path.Ext(path.Base(string(p))), nil
	case "parts":
		out := []any{}
		if p != "/" {
			for _, c := range strings.Split(string(p)[1:], "/") {
				out = append(out, c)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("type expr path field %s", textOf(key))
}

// pathStarts compares whole components: /etc/nginx2 does not start with
// /etc/nginx.
func pathStarts(p, root pathV) bool {
	return root == "/" || p == root || strings.HasPrefix(string(p), string(root)+"/")
}

// fnPath is path(x): a path from text (relative to the prefix), or a path
// unchanged.
func fnPath(a []any, c *evalCtx) (any, error) {
	switch x := a[0].(type) {
	case pathV:
		return x, nil
	case string:
		return makePath(c.prefix, x)
	}
	return nil, fnErr("path", a[0])
}

// pathLiteralOK refuses a glob in a path literal: a glob is not a path,
// and globs stay words that expand before the run.
func pathLiteralOK(w Word) error {
	for _, part := range w.Parts {
		if !part.Exp && part.X == nil && strings.ContainsAny(part.Lit, "*?") {
			return fmt.Errorf("parse: a glob is not a path")
		}
	}
	return nil
}

// pathOnly reports a path value (a secret path counts: the secret rule
// refuses it on its own).
func pathOnly(d any) bool {
	if s, ok := d.(secretVal); ok {
		d = s.d
	}
	_, ok := d.(pathV)
	return ok
}

// runLetPath binds let x = path WORD and declare -P x = WORD. A [ ]
// expression may yield a path or text; any other word is expanded to text.
// Either way text becomes a path only through makePath. A secret word is
// refused, as for any word.
func (m *machine) runLetPath(s *Let) (int, bool) {
	fail := func(err error) (int, bool) {
		m.err.WriteString(err.Error() + "\n")
		return exitType, true
	}
	var d any
	if x := exprOf(*s.Word); x != nil {
		v, err := m.evalX(x)
		if err != nil {
			return fail(err)
		}
		d = v
	} else {
		m.sawSecret = false
		text, err := m.expand(*s.Word)
		if err == nil && m.sawSecret {
			err = fmt.Errorf("a secret in a word: build it inside [ ]")
		}
		if err != nil {
			return fail(err)
		}
		d = text
	}
	inner, secret := unsecret(d)
	var p pathV
	switch x := inner.(type) {
	case pathV:
		p = x
	case string:
		var err error
		if p, err = makePath(m.prefix(), x); err != nil {
			return fail(err)
		}
	default:
		return fail(fmt.Errorf("type path %s", typeName(inner)))
	}
	if secret {
		return m.bindLet(s.Name, value{kind: valData, d: secretVal{p}})
	}
	return m.bindLet(s.Name, value{kind: valData, d: p})
}

// pathArgsTyped refuses, in a script, a file verb's path argument holding
// text from host data: wrap it with path(...) or join it onto a path,
// which checks it. At the prompt, words keep working as before.
func (m *machine) pathArgsTyped(st *expStage) (int, *problem) {
	if m.prompt {
		return -1, nil
	}
	var idx []int
	switch st.argv[0] {
	case "cat":
		for i := 1; i < len(st.argv); i++ {
			idx = append(idx, i)
		}
	case "ls", "cd", "pushd", "template":
		idx = []int{1}
	case "config":
		idx = []int{2}
	case "template_file":
		idx = []int{1, 2, 3}
	}
	for _, i := range idx {
		if i < len(st.argData) && st.argData[i] {
			return i, &problem{exitType, "type path"}
		}
	}
	return -1, nil
}
