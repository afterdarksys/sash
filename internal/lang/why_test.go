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
	out := Why("fn greet x { echo $x }; greet hi", g, Options{})
	if strings.Contains(out.Stdout, "unknown") {
		t.Fatalf("fn\n%s", out.Stdout)
	}
}
