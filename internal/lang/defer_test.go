package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

func TestDeferRunsLastFirstOnEveryExit(t *testing.T) {
	for _, tc := range []struct {
		src, want string
		status    int
	}{
		{"defer { echo one }\ndefer { echo two }\necho body", "body\ntwo\none\n", 0},
		{"defer { echo cleanup }\necho \"{bad\" | from json\necho never", "cleanup\n", exitType},
		{"set -e\ndefer { echo cleanup }\necho a | egrep zz\necho never", "cleanup\n", 1},
		{"defer { echo cleanup }\nexit 4", "cleanup\n", 4},
		{"fn f() { defer { echo f-done }; echo in-f; return [ 3 ] }\nlet x = (f)\necho got $x", "in-f\nf-done\ngot 3\n", 0},
		{"set loopmax 2\ndefer { echo cleanup }\nfor i in [ 1..5 ] { echo $i }", "1\n2\ncleanup\n", exitType},
	} {
		out := Run(tc.src, allow(), &fake{}, Options{})
		if out.Status != tc.status || out.Stdout != tc.want {
			t.Errorf("%q: %+v want %q %d", tc.src, out, tc.want, tc.status)
		}
	}
}

// A failing defer is reported, the others still run, and the first failure
// keeps its status.
func TestDeferKeepsFirstFailure(t *testing.T) {
	out := Run("defer { echo second }\ndefer { echo \"{x\" | from json }\necho \"{y\" | from json", allow(), &fake{}, Options{})
	if out.Status != exitType || out.Stdout != "second\n" {
		t.Fatalf("%+v", out)
	}
}

// A defer is checked like any statement: no grant, no write; plan mode plans.
func TestDeferIsGrantChecked(t *testing.T) {
	out := Run("defer { unit restart nginx }\necho body", grant.Grant{}, &fake{}, Options{})
	if out.Status != exitDenied || !strings.Contains(out.Stderr, "denied unit.restart") {
		t.Fatalf("no grant: %+v", out)
	}
	p := &fake{}
	plan := Run("set -o plan\ndefer { unit restart nginx }\necho body", allow(), p, Options{})
	if !strings.Contains(plan.Stderr, "plan: would") || len(p.calls) != 0 {
		t.Fatalf("plan: %+v %v", plan, p.calls)
	}
}

func TestDeferRefusals(t *testing.T) {
	for _, src := range []string{"defer { break }", "for i in [ 1..2 ] { defer { continue } }", "fn f() { defer { return [ 1 ] } }"} {
		if out := Run(src, allow(), &fake{}, Options{}); out.Status != exitType {
			t.Errorf("%q: %+v", src, out)
		}
	}
	var b strings.Builder
	for i := 0; i < deferMax+1; i++ {
		b.WriteString("defer { echo x }\n")
	}
	if out := Run(b.String(), allow(), &fake{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "defer max") {
		t.Fatalf("cap: %+v", out)
	}
}

func TestFinallyRunsOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		src, want string
		status    int
	}{
		{"try { echo t } catch e { echo c } finally { echo f }\necho after", "t\nf\nafter\n", 0},
		{"try { echo \"{x\" | from json } catch e { echo c } finally { echo f }\necho after", "c\nf\nafter\n", 0},
		{"fn g() { try { return [ 1 ] } catch e { echo c } finally { echo f } }\nlet y = (g)\necho got $y", "f\ngot 1\n", 0},
		{"for i in [ 1..3 ] { try { break } catch e { } finally { echo f } }\necho out", "f\nout\n", 0},
		{"try { echo t } catch e { }\nfinally { echo f }", "t\nf\n", 0},
		// A failing finally replaces the status.
		{"try { echo t } catch e { } finally { echo \"{x\" | from json }\necho never", "t\n", exitType},
	} {
		out := Run(tc.src, allow(), &fake{}, Options{})
		if out.Status != tc.status || out.Stdout != tc.want {
			t.Errorf("%q: %+v want %q %d", tc.src, out, tc.want, tc.status)
		}
	}
}

// --needs and --lint see inside defer and finally.
func TestDeferAndFinallyAreStaticallyVisible(t *testing.T) {
	src := "defer { unit restart nginx }\ntry { ps } catch e { } finally { cat /var/log/syslog }"
	n := Needs(src, NeedsOptions{})
	if !strings.Contains(n.Stdout, "unit.restart") || !strings.Contains(n.Stdout, "/var/log/syslog") {
		t.Fatalf("needs: %+v", n)
	}
}
