package line

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
)

func abbrEditor(t *testing.T, g grant.Grant) *Editor {
	t.Helper()
	g.AllowPrivate = true
	e := New(g, failParent{t})
	e.Feed([]byte("abbr urs 'unit restart nginx'\r"))
	if e.note != "abbr urs" || e.abbrs["urs"] != "unit restart nginx" {
		t.Fatalf("define: note %q abbrs %v", e.note, e.abbrs)
	}
	return e
}

// Space after a command-position abbreviation rewrites it on the line,
// and the rewritten line gets the same Block preview as typed text.
func TestAbbrSpaceExpandsVisibly(t *testing.T) {
	e := abbrEditor(t, grant.Grant{})
	e.Feed([]byte("urs "))
	if e.line != "unit restart nginx " {
		t.Fatalf("line %q", e.line)
	}
	if v := e.View(); !v.Block || !v.Denied || v.Smile != "denied unit.restart" {
		t.Fatalf("preview %+v", v)
	}
	e.Feed([]byte("\r"))
	if e.Runs != 0 {
		t.Fatal("a denied expansion ran")
	}
	for line, want := range map[string]string{
		"ps; urs ":       "ps; unit restart nginx ",
		"if urs ":        "if unit restart nginx ",
		"echo urs ":      "echo urs ",
		"echo 'urs ":     "echo 'urs ",
		"ursx ":          "ursx ",
		"echo hi | urs ": "echo hi | unit restart nginx ",
	} {
		e.Feed([]byte{0x15})
		e.Feed([]byte(line))
		if e.line != want {
			t.Errorf("%q: %q want %q", line, e.line, want)
		}
	}
}

// Enter on an unexpanded abbreviation rewrites it and stops; a second
// Enter runs what is shown.
func TestAbbrEnterExpandsAndStops(t *testing.T) {
	e := abbrEditor(t, grant.Grant{Entitlements: []string{"unit.restart", "ps"}})
	p := &countParent{}
	e.parent = p
	e.Feed([]byte("ps; urs\r"))
	if e.line != "ps; unit restart nginx" || e.Runs != 0 || p.n != 0 {
		t.Fatalf("first Enter: line %q runs %d calls %d", e.line, e.Runs, p.n)
	}
	e.Feed([]byte("\r"))
	if e.Runs != 1 || p.n != 2 {
		t.Fatalf("second Enter: runs %d calls %d", e.Runs, p.n)
	}
}

// Expansion is one level: an abbreviation's text is not expanded again.
func TestAbbrNotRecursive(t *testing.T) {
	e := abbrEditor(t, grant.Grant{})
	e.Feed([]byte("abbr two 'urs now'\r"))
	e.Feed([]byte("two "))
	if e.line != "urs now " {
		t.Fatalf("line %q", e.line)
	}
	e.Feed([]byte{0x15})
	e.Feed([]byte("two\r"))
	if e.line != "urs now" {
		t.Fatalf("enter %q", e.line)
	}
}

// Neither the name nor the text can carry a separator, a pipe, a comment,
// a newline, an expansion, a quote, a control character, or a secret.
func TestAbbrRefusesSmuggling(t *testing.T) {
	e := New(grant.Grant{AllowPrivate: true}, failParent{t})
	for _, line := range []string{
		"abbr a 'ps; unit restart x'", "abbr a 'ps | egrep x'", "abbr a 'ps # x'", "abbr a 'ps && unit restart x'",
		"abbr a \"ps\nunit restart x\"", "abbr a 'echo $tok'", "abbr a 'echo [ 1 ]'", "abbr a \"cat 'x'\"",
		"abbr a 'declare -s t = x'", "abbr a 'cat x > /etc/y'", "abbr a 'echo \x1b[31m'", "abbr a 'echo \u202ex'",
		"abbr a 'ps  x'", "abbr a ' ps'", "abbr a ''", "abbr a '" + strings.Repeat("x", 257) + "'", "abbr a 'echo `id`'",
		"abbr ps 'echo'", "abbr unit 'echo'", "abbr if 'echo'", "abbr abbr 'echo'", "abbr bind 'echo'", "abbr exit 'echo'",
		"abbr a;b 'echo'", "abbr $x 'echo'", "abbr 'a b' 'echo'", "abbr a $x", "abbr a", "abbr a b c",
	} {
		e.Feed([]byte{0x15})
		e.line, e.cursor = line, len(line)
		e.Feed([]byte("\r"))
		if len(e.abbrs) != 0 {
			t.Fatalf("%q defined %v", line, e.abbrs)
		}
		if !strings.HasPrefix(e.note, "abbr") && !strings.HasPrefix(e.note, "usage abbr") {
			t.Errorf("%q: note %q", line, e.note)
		}
	}
	e.Feed([]byte("abbr ok 'unit status nginx'\r"))
	if e.abbrs["ok"] != "unit status nginx" {
		t.Fatalf("plain words refused: %q", e.note)
	}
	e.Feed([]byte("abbr -e ok\r"))
	if len(e.abbrs) != 0 || e.note != "abbr -e ok" {
		t.Fatalf("erase: %v %q", e.abbrs, e.note)
	}
}

// abbr never reaches lang: a script sees an unknown command (127), State,
// history, --needs, and --lint never hold one, and nothing calls the
// parent.
func TestAbbrIsEditorOnly(t *testing.T) {
	e := abbrEditor(t, grant.Grant{})
	if len(e.opt.State.Names()) != 0 || len(e.history) != 0 {
		t.Fatalf("state %v history %v", e.opt.State.Names(), e.history)
	}
	if out := lang.Run("abbr urs 'unit restart nginx'", grant.Grant{}, failParent{t}, lang.Options{}); out.Status != 127 || !strings.Contains(out.Stderr, "unknown abbr") {
		t.Fatalf("script %+v", out)
	}
	if out := lang.Run("urs", grant.Grant{}, failParent{t}, lang.Options{State: e.opt.State}); out.Status != 127 {
		t.Fatalf("a run sees the abbreviation: %+v", out)
	}
	if out := lang.Needs("abbr urs 'unit restart nginx'\nurs", lang.NeedsOptions{}); strings.Contains(out.Stdout, "unit.restart") {
		t.Fatalf("needs %+v", out)
	}
	if out := lang.Lint("abbr urs 'unit restart nginx'\nurs"); strings.Contains(out.Stdout, "unit") {
		t.Fatalf("lint %+v", out)
	}
	e.Feed([]byte("abbr\r"))
	if e.note != "abbr urs" {
		t.Fatalf("list %q", e.note)
	}
}
