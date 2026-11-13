package line

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

// lsParent lists every directory as the names in rows and answers any
// other verb with no bytes.
type lsParent struct{ names []string }

func (p *lsParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "history" {
		return parent.Result{Kind: parent.KindRows, Cols: []string{"line"}}, nil
	}
	if len(v.Argv) > 0 && v.Argv[0] == "ls" {
		var rows []parent.Row
		for _, n := range p.names {
			rows = append(rows, parent.Row{"name": n, "type": "file"})
		}
		return parent.Result{Kind: parent.KindRows, Rows: rows}, nil
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

func readGrant() grant.Grant {
	return grant.Grant{Entitlements: []string{"ps", "file.read", "unit.status", "unit.restart"}, FileRead: []string{"/var/log"}, AllowPrivate: true}
}

func menuInserts(v View) []string {
	var out []string
	for _, r := range v.Menu {
		out = append(out, r.Insert)
	}
	return out
}

// Tab completes the word at the cursor and keeps the rest of the line.
func TestTabCompletesWordAtCursor(t *testing.T) {
	e := New(readGrant(), nil)
	e.Feed([]byte("echo hi; un\t"))
	if v := e.View(); !v.MenuOpen || strings.Join(menuInserts(v), "|") != "unit status |unit restart |uniq " {
		t.Fatalf("menu %+v", v.Menu)
	}
	e.Feed([]byte("\t\x1b[C"))
	if e.line != "echo hi; unit restart " {
		t.Fatalf("line %q", e.line)
	}
	e = New(readGrant(), nil)
	e.Feed([]byte("unit re | egrep x"))
	e.cursor = 7
	e.Feed([]byte("\t"))
	if e.line != "unit restart  | egrep x" || e.cursor != 13 {
		t.Fatalf("middle %q %d", e.line, e.cursor)
	}
	e = New(readGrant(), nil)
	e.Feed([]byte("if unit re\t"))
	if e.line != "if unit restart " {
		t.Fatalf("after if %q", e.line)
	}
	// A form already typed in full is not offered again: the word is an
	// argument.
	e = New(readGrant(), nil)
	e.Feed([]byte("ps \t"))
	if e.line != "ps " || e.View().MenuOpen {
		t.Fatalf("argument %q", e.line)
	}
}

func TestTabCompletesFlags(t *testing.T) {
	for in, want := range map[string]string{
		"shopt -\t":  "",
		"shopt -s\t": "shopt -s ",
		"template_file /var/log/s.yaml /var/log/t o --\t": "template_file /var/log/s.yaml /var/log/t o --check ",
		"from csv --n\t": "from csv --noheader ",
		"declare -r -\t": "",
		"echo -\t":       "echo -",
	} {
		e := New(readGrant(), nil)
		e.Feed([]byte(in))
		if want == "" {
			if v := e.View(); !v.MenuOpen || len(v.Menu) < 2 {
				t.Errorf("%q: menu %+v", in, v.Menu)
			}
			continue
		}
		if e.line != want {
			t.Errorf("%q: line %q want %q", in, e.line, want)
		}
	}
	e := New(readGrant(), nil)
	e.Feed([]byte("declare -\t"))
	if got := strings.Join(menuInserts(e.View()), ""); got != "-r -i -a -A -p -s " {
		t.Fatalf("declare flags %q", got)
	}
}

// After $, Tab offers names from the session and from earlier on the line,
// never values.
func TestTabCompletesNamesNotValues(t *testing.T) {
	e := New(readGrant(), nil)
	e.Feed([]byte("let hostname = SEKRETVALUE\r"))
	e.Feed([]byte("declare -s token = SEKRETTOKEN\r"))
	e.Feed([]byte("echo $hos\t"))
	if e.line != "echo $hostname" {
		t.Fatalf("session name %q", e.line)
	}
	e.Feed([]byte{0x15})
	e.Feed([]byte("echo $\t"))
	v := e.View()
	f := Frame(v)
	if !v.MenuOpen || strings.Contains(f, "SEKRET") || !strings.Contains(f, "$token") || !strings.Contains(f, "$hostname") {
		t.Fatalf("names %q", f)
	}
	for in, want := range map[string]string{
		"let abc = 1; echo $ab\t":           "let abc = 1; echo $abc",
		"for row, idx in ps { echo $id\t":   "for row, idx in ps { echo $idx",
		"fn f(alpha, beta) { echo $be\t":    "fn f(alpha, beta) { echo $beta",
		"declare -i count = 2; echo $cou\t": "declare -i count = 2; echo $count",
		"try { ps } catch err { echo $er\t": "try { ps } catch err { echo $err",
		"with zed = 1 { echo $ze\t":         "with zed = 1 { echo $zed",
		"echo $ab\t; let abc = 1":           "echo $ab; let abc = 1",
	} {
		e := New(readGrant(), nil)
		e.Feed([]byte(in))
		if e.line != want {
			t.Errorf("%q: %q want %q", in, e.line, want)
		}
	}
}

func TestTabCompletesFunctions(t *testing.T) {
	e := New(readGrant(), nil)
	e.Feed([]byte("fn greet() { echo hi }\r"))
	e.Feed([]byte("gre\t"))
	if e.line != "greet " {
		t.Fatalf("session fn %q", e.line)
	}
	e = New(readGrant(), nil)
	e.Feed([]byte("fn hello() { echo }; hel\t"))
	if e.line != "fn hello() { echo }; hello " {
		t.Fatalf("line fn %q", e.line)
	}
}

// Paths come only from what the session has seen, filtered by the grant;
// a path that is not a plain word is quoted; a shown path is escaped; a
// denied row inserts nothing.
func TestTabCompletesSeenPathsOnly(t *testing.T) {
	p := &lsParent{names: []string{"a.log", "b.log"}}
	e := New(readGrant(), p)
	for _, line := range []string{
		"cat /var/log/syslog",
		"echo /etc/shadow /var/log/nginx/access.log",
		"echo '/var/log/a b' \"/var/log/it's\"",
		"echo /var/log/*.log",
		"cd /var/log",
	} {
		e.Feed([]byte(line + "\r"))
		if e.Runs == 0 || e.hold != "" {
			t.Fatalf("%q did not run: hold %q", line, e.hold)
		}
	}
	for in, want := range map[string]string{
		"cat /var/log/sy\t": "cat /var/log/syslog",
		"cat /var/log/ng\t": "cat /var/log/nginx/access.log",
		"cat sy\t":          "cat syslog",
		"cat /var/log/b.\t": "cat /var/log/b.log",
		"cat /etc/sh\t":     "cat /etc/sh",
		"cat /var/log/it\t": "cat /var/log/it",
	} {
		e.Feed([]byte{0x15})
		e.Feed([]byte(in))
		if e.line != want {
			t.Errorf("%q: %q want %q", in, e.line, want)
		}
	}
	e.Feed([]byte{0x15})
	e.Feed([]byte("cat /var/log/a\t"))
	v := e.View()
	ins := strings.Join(menuInserts(v), "|")
	if !v.MenuOpen || !strings.Contains(ins, "'/var/log/a b'") || !strings.Contains(ins, "/var/log/a.log") {
		t.Fatalf("menu %q", ins)
	}
	// A name from the host can hold anything; keys cannot type ESC, so
	// see it directly. It is never kept, so never shown.
	e.see("/var/log/esc\x1b]0;pwn\x07\u202e")
	e.Feed([]byte{0x15})
	e.Feed([]byte("cat /\t"))
	v = e.View()
	f := Frame(v)
	if strings.Contains(f, "/etc/shadow") || strings.Contains(f, "pwn") || strings.Contains(f, "it's") || !strings.Contains(f, "/var/log/syslog") {
		t.Fatalf("frame %q", f)
	}
	// No file.read: the rows are dim and insert nothing.
	g := readGrant()
	g.Entitlements = []string{"ps"}
	d := New(g, nil)
	d.see("/var/log/one")
	d.see("/var/log/two")
	d.Feed([]byte("echo /var/log/\t"))
	v = d.View()
	if !v.MenuOpen || !v.Menu[0].Denied {
		t.Fatalf("denied %+v", v.Menu)
	}
	d.Feed([]byte("\x1b[C"))
	if d.line != "echo /var/log/" {
		t.Fatalf("denied row inserted %q", d.line)
	}
}

// Tab never calls the parent: every completion is from what sash holds.
func TestTabNeverCallsParent(t *testing.T) {
	g := readGrant()
	st := lang.NewState()
	lang.Run("let host = x; fn greet(a) { echo $a }", g, &lsParent{}, lang.Options{State: st})
	e := New(g, failParent{t})
	e.opt.State = st
	e.opt.Prefix = "/var/log"
	for _, p := range []string{"/var/log/syslog", "/var/log/a b", "/etc/shadow"} {
		e.see(p)
	}
	for _, s := range []string{
		"", "u", "unit ", "unit r", "p", "ps | eg", "echo $", "echo $h", "gr", "cat /", "cat /var/log/s",
		"cat sy", "declare -", "set -", "from csv --", "if p", "let a = 1; echo $a", "*", "cat /var/log/*",
		"!!", "echo {a,b}", "$cmd", "echo '/var", "unit $op ",
	} {
		e.Feed([]byte{0x15})
		e.Feed([]byte(s))
		for i := 0; i < 3; i++ {
			e.Feed([]byte("\t"))
			Frame(e.View())
		}
		e.Feed([]byte("\x1b[C"))
		Frame(e.View())
		e.Feed([]byte{0x15})
		e.Feed([]byte(s))
		e.cursor = len(s) / 2
		e.Feed([]byte("\t"))
		Frame(e.View())
	}
}

// Every form that takes flags is in the table.
func TestFlagsAreForms(t *testing.T) {
	forms := map[string]bool{}
	n := 0
	for _, b := range lang.Builtins() {
		forms[b.Form] = true
		n += len(b.Flags)
	}
	for _, f := range []string{"set", "shopt", "declare", "template_file", "from csv"} {
		if !forms[f] {
			t.Errorf("%s not a form", f)
		}
	}
	if n != 1+5+2+6+1+1 {
		t.Fatalf("%d flags", n)
	}
}

// An inserted path lexes back to the same one word, through Enter's
// expansion too; a path quoting cannot keep one word is refused.
func TestQuotePathRoundTrips(t *testing.T) {
	for _, p := range []string{"/var/log/a", "/var/log/a b", "/var/log/a&b", "/var/log/{a,b}", "/var/log/!!", "/var/log/a<b>(c)", "/var/log/日本"} {
		q, ok := quotePath(p)
		line := "cat " + q
		exp, prob := lang.Expand(line, readGrant(), failParent{t}, lang.Options{History: []string{"ps"}})
		if !ok || prob != nil || exp != line {
			t.Errorf("%q: quoted %q ok %v expand %q %v", p, q, ok, exp, prob)
		}
		if got := lang.PathWords(line); len(got) != 1 || got[0] != p {
			t.Errorf("%q quoted %s lexes to %q", p, q, got)
		}
	}
	for _, p := range []string{"/var/log/it's", `/var/log/q"x`, `/var/log/a\b`, "/var/log/$x", "/var/log/a#b", "/var/log/a;b", "/var/log/a|b", "/var/log/*.log", "/var/log/a?", "/var/log/a`b`", ""} {
		if q, ok := quotePath(p); ok {
			t.Errorf("%q quoted as %q", p, q)
		}
	}
}

// origin is in the compiled-in table, so Tab offers it.
func TestTabOffersOrigin(t *testing.T) {
	e := New(readGrant(), nil)
	e.Feed([]byte("origi\t"))
	if e.line != "origin " {
		t.Fatalf("%q", e.line)
	}
}
