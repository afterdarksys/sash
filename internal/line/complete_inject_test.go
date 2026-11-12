package line

import (
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
)

func srvGrant() grant.Grant {
	return grant.Grant{Entitlements: []string{"file.read", "unit.restart"}, FileRead: []string{"/srv"}, AllowPrivate: true}
}

// plant puts p in the seen list without see's filter, as if a host name
// had got past it.
func plant(e *Editor, p string) {
	if e.seenSet == nil {
		e.seenSet = map[string]bool{}
	}
	e.seenSet[p] = true
	e.seen = append(e.seen, p)
}

// The review repro: Tab inside an open quote must not turn a host file
// name into a stage. Every variant leaves the line and the cursor as
// they were, and Tab offers nothing inside a quote.
func TestTabInsideQuoteInjectsNothing(t *testing.T) {
	cases := []struct{ line, path string }{
		{"cat 'q x", "/srv/x';unit restart nginx;'b"},
		{"cat 'q x", "/srv/x';unit restart nginx;echo '"},
		{"cat 'q x", "/srv/x y"},
		{"cat \"q x", "/srv/x\";unit restart nginx;\"b"},
		{"cat \"q x", "/srv/x y"},
		{"cat \"a\\\" x", "/srv/x y"},
		{"echo \"$", "/srv/x"},
		{"echo 'a $", "/srv/x"},
	}
	for _, c := range cases {
		e := New(srvGrant(), failParent{t})
		e.opt.Prefix = "/srv"
		plant(e, c.path)
		e.line, e.cursor = c.line, len(c.line)
		if rows, _, _ := e.complete(); len(rows) != 0 {
			t.Errorf("%q: offered %+v inside a quote", c.line, rows)
		}
		e.Feed([]byte("\t\t\x1b[C"))
		if e.line != c.line || e.cursor != len(c.line) {
			t.Errorf("%q with %q: line became %q", c.line, c.path, e.line)
		}
	}
}

// The line the bug produced is Block: unit restart paints as a stage, and
// the unknown command after it stops Enter.
func TestInjectedLineIsBlock(t *testing.T) {
	for _, line := range []string{
		"cat 'q \"x';unit restart nginx;'b\"' ",
		"cat 'q x';unit restart nginx;nosuch",
	} {
		pv := lang.Preview(line, srvGrant(), lang.Options{Prefix: "/srv"})
		if !pv.Block {
			t.Errorf("%q: not Block %+v", line, pv)
		}
	}
	if pv := lang.Preview("nosuch; echo hi", grant.Grant{}, lang.Options{}); !pv.Block || pv.Smile != "unknown nosuch" {
		t.Fatalf("unknown %+v", pv)
	}
	if pv := lang.Preview("use /srv/lib.sash; helper", srvGrant(), lang.Options{}); pv.Block {
		t.Fatalf("a use may define the name: %+v", pv)
	}
	if pv := lang.Preview("fn helper() { echo hi }; helper", srvGrant(), lang.Options{}); pv.Block {
		t.Fatalf("a fn on the line: %+v", pv)
	}
}

// see never keeps a path a quote cannot hold as one inert word.
func TestSeeRejectsUnsafePaths(t *testing.T) {
	e := New(srvGrant(), nil)
	for _, p := range []string{
		"/srv/a'b", "/srv/a\"b", "/srv/a\\b", "/srv/a\nb", "/srv/a$b", "/srv/a`b`", "/srv/a#b",
		"/srv/a|b", "/srv/a;b", "/srv/*", "/srv/a?", "/srv/a\x1bb", "/srv/a\u202eb", "/srv/a\tb",
	} {
		e.see(p)
	}
	if len(e.seen) != 0 {
		t.Fatalf("kept %q", e.seen)
	}
	e.see("/srv/a b")
	if len(e.seen) != 1 {
		t.Fatalf("plain path with a space refused: %q", e.seen)
	}
}

// After any insert the line is lexed again; an insert that is not exactly
// the expected words is refused and the line stays as it was.
func TestTabInsertMustLexAsItsWords(t *testing.T) {
	g := srvGrant()
	st := lang.NewState()
	lang.Run("fn 'g;unit restart nginx'() { echo }\nfn 'h #x'() { echo }\nfn 'k|ps'() { echo }", g, failParent{t}, lang.Options{State: st})
	for _, typed := range []string{"g", "h", "k"} {
		e := New(g, failParent{t})
		e.opt.State = st
		e.Feed([]byte(typed + "\t\t\x1b[C"))
		if e.line != typed {
			t.Errorf("%q became %q", typed, e.line)
		}
	}
	for _, row := range []completion{
		{insert: "x;unit restart nginx", words: []string{"x;unit restart nginx"}},
		{insert: "x | ps", words: []string{"x | ps"}},
		{insert: "'x' > /srv/out", words: []string{"x"}},
		{insert: "x#y", words: []string{"x#y"}},
		{insert: "$x", words: []string{"x"}},
		{insert: "'x", words: []string{"x"}},
		{insert: "[ 1 ]", words: []string{"[ 1 ]"}},
		{insert: "x y", words: []string{"x"}},
		{insert: "x", words: []string{"y"}},
	} {
		e := New(g, failParent{t})
		e.line, e.cursor = "cat /srv/a then", len("cat /srv/a")
		e.replace(4, len("cat /srv/a"), row)
		if e.line != "cat /srv/a then" || e.cursor != len("cat /srv/a") {
			t.Errorf("%q inserted: %q", row.insert, e.line)
		}
	}
	e := New(g, failParent{t})
	e.line, e.cursor = "cat /srv/a then", len("cat /srv/a")
	e.replace(4, len("cat /srv/a"), completion{insert: "'/srv/a b'", words: []string{"/srv/a b"}})
	if e.line != "cat '/srv/a b' then" {
		t.Fatalf("good insert refused: %q", e.line)
	}
}
