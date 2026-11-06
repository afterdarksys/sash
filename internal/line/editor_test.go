package line

import (
	"os"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

type countParent struct {
	n int
}

func (c *countParent) Do(parent.Verb) (parent.Result, error) {
	c.n++
	return parent.Result{Kind: parent.KindBytes}, nil
}

func paintOf(e *Editor) string {
	v := e.View()
	return Paint(v.Line, v.Suggestion, v.Spans, v.Syntax)
}

func TestDeniedStaysDimWithSyntaxOff(t *testing.T) {
	for _, syntax := range []bool{false, true} {
		p := &countParent{}
		e := New(grant.Grant{}, p)
		e.Syntax = syntax
		e.Feed([]byte("unit restart nginx"))
		v := e.View()
		painted := Paint(v.Line, v.Suggestion, v.Spans, v.Syntax)
		if !strings.Contains(painted, "\x1b[2m") || !strings.Contains(painted, "unit") {
			t.Fatalf("syntax %v paint %q", syntax, painted)
		}
		if strings.Contains(painted, "\x1b[31m") || strings.Contains(painted, "\x1b[36m") {
			t.Fatalf("syntax %v paint %q", syntax, painted)
		}
		if v.Smile != "denied unit.restart" || !v.Denied || !v.Block {
			t.Fatalf("syntax %v smile %q denied %v block %v", syntax, v.Smile, v.Denied, v.Block)
		}
		e.Feed([]byte("\n"))
		if e.Runs != 0 || e.line != "unit restart nginx" || p.n != 0 {
			t.Fatalf("syntax %v runs %d line %q calls %d", syntax, e.Runs, e.line, p.n)
		}
	}
}

func TestEntitledRestartIsDangerAndRuns(t *testing.T) {
	p := &countParent{}
	e := New(grant.Grant{Entitlements: []string{"unit.restart"}}, p)
	e.Feed([]byte("unit restart nginx"))
	if !strings.Contains(paintOf(e), "\x1b[31m") {
		t.Fatalf("paint %q", paintOf(e))
	}
	e.Feed([]byte("\n"))
	if e.Runs != 1 || p.n != 1 || e.Last.Status != 0 {
		t.Fatalf("runs %d calls %d status %d stderr %q", e.Runs, p.n, e.Last.Status, e.Last.Stderr)
	}
}

func TestRightAcceptsSuggestion(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.history = []string{"echo hello"}
	e.Feed([]byte("echo h"))
	e.Feed([]byte{0x1b, '[', 'C'})
	if e.line != "echo hello" || e.Runs != 0 {
		t.Fatalf("line %q runs %d", e.line, e.Runs)
	}
}

func TestUpFindsNewestPrefix(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.history = []string{"echo hello", "echo help"}
	e.Feed([]byte("echo h"))
	e.Feed([]byte{0x1b, '[', 'A'})
	if e.line != "echo help" {
		t.Fatalf("line %q", e.line)
	}
}

func TestCtrlRFindsSubstring(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.history = []string{"echo hello"}
	e.Feed([]byte{0x12})
	e.Feed([]byte("hel"))
	if e.line != "echo hello" {
		t.Fatalf("line %q", e.line)
	}
}

func TestCtrlRMissRestoresAndDoesNotRun(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.Feed([]byte("echo hi"))
	e.Feed([]byte{0x12})
	e.Feed([]byte("zzz\n"))
	if e.line != "echo hi" || e.Runs != 0 {
		t.Fatalf("line %q runs %d", e.line, e.Runs)
	}
}

func TestTabDeniedRowInsertsNothing(t *testing.T) {
	e := New(grant.Grant{Entitlements: []string{"unit.restart"}}, nil)
	e.Feed([]byte("unit\t"))
	v := e.View()
	if !v.MenuOpen || len(v.Menu) != 2 {
		t.Fatalf("open %v rows %+v", v.MenuOpen, v.Menu)
	}
	if v.Menu[0].Insert != "unit status " || v.Menu[0].Key != "unit.status" || v.Menu[0].Desc == "" || !v.Menu[0].Denied {
		t.Fatalf("status %+v", v.Menu[0])
	}
	if v.Menu[1].Insert != "unit restart " || v.Menu[1].Key != "unit.restart" || v.Menu[1].Desc == "" || v.Menu[1].Denied {
		t.Fatalf("restart %+v", v.Menu[1])
	}
	e.Feed([]byte{0x1b, '[', 'C'})
	if e.line != "unit" {
		t.Fatalf("line %q", e.line)
	}
	e.Feed([]byte{0x1b, '[', 'B'})
	e.Feed([]byte{0x1b, '[', 'C'})
	if e.line != "unit restart " {
		t.Fatalf("line %q", e.line)
	}
	if !strings.Contains(paintOf(e), "\x1b[31m") {
		t.Fatalf("paint %q smile %q", paintOf(e), e.View().Smile)
	}
}

func TestTabSingleEntitledInserts(t *testing.T) {
	e := New(grant.Grant{Entitlements: []string{"ps"}}, nil)
	e.Feed([]byte("ps\t"))
	if e.line != "ps " {
		t.Fatalf("line %q", e.line)
	}
	empty := New(grant.Grant{}, nil)
	empty.Feed([]byte("ps\t"))
	if empty.line != "ps" {
		t.Fatalf("line %q", empty.line)
	}
}

func TestViCommandIgnoresEmacsHome(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.Feed([]byte("bind vi\n"))
	if e.Runs != 0 || !e.Vi || e.viCmd {
		t.Fatalf("runs %d vi %v cmd %v", e.Runs, e.Vi, e.viCmd)
	}
	e.Feed([]byte("abc"))
	e.Feed([]byte{0x1b})
	e.Feed([]byte{0x01})
	if e.cursor != 3 || e.line != "abc" {
		t.Fatalf("after ctrl-a cursor %d line %q", e.cursor, e.line)
	}
	e.Feed([]byte("h"))
	if e.cursor != 2 || e.line != "abc" || e.Runs != 0 {
		t.Fatalf("cursor %d line %q runs %d", e.cursor, e.line, e.Runs)
	}
	got := lang.Run("bind vi", grant.Grant{}, nil, lang.Options{})
	if got.Status != 127 || !strings.Contains(got.Stderr, "unknown bind") {
		t.Fatalf("status %d stderr %q", got.Status, got.Stderr)
	}
}

func TestStarGrantDoesNotRun(t *testing.T) {
	p := &countParent{}
	e := New(grant.Grant{Entitlements: []string{"*", "ps"}}, p)
	e.Feed([]byte("ps\n"))
	if e.Runs != 0 || p.n != 0 {
		t.Fatalf("runs %d calls %d", e.Runs, p.n)
	}
}

func TestTypePipeEnterRuns(t *testing.T) {
	p := &countParent{}
	e := New(grant.Grant{}, p)
	e.Feed([]byte("echo hi |> where a == b\n"))
	if e.Runs != 1 || p.n != 0 || e.Last.Status != 2 || e.Last.Stderr != "type |>\n" {
		t.Fatalf("runs %d calls %d status %d stderr %q", e.Runs, p.n, e.Last.Status, e.Last.Stderr)
	}
}

func TestIsTermDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if IsTerm(f) {
		t.Fatal("DevNull is a terminal")
	}
	if IsTerm(nil) {
		t.Fatal("nil is a terminal")
	}
}
