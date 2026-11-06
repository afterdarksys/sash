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

func (c *countParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "history" {
		return parent.Result{Kind: parent.KindRows, Cols: []string{"line"}}, nil
	}
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

type argvParent struct {
	calls []parent.Verb
}

func (a *argvParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "history" {
		return parent.Result{Kind: parent.KindRows, Cols: []string{"line"}}, nil
	}
	a.calls = append(a.calls, v)
	return parent.Result{Kind: parent.KindBytes}, nil
}

func TestSpaceDoesNotAbbreviate(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.Feed([]byte("unt "))
	if e.line != "unt " || e.Runs != 0 {
		t.Fatalf("line %q runs %d", e.line, e.Runs)
	}
}

func TestAltDotInsertsLastArg(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.history = []string{"echo hello world"}
	e.Feed([]byte{0x1b, '.'})
	if e.line != "world" || e.Runs != 0 {
		t.Fatalf("line %q runs %d", e.line, e.Runs)
	}
	empty := New(grant.Grant{}, nil)
	empty.Feed([]byte{0x1b, '.'})
	if empty.line != "" || empty.Runs != 0 {
		t.Fatalf("empty line %q runs %d", empty.line, empty.Runs)
	}
	blank := New(grant.Grant{}, nil)
	blank.history = []string{" \t"}
	blank.Feed([]byte{0x1b, '.'})
	if blank.line != "" || blank.Runs != 0 {
		t.Fatalf("blank line %q runs %d", blank.line, blank.Runs)
	}
	long := New(grant.Grant{}, nil)
	long.history = []string{strings.Repeat("a", maxLine+1)}
	long.Feed([]byte{0x1b, '.'})
	if long.line != "" || long.Runs != 0 {
		t.Fatalf("long line %q runs %d", long.line, long.Runs)
	}
}

func TestViAltDotInsertsLastArg(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.Feed([]byte("bind vi\n"))
	e.Feed([]byte{0x1b})
	e.history = []string{"echo hello world"}
	e.Feed([]byte{0x1b, '.'})
	if e.line != "world" || e.Runs != 0 || !e.Vi {
		t.Fatalf("line %q runs %d vi %v", e.line, e.Runs, e.Vi)
	}
}

func TestBangDeniedStays(t *testing.T) {
	p := &argvParent{}
	e := New(grant.Grant{}, p)
	e.history = []string{"unit restart nginx"}
	e.Feed([]byte("!!\n"))
	v := e.View()
	if e.line != "unit restart nginx" || e.Runs != 0 || len(p.calls) != 0 || len(e.history) != 1 {
		t.Fatalf("line %q runs %d calls %d hist %d", e.line, e.Runs, len(p.calls), len(e.history))
	}
	if v.Smile != "denied unit.restart" || !v.Block || !v.Denied {
		t.Fatalf("smile %q block %v denied %v", v.Smile, v.Block, v.Denied)
	}
}

func TestBangEntitledRunsExpanded(t *testing.T) {
	p := &argvParent{}
	e := New(grant.Grant{Entitlements: []string{"unit.restart"}}, p)
	e.history = []string{"unit restart nginx"}
	e.Feed([]byte("!!\n"))
	if e.Runs != 1 || len(p.calls) != 1 || len(e.history) != 2 {
		t.Fatalf("runs %d calls %d hist %v", e.Runs, len(p.calls), e.history)
	}
	if e.history[0] != "unit restart nginx" || e.history[1] != "unit restart nginx" {
		t.Fatalf("hist %v", e.history)
	}
	if strings.Join(p.calls[0].Argv, " ") != "unit restart nginx" {
		t.Fatalf("argv %v", p.calls[0].Argv)
	}
}

func TestBangOverlongHolds(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.history = []string{strings.Repeat("a", maxLine+1)}
	e.Feed([]byte("!!\n"))
	v := e.View()
	if e.line != "!!" || e.Runs != 0 || v.Smile != "type brace" || !v.Block {
		t.Fatalf("line %q runs %d smile %q block %v", e.line, e.Runs, v.Smile, v.Block)
	}
}

type histParent struct {
	rows    []parent.Row
	gets    int
	appends []string
}

func (h *histParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key != "history" {
		return parent.Result{Kind: parent.KindBytes}, nil
	}
	if len(v.Argv) == 1 && v.Argv[0] == "get" {
		h.gets++
		return parent.Result{Kind: parent.KindRows, Cols: []string{"line"}, Rows: append([]parent.Row(nil), h.rows...)}, nil
	}
	if len(v.Argv) == 1 && v.Argv[0] == "append" {
		h.appends = append(h.appends, string(v.Body))
		return parent.Result{}, nil
	}
	return parent.Result{}, os.ErrInvalid
}

func TestHistoryLoadsAndViSearch(t *testing.T) {
	p := &histParent{rows: []parent.Row{{"line": "echo persisted"}}}
	e := New(grant.Grant{}, p)
	if p.gets != 1 || len(e.history) != 1 || e.history[0] != "echo persisted" {
		t.Fatalf("gets %d hist %v", p.gets, e.history)
	}
	e.Feed([]byte("bind vi\n"))
	e.Feed([]byte{0x1b, 0x12})
	e.Feed([]byte("pers"))
	if e.line != "echo persisted" || !e.Vi {
		t.Fatalf("line %q vi %v", e.line, e.Vi)
	}
}

func TestPrivateSessionSkipsSuggestions(t *testing.T) {
	p := &histParent{rows: []parent.Row{{"line": "echo persisted"}}}
	e := New(grant.Grant{AllowPrivate: true}, p)
	if p.gets != 0 || len(e.history) != 0 {
		t.Fatalf("gets %d hist %v", p.gets, e.history)
	}
	e.Feed([]byte("echo hi\n"))
	if e.Runs != 1 || e.Last.Stdout != "hi\n" || len(p.appends) != 1 || p.appends[0] != "echo hi" {
		t.Fatalf("runs %d stdout %q appends %v", e.Runs, e.Last.Stdout, p.appends)
	}
	if len(e.history) != 1 || e.history[0] != "echo hi" {
		t.Fatalf("hist %v", e.history)
	}
}

func TestTraceSessionPrintsStage(t *testing.T) {
	e := New(grant.Grant{}, nil)
	e.Feed([]byte("trace"))
	v := e.View()
	if v.Smile != "" || strings.Contains(v.Smile, "unknown") {
		t.Fatalf("smile %q", v.Smile)
	}
	e.Feed([]byte(" on\necho hi\n"))
	if e.Runs != 1 || e.Last.Stdout != "hi\n" || e.Last.Stderr != "trace echo\n" || !e.opt.Trace {
		t.Fatalf("runs %d stdout %q stderr %q trace %v", e.Runs, e.Last.Stdout, e.Last.Stderr, e.opt.Trace)
	}
	e.Feed([]byte("trace off\necho yo\n"))
	if e.Last.Stdout != "yo\n" || e.Last.Stderr != "" || e.opt.Trace {
		t.Fatalf("stdout %q stderr %q trace %v", e.Last.Stdout, e.Last.Stderr, e.opt.Trace)
	}
	denied := New(grant.Grant{}, nil)
	denied.Feed([]byte("trace on\nunit restart nginx\n"))
	if denied.Runs != 0 || strings.Contains(denied.Last.Stderr, "trace") {
		t.Fatalf("runs %d stderr %q", denied.Runs, denied.Last.Stderr)
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
