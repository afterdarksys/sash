package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type fake struct {
	calls    []parent.Verb
	statExit int
}

func (f *fake) Do(v parent.Verb) (parent.Result, error) {
	f.calls = append(f.calls, v)
	switch {
	case v.Key == "ps":
		return parent.Result{
			Kind: parent.KindRows,
			Cols: []string{"user", "pid", "cmd"},
			Rows: []parent.Row{
				{"user": "root", "pid": "1", "cmd": "/usr/sbin/sshd"},
				{"user": "root", "pid": "2", "cmd": "cron"},
			},
		}, nil
	case v.Key == "unit.status":
		return parent.Result{
			Kind: parent.KindRows,
			Cols: []string{"name", "state"},
			Rows: []parent.Row{
				{"name": "cron", "state": "failed"},
				{"name": "sshd", "state": "running"},
			},
		}, nil
	case v.Key == "file.read" && len(v.Argv) > 0 && v.Argv[0] == "stat":
		return parent.Result{Kind: parent.KindBytes, Exit: f.statExit}, nil
	case v.Key == "file.read":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("sshd line\n")}, nil
	case v.Key == "file.write":
		return parent.Result{Kind: parent.KindBytes}, nil
	default:
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
	}
}

func allow() grant.Grant {
	return grant.Grant{
		V:            "sash/1",
		Entitlements: []string{"ps", "unit.status", "unit.restart", "journal", "package.query", "package.install", "sysctl.get", "sysctl.set", "config.read", "file.read", "file.write"},
		FileRead:     []string{"/var/log", "/etc"},
		FileWrite:    []string{"/var/tmp/ops"},
	}
}

func keys(f *fake) []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Key
	}
	return out
}

func TestBytePipeFiltersText(t *testing.T) {
	f := &fake{}
	got := Run(`ps -ef | egrep sshd | head -n 1`, allow(), f, Options{})
	if got.Status != 0 {
		t.Fatalf("status %d stderr %s", got.Status, got.Stderr)
	}
	if got.Stdout != "root  1  /usr/sbin/sshd\n" {
		t.Fatalf("stdout %q", got.Stdout)
	}
	if strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("calls %v", keys(f))
	}
}

func TestRowPipeKeepsRows(t *testing.T) {
	f := &fake{}
	got := Run(`unit status |> where state == "failed"`, allow(), f, Options{})
	if got.Status != 0 {
		t.Fatalf("status %d stderr %s", got.Status, got.Stderr)
	}
	if got.Stdout != "cron  failed\n" {
		t.Fatalf("stdout %q", got.Stdout)
	}
}

func TestRowPipeTypeErrorRunsNothing(t *testing.T) {
	f := &fake{}
	got := Run(`ps -ef |> egrep sshd`, allow(), f, Options{})
	if got.Status != exitType {
		t.Fatalf("status %d", got.Status)
	}
	if !strings.Contains(got.Stderr, "type |>") {
		t.Fatalf("stderr %q", got.Stderr)
	}
	if len(f.calls) != 0 {
		t.Fatalf("parent called: %v", keys(f))
	}
}

func TestDenialRunsNothing(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"ps"}
	got := Run(`ps -ef | unit restart cron`, g, f, Options{})
	if got.Status != exitDenied {
		t.Fatalf("status %d stderr %s", got.Status, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "denied unit.restart") {
		t.Fatalf("stderr %q", got.Stderr)
	}
	if len(f.calls) != 0 {
		t.Fatalf("parent called: %v", keys(f))
	}
}

func TestAndDenialRunsNothing(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"ps"}
	got := Run(`echo kept && unit restart cron`, g, f, Options{})
	if got.Status != exitDenied || got.Stdout != "" || len(f.calls) != 0 {
		t.Fatalf("status %d stdout %q calls %v stderr %s", got.Status, got.Stdout, keys(f), got.Stderr)
	}
}

func TestLetValueIsNotSyntax(t *testing.T) {
	f := &fake{}
	src := "let pat = \"sshd|unit restart cron\"\nps -ef | egrep $pat\n"
	got := Run(src, allow(), f, Options{})
	if strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("calls %v stderr %s", keys(f), got.Stderr)
	}
	if got.Status != 0 || !strings.Contains(got.Stdout, "sshd") {
		t.Fatalf("status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}

	f = &fake{}
	src = "let pat = \"sshd | unit restart cron\"\nps -ef | egrep $pat\n"
	got = Run(src, allow(), f, Options{})
	if strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("spaced calls %v stderr %s", keys(f), got.Stderr)
	}
	// Spaces stay inside the one regexp, so "sshd " does not match the row.
	// A word split would have searched for sshd and printed it.
	if got.Status != 1 || got.Stdout != "" {
		t.Fatalf("spaced status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}
}

func TestIfRowTruth(t *testing.T) {
	f := &fake{}
	src := "if unit status |> where state == \"failed\" {\n  echo yes\n} else {\n  echo no\n}\n"
	got := Run(src, allow(), f, Options{})
	if got.Stdout != "yes\n" || got.Status != 0 {
		t.Fatalf("status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}
	src = "if unit status |> where state == \"missing\" {\n  echo yes\n} else {\n  echo no\n}\n"
	got = Run(src, allow(), f, Options{})
	if got.Stdout != "no\n" || got.Status != 0 {
		t.Fatalf("stdout %q status %d stderr %s", got.Stdout, got.Status, got.Stderr)
	}
}

func TestRowsLiftsText(t *testing.T) {
	f := &fake{}
	got := Run(`echo "a  b" | rows |> where line == "a  b"`, grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "a  b\n" || len(f.calls) != 0 {
		t.Fatalf("status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, len(f.calls))
	}
	got = Run(`echo "root 1 /usr/sbin/sshd -D" | rows user pid cmd`, grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "root  1  /usr/sbin/sshd -D\n" {
		t.Fatalf("named status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	got = Run(`echo only | rows user pid cmd`, grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "only    \n" {
		t.Fatalf("short stdout %q stderr %q", got.Stdout, got.Stderr)
	}
	got = Run(`echo "sshd  -D" | rows cmd`, grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "sshd  -D\n" {
		t.Fatalf("remainder stdout %q", got.Stdout)
	}
	src := "for row in echo \"root 1 sshd -D\" | rows user pid cmd {\n  echo $row.cmd\n}\n"
	got = Run(src, grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "sshd -D\n" {
		t.Fatalf("for status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	got = Run("if cat | rows {\n  echo yes\n} else {\n  echo no\n}\n", grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "no\n" || len(f.calls) != 0 {
		t.Fatalf("empty status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, len(f.calls))
	}
}

func TestRowsDoesNotLiftSilently(t *testing.T) {
	f := &fake{}
	got := Run(`echo hello |> where line == "hello"`, grant.Grant{}, f, Options{})
	if got.Status != exitType || !strings.Contains(got.Stderr, "type |>") || got.Stdout != "" || len(f.calls) != 0 {
		t.Fatalf("status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, len(f.calls))
	}
	got = Run(`echo hello | rows line |> where line == "hello"`, grant.Grant{}, f, Options{})
	if got.Status != 0 || got.Stdout != "hello\n" {
		t.Fatalf("lift status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	got = Run(`ps -ef | rows user pid cmd |> where pid == "1"`, allow(), f, Options{})
	if got.Status != 0 || got.Stdout != "root  1  /usr/sbin/sshd\n" || strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("ps status %d stdout %q stderr %q calls %v", got.Status, got.Stdout, got.Stderr, keys(f))
	}
	f.calls = nil
	g := grant.Grant{}
	got = Run(`ps | rows`, g, f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 || got.Stdout != "" {
		t.Fatalf("denied status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, len(f.calls))
	}
}

func TestRowsUsagePrintsNoValue(t *testing.T) {
	f := &fake{}
	for _, src := range []string{
		`echo secret | rows 1bad`,
		`echo secret | rows a a`,
		`echo secret | rows ""`,
		"fn rows() {\n  echo no\n}\n",
	} {
		got := Run(src, grant.Grant{}, f, Options{})
		if got.Status != exitType || got.Stdout != "" || strings.Contains(got.Stdout, "secret") || strings.Contains(got.Stderr, "secret") {
			t.Fatalf("src %q status %d stdout %q stderr %q", src, got.Status, got.Stdout, got.Stderr)
		}
		if !strings.Contains(got.Stderr, "usage rows") && !strings.Contains(got.Stderr, "usage fn") {
			t.Fatalf("src %q stderr %q", src, got.Stderr)
		}
		if len(f.calls) != 0 {
			t.Fatalf("src %q calls %v", src, keys(f))
		}
	}
}

func TestForWalksRows(t *testing.T) {
	f := &fake{}
	src := "for row in unit status |> where state == \"failed\" {\n  echo $row.name\n}\n"
	got := Run(src, allow(), f, Options{})
	if got.Stdout != "cron\n" || got.Status != 0 {
		t.Fatalf("status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}
}

func TestFnChecksEveryCall(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"unit.status"}
	src := "fn boom() {\n  unit restart cron\n}\nboom\n"
	got := Run(src, g, f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 {
		t.Fatalf("status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
	g = allow()
	got = Run(src, g, f, Options{})
	if got.Status != 0 || strings.Join(keys(f), ",") != "unit.restart" {
		t.Fatalf("status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
}

func TestRedirectPreflight(t *testing.T) {
	f := &fake{}
	got := Run(`ps -ef > /etc/passwd`, allow(), f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 {
		t.Fatalf("escape status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
	got = Run(`ps -ef > /var/tmp/ops/out`, allow(), f, Options{})
	if got.Status != 0 || strings.Join(keys(f), ",") != "ps,file.write" {
		t.Fatalf("write status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
	if got.Stdout != "" {
		t.Fatalf("redirect printed %q", got.Stdout)
	}
	if !strings.Contains(string(f.calls[1].Body), "sshd") {
		t.Fatalf("body %q", f.calls[1].Body)
	}
}

func TestUnknown(t *testing.T) {
	f := &fake{}
	got := Run(`rm -rf /`, allow(), f, Options{})
	if got.Status != exitUnknown || len(f.calls) != 0 {
		t.Fatalf("status %d calls %v", got.Status, keys(f))
	}
	if !strings.Contains(got.Stderr, "unknown rm") {
		t.Fatalf("stderr %q", got.Stderr)
	}
}

func TestPipefail(t *testing.T) {
	f := &fake{}
	got := Run(`echo hello | egrep sshd | echo kept`, allow(), f, Options{})
	if got.Status != 1 || got.Stdout != "kept\n" {
		t.Fatalf("status %d stdout %q", got.Status, got.Stdout)
	}
}

func TestStarGrantAuthorizesNothing(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"*", "ps"}
	got := Run(`ps -ef`, g, f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 {
		t.Fatalf("status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
}

func TestCdStaysUnderReadRoot(t *testing.T) {
	f := &fake{}
	got := Run("cd /tmp\ndirs\n", allow(), f, Options{Prefix: "/var/log"})
	if got.Status != exitDenied || got.Prefix != "/var/log" || len(f.calls) != 0 {
		t.Fatalf("status %d prefix %s calls %v stderr %s stdout %q", got.Status, got.Prefix, keys(f), got.Stderr, got.Stdout)
	}
	f = &fake{}
	got = Run("cd /etc\ndirs\n", allow(), f, Options{Prefix: "/var/log"})
	if got.Status != 0 || got.Prefix != "/etc" || got.Stdout != "/etc\n" {
		t.Fatalf("status %d prefix %s stdout %q stderr %s calls %v", got.Status, got.Prefix, got.Stdout, got.Stderr, keys(f))
	}
	if f.calls[0].Argv[0] != "stat" || f.calls[0].Path != "/etc" {
		t.Fatalf("stat verb %+v", f.calls[0])
	}
}
