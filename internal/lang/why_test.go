package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

func whyGrant() grant.Grant {
	return grant.Grant{
		V: "sash/1", Purpose: "terminal:open",
		Entitlements: []string{"ps", "file.read", "unit.status"},
		FileRead:     []string{"/var/log"},
	}
}

// whyLines returns the table rows after the header, one field slice per row.
func whyLines(t *testing.T, out Outcome) [][]string {
	t.Helper()
	if out.Status != 0 {
		t.Fatalf("status %d stderr %q", out.Status, out.Stderr)
	}
	lines := strings.Split(strings.TrimRight(out.Stdout, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "command") {
		t.Fatalf("header %q", lines[0])
	}
	var rows [][]string
	for _, l := range lines[1:] {
		rows = append(rows, strings.Fields(l))
	}
	return rows
}

func hasRow(rows [][]string, key, granted, reason string) bool {
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		for i := 0; i+2 < len(r); i++ {
			if r[i] == key && r[i+1] == granted && r[i+2] == reason {
				return true
			}
		}
	}
	return false
}

func TestWhyNoSessionNamesTheMissingParent(t *testing.T) {
	rows := whyLines(t, Why("ls", grant.Grant{}, Options{}))
	if !hasRow(rows, "file.read", "no", "no_session") {
		t.Fatalf("rows %v", rows)
	}
	rows = whyLines(t, Why("ps", grant.Grant{}, Options{}))
	if !hasRow(rows, "ps", "no", "no_session") {
		t.Fatalf("rows %v", rows)
	}
}

func TestWhyNamesKeyRootsAndFix(t *testing.T) {
	g := whyGrant()
	out := Why("unit restart nginx | wc; cat /etc/passwd; ls /var/log; unit status nginx; journal; nosuch", g, Options{})
	rows := whyLines(t, out)
	for _, want := range [][3]string{
		{"unit.restart", "no", "not_in_grant"},
		{"-", "-", "in_process"},
		{"file.read", "no", "outside_roots"},
		{"file.read", "yes", "granted"},
		{"unit.status", "yes", "granted"},
		{"journal", "no", "not_grantable"},
		{"-", "-", "unknown"},
	} {
		if !hasRow(rows, want[0], want[1], want[2]) {
			t.Fatalf("missing %v in\n%s", want, out.Stdout)
		}
	}
	if !strings.Contains(out.Stdout, "--service <unit>") || !strings.Contains(out.Stdout, "roots: /var/log") {
		t.Fatalf("fix text\n%s", out.Stdout)
	}
}

func TestWhyRedirectsGlobsAndValues(t *testing.T) {
	g := whyGrant()
	rows := whyLines(t, Why("ps > /tmp/x", g, Options{}))
	if !hasRow(rows, "file.write", "no", "not_in_grant") {
		t.Fatalf("redirect %v", rows)
	}
	rows = whyLines(t, Why("cat /var/log/*.log", g, Options{}))
	if !hasRow(rows, "file.read", "yes", "granted") {
		t.Fatalf("glob in root %v", rows)
	}
	rows = whyLines(t, Why("ls /etc/*", g, Options{}))
	if !hasRow(rows, "file.read", "no", "outside_roots") {
		t.Fatalf("glob outside %v", rows)
	}
	rows = whyLines(t, Why("let p = /etc; cat $p", g, Options{}))
	if !hasRow(rows, "file.read", "yes", "value_path") {
		t.Fatalf("value path %v", rows)
	}
	rows = whyLines(t, Why("ls", g, Options{}))
	if !hasRow(rows, "file.read", "no", "prefix_unset") {
		t.Fatalf("prefix %v", rows)
	}
	rows = whyLines(t, Why("ls", g, Options{Prefix: "/var/log"}))
	if !hasRow(rows, "file.read", "yes", "granted") {
		t.Fatalf("prefix set %v", rows)
	}
}

func TestWhyStarAndEmergency(t *testing.T) {
	g := whyGrant()
	g.Entitlements = append(g.Entitlements, "*")
	rows := whyLines(t, Why("ps", g, Options{}))
	if !hasRow(rows, "ps", "no", "not_in_grant") {
		t.Fatalf("star %v", rows)
	}
	e := whyGrant()
	e.Purpose = "emergency"
	out := Why("unit restart nginx", e, Options{})
	if !strings.Contains(out.Stdout, "adssh signs it") {
		t.Fatalf("emergency\n%s", out.Stdout)
	}
}

func TestWhyHistoryBracesAndErrors(t *testing.T) {
	g := whyGrant()
	rows := whyLines(t, Why("!!", g, Options{History: []string{"unit restart nginx"}}))
	if !hasRow(rows, "unit.restart", "no", "not_in_grant") {
		t.Fatalf("history %v", rows)
	}
	rows = whyLines(t, Why("cat /var/log/{a,b}", g, Options{}))
	if len(rows) != 1 || !hasRow(rows, "file.read", "yes", "granted") {
		t.Fatalf("braces %v", rows)
	}
	if out := Why("if", g, Options{}); out.Status != exitType || out.Stdout != "" {
		t.Fatalf("parse error %+v", out)
	}
	if out := Why("!!", g, Options{}); out.Status == 0 {
		t.Fatalf("empty history %+v", out)
	}
}

func TestWhyReportsShapeErrorsRunWouldRefuse(t *testing.T) {
	g := whyGrant()
	src := "cat /var/log/a | head < /var/log/b"
	if got := Run(src, g, &countParent{}, Options{}); got.Status == 0 {
		t.Fatalf("run accepted %q", src)
	}
	out := Why(src, g, Options{})
	if !strings.Contains(out.Stdout, "the line does not run: usage <") {
		t.Fatalf("shape\n%s", out.Stdout)
	}
	if out := Why("ps |> where user == root", g, Options{}); strings.Contains(out.Stdout, "does not run") {
		t.Fatalf("valid line flagged\n%s", out.Stdout)
	}
}

func TestWhyKnowsFunctionsDefinedOnTheLine(t *testing.T) {
	g := whyGrant()
	out := Why("fn greet(x) { echo $x }\ngreet hi", g, Options{})
	rows := whyLines(t, out)
	if strings.Contains(out.Stdout, "unknown") || !hasRow(rows, "-", "-", "in_process") || len(rows) != 2 {
		t.Fatalf("fn\n%s", out.Stdout)
	}
}

// A run reaches the pipes of let, declare, and with values, so why
// explains each of their stages, a command name from a value included.
func TestWhyExplainsValuePipes(t *testing.T) {
	g := whyGrant()
	cases := []struct {
		src  string
		want [3]string
	}{
		{"declare x = (unit restart nginx)", [3]string{"unit.restart", "no", "not_in_grant"}},
		{"declare -r x = (cat /etc/passwd)", [3]string{"file.read", "no", "outside_roots"}},
		{"let x = (cat /etc/passwd)", [3]string{"file.read", "no", "outside_roots"}},
		{"with v = (unit restart nginx) { echo $v }", [3]string{"unit.restart", "no", "not_in_grant"}},
		{"fn f() { declare x = (unit restart nginx) }", [3]string{"unit.restart", "no", "not_in_grant"}},
		{"let c = unit\ndeclare x = ($c restart nginx)", [3]string{"-", "no", msgCommandFromValue}},
		{"let c = unit\nwith v = ($c restart nginx) { echo $v }", [3]string{"-", "no", msgCommandFromValue}},
	}
	for _, c := range cases {
		out := Why(c.src, g, Options{})
		rows := whyLines(t, out)
		if !hasRow(rows, c.want[0], c.want[1], c.want[2]) {
			t.Errorf("%q: missing %v in\n%s", c.src, c.want, out.Stdout)
		}
	}
}

// A literal path is printed escaped: a bidi override, an escape sequence,
// or a zero-width character cannot rewrite or disguise the terminal.
func TestWhyEscapesCells(t *testing.T) {
	g := whyGrant()
	cases := []struct{ src, bad, want string }{
		{"cat '/etc/a\u202eb'", "\u202e", `/etc/a\u202eb`},
		{"cat '/etc/a\x1b[31mb'", "\x1b", `/etc/a\x1b[31mb`},
		{"cat '/etc/a\x1b]0;x\x07b'", "\x07", `/etc/a\x1b]0;x\x07b`},
		{"cat '/etc/a\u200bb\u200dc'", "\u200b", `/etc/a\u200bb\u200dc`},
		{"cat '/etc/a\ufeffb'", "\ufeff", `/etc/a\ufeffb`},
		{"cat '/etc/a\u2028b'", "\u2028", `/etc/a\u2028b`},
		{"cat '/etc/a\u0085b'", "\u0085", `/etc/a\u0085b`},
		{"cat '/etc/a\xffb'", "\xff", `/etc/a\xffb`},
	}
	for _, c := range cases {
		out := Why(c.src, g, Options{})
		if strings.Contains(out.Stdout, c.bad) || !strings.Contains(out.Stdout, c.want) {
			t.Errorf("%q printed\n%q", c.src, out.Stdout)
		}
	}
}
