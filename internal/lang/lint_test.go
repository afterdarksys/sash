package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

type lintRow struct{ line, rule, sev, what string }

func lintRows(t *testing.T, out Outcome) []lintRow {
	t.Helper()
	if out.Stdout == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(out.Stdout, "\n"), "\n")
	if f := strings.Fields(lines[0]); strings.Join(f, " ") != "line rule severity what" {
		t.Fatalf("header %q", lines[0])
	}
	var rows []lintRow
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 4 {
			t.Fatalf("row %q", l)
		}
		rows = append(rows, lintRow{f[0], f[1], f[2], strings.Join(f[3:], " ")})
	}
	return rows
}

func hasRule(rows []lintRow, rule, sev string) bool {
	for _, r := range rows {
		if r.rule == rule && (sev == "" || r.sev == sev) {
			return true
		}
	}
	return false
}

// lintSinks are every sink in the secret-to-sink row of the contract,
// plus the ones a run refuses for the same reason. %s is where the
// secret goes.
var lintSinks = []string{
	"echo %s",
	"echo hi | egrep %s",
	"log info %s",
	"log info x [ {\"k\": %s} ]",
	"check %s [ true ]",
	"check \"c\" [ %s == 1 ]",
	"if [ %s == 1 ] { echo y }",
	"if echo %s { echo y }",
	"while [ %s == 1 ] { break }",
	"do { echo y } while [ %s == 1 ]",
	"ps |> filter [ %s == 1 ]",
	"echo %s | template /var/log/t",
	"template_file %s /var/log/t /tmp/o",
	"template %s",
	"unit restart %s",
	"unit status %s",
	"cat %s",
	"config read %s",
	"echo hi > %s",
	"egrep a < %s",
	"fn f(a) { echo ok }\nf %s",
	"exit %s",
	"fn g() { return %s }",
	"use %s",
	"with prefix %s { ps }",
	"retry %s { ps }",
	"timeout %s { ps }",
	"let w = \"pre%s\"",
	"let w = (unit restart %s)",
	"let w = (echo %s | egrep x)",
	"let w = (echo %s | template /var/log/t)",
	"%s",
}

// lintVias carry the secret to the sink: %s is the secret word.
var lintVias = []struct{ bind, use string }{
	{"", "$tok"},
	{"", "[ $tok ]"},
	{"", "[ upper($tok) ]"},
	{"let a = $tok\n", "$a"},
	{"let a = [ len($tok) ]\n", "$a"},
	{"let a = (echo $tok)\n", "$a"},
	{"declare a = $tok\n", "$a"},
	{"let a = $tok\nlet b = [ $a ]\nlet c = $b\n", "$c"},
	{"for a in [ [$tok] ] { echo ok }\n", "$a"},
	{"for i, a in [ {\"k\": $tok} ] { echo ok }\n", "$a"},
	{"with a = $tok { echo ok }\n", "$a"},
	{"fn h(p) { echo ok }\nh $tok\n", "$p"},
	{"try { echo [ $tok + 1 ] } catch a { echo ok }\n", "$a"},
	{"let m = [ {\"k\": $tok} ]\n", "$m.k"},
}

// Every sink, through every way a secret travels, is a secret-to-sink
// error, and #lint:allow cannot hide one.
func TestLintSecretToSinkEverySink(t *testing.T) {
	for _, via := range lintVias {
		for _, sink := range lintSinks {
			src := "declare -s tok = abc\n" + via.bind + strings.ReplaceAll(sink, "%s", via.use)
			for _, s := range []string{src, src + " #lint:allow secret-to-sink it is fine"} {
				out := Lint(s)
				rows := lintRows(t, out)
				if out.Status != 1 || !hasRule(rows, "secret-to-sink", "error") {
					t.Errorf("missed sink:\n%s\n%+v %q", s, out, out.Stderr)
				}
			}
		}
	}
}

// declare -p prints a secret as ***: it is a sink by name.
func TestLintDeclarePrintsSecret(t *testing.T) {
	for _, src := range []string{"declare -s tok = abc\ndeclare -p tok", "declare -s tok = abc\nlet a = [ $tok ]\ndeclare -p a"} {
		if !hasRule(lintRows(t, Lint(src)), "secret-to-sink", "error") {
			t.Errorf("%q", src)
		}
	}
}

// The over-report guarantee, checked against the run: whenever a run
// prints *** or refuses for a secret, lint reports secret-to-sink. A
// lint-clean script with a secret sink is impossible for every script
// here, and the fuzz target extends it.
func TestLintCleanScriptCannotSinkASecret(t *testing.T) {
	confirmed := 0
	for _, via := range lintVias {
		for _, sink := range lintSinks {
			src := "declare -s tok = abc\n" + via.bind + strings.ReplaceAll(sink, "%s", via.use)
			if lintSecretCheck(t, src) {
				confirmed++
			}
		}
	}
	if confirmed < 100 {
		t.Fatalf("only %d scripts showed the secret at run time", confirmed)
	}
}

// lintSecretCheck runs src; when the run shows or refuses a secret, lint
// must report secret-to-sink. It reports whether the run did.
func lintSecretCheck(t *testing.T, src string) bool {
	t.Helper()
	g := grant.Grant{
		V: "sash/1", Entitlements: []string{"ps", "file.read", "file.write", "unit.status", "unit.restart", "config.read"},
		FileRead: []string{"/"}, FileWrite: []string{"/"},
	}
	run := Run(src, g, &rowsParent{}, Options{})
	// The script's own text can spell *** or secret; then only the other
	// signal counts.
	body := strings.TrimPrefix(src, "declare -s tok = abc\n")
	shown := (!strings.Contains(body, "*") && strings.Contains(run.Stdout+run.Stderr, redacted)) ||
		(!strings.Contains(strings.ToLower(body), "secret") && strings.Contains(run.Stderr, "secret"))
	if !shown {
		return false
	}
	out := Lint(src)
	if out.Status == exitType {
		return true
	}
	if !hasRule(lintRows(t, out), "secret-to-sink", "error") {
		t.Errorf("run showed a secret, lint is clean:\n%s\nrun %+v\nlint %+v", src, run, out)
	}
	return true
}

func FuzzLintNeverMissesASecret(f *testing.F) {
	for _, via := range lintVias {
		for _, sink := range lintSinks {
			f.Add(via.bind + strings.ReplaceAll(sink, "%s", via.use))
		}
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 512 || strings.ContainsAny(s, "*!{}") && strings.Contains(s, "use") {
			return
		}
		lintSecretCheck(t, "declare -s tok = abc\n"+s)
	})
}

// A secret that only travels and never reaches a sink is not reported,
// and neither is a plain value in every sink.
func TestLintSecretNoFalseError(t *testing.T) {
	for _, src := range []string{
		"declare -s tok = abc\nlet _a = $tok\nlet _b = [ upper($_a) ]",
		"declare -s tok = abc\nlet _a = (echo $tok | head 1)",
		"let x = abc\necho $x\nunit status $x",
	} {
		if hasRule(lintRows(t, Lint(src)), "secret-to-sink", "") {
			t.Errorf("false sink: %q %s", src, Lint(src).Stdout)
		}
	}
}

func TestLintRulesTriggerAndDoNot(t *testing.T) {
	cases := []struct {
		rule string
		yes  []string
		no   []string
	}{
		{"unused-binding",
			[]string{"let x = 1", "declare x = 1", "for r in ps { echo hi }", "try { ps } catch e { echo no }", "fn f(a) { echo hi }", "for i, r in ps { echo $r }", "if [ true ] { let y = 2 }\nuse /var/log/l.sash"},
			[]string{"let _x = 1", "declare -r x = 1", "let x = 1\nuse /var/log/l.sash", "let x = 1\necho $x", "let x = 1\necho [ $x + 1 ]", "let m = 1\necho $m.k", "let x = 1\nfor y in $x { echo $y }", "let x = 1\ndeclare -p x"}},
		{"unreachable",
			[]string{"exit 0\necho a", "fn f() { return 1; echo a }", "for i in [ 1..2 ] { break; echo a }", "for i in [ 1..2 ] { continue\necho a }"},
			[]string{"if [ $x ] { exit 1 } else { exit 2 }\necho a", "echo a\nexit 0"}},
		{"constant-check",
			[]string{"if [ true ] { echo }", "if [ 1 + 2 == 3 ] { echo }", "if [ upper(\"a\") == \"A\" ] { echo }", "check \"c\" [ true ]", "while [ true ] { echo }", "do { echo } while [ false ]", "let x = 1\nif [ $x ] { echo } else if [ false ] { echo }"},
			[]string{"while [ true ] { break }", "let x = 1\nif [ $x == 1 ] { echo }", "let x = 1\ncheck \"c\" [ $x > 0 ]", "if ps { echo }"}},
		{"retry-on-mutation",
			[]string{"retry 3 { unit restart nginx }", "retry 3 { package install vim }", "retry 3 { sysctl set a 1 }", "retry 3 { echo a > /tmp/x }", "retry 3 { template_file /var/log/s.yaml /var/log/t /tmp/o }", "fn f() { unit restart x }\nretry 3 { f }", "fn g() { unit restart x }\nfn f() { g }\nretry 3 { f }", "let op = restart\nretry 2 { unit $op x }", "retry 2 { try { ps } catch { unit restart x } }"},
			[]string{"retry 3 { ps }", "retry 3 { try { unit restart x } catch { echo no } }", "retry 3 { template_file /var/log/s.yaml /var/log/t /tmp/o --check }", "fn f() { unit restart x }\nretry 3 { ps }"}},
		{"silent-catch",
			[]string{"try { ps } catch { }", "try { ps } catch e {\n}"},
			[]string{"try { ps } catch { echo no }"}},
		{"incomplete-needs",
			[]string{"journal", "let u = x\nunit status $u", "cat rel", "$cmd"},
			[]string{"unit status nginx", "cat /etc/hosts"}},
		{"shadowed-secret",
			[]string{"declare -s t = a\nlet t = b", "declare -s t = a\ndeclare t = b", "declare -s t = a\nfor t in ps { echo }", "declare -s t = a\ntry { ps } catch t { echo }", "declare -s t = a\nfn f(t) { echo }", "declare -s t = a\nwith t = b { echo }"},
			[]string{"declare -s t = a\ndeclare -s t = b"}},
	}
	for _, c := range cases {
		for _, src := range c.yes {
			out := Lint(src)
			if !hasRule(lintRows(t, out), c.rule, "") {
				t.Errorf("%s missed %q:\n%s%s", c.rule, src, out.Stdout, out.Stderr)
			}
		}
		for _, src := range c.no {
			out := Lint(src)
			if hasRule(lintRows(t, out), c.rule, "") {
				t.Errorf("%s on %q:\n%s", c.rule, src, out.Stdout)
			}
		}
	}
}

// incomplete-needs is the Needs rows with a note, line by line, and an
// error for command_from_value.
func TestLintIncompleteNeedsIsTheNeedsRows(t *testing.T) {
	src := "journal\nlet u = x\nunit status $u\ncat rel\nps\njournal\n$cmd x"
	var want []string
	for _, r := range needsTableRows(t, Needs(src, NeedsOptions{})) {
		if r[4] == "" {
			continue
		}
		for _, ln := range strings.Split(r[3], ",") {
			want = append(want, ln+" "+r[0]+" "+r[1]+": "+r[4])
		}
	}
	var got []string
	for _, r := range lintRows(t, Lint(src)) {
		if r.rule == "incomplete-needs" {
			got = append(got, r.line+" "+r.what)
			if want := map[bool]string{true: "error", false: "warning"}[strings.Contains(r.what, "command_from_value")]; r.sev != want {
				t.Errorf("%v severity", r)
			}
		}
	}
	if len(got) != len(want) || len(got) != 5 {
		t.Fatalf("got %q want %q", got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}

func TestLintOutputAndExit(t *testing.T) {
	if out := Lint("ps\necho hi"); out.Status != 0 || out.Stdout != "" || out.Stderr != "" {
		t.Fatalf("clean %+v", out)
	}
	if out := Lint("let x = 1"); out.Status != 0 || !strings.Contains(out.Stdout, "unused-binding") {
		t.Fatalf("info only %+v", out)
	}
	for _, src := range []string{"try { ps }", "cat (", "echo 'open", "!!"} {
		if out := Lint(src); out.Status != exitType || out.Stdout != "" || out.Stderr == "" {
			t.Errorf("%q: %+v", src, out)
		}
	}
	src := "if [ true ] { echo }\nlet z = 1\nexit 0\necho a\ntry { ps } catch { }"
	out := Lint(src)
	rows := lintRows(t, out)
	want := []lintRow{
		{"1", "constant-check", "warning", "if [ ] reads no name"},
		{"2", "unused-binding", "info", "let z is never read"},
		{"4", "unreachable", "warning", "statement after exit"},
		{"5", "silent-catch", "warning", "empty catch drops the failure"},
	}
	if out.Status != 1 || len(rows) != len(want) {
		t.Fatalf("%+v", out)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d %+v want %+v", i, rows[i], want[i])
		}
	}
	// Cells are escaped: a crafted target cannot drive the terminal.
	out = Lint("cat 'rel\x1b]0;pwn\x07\u202e'")
	if strings.ContainsAny(out.Stdout, "\x1b\x07\u202e") || !strings.Contains(out.Stdout, `rel\x1b]0;pwn\x07\u202e`) {
		t.Fatalf("escape %q", out.Stdout)
	}
	// what names, never values: the secret literal is not printed.
	out = Lint("declare -s tok = hunter2\necho $tok\nlet tok = hunter2b")
	if strings.Contains(out.Stdout, "hunter2") {
		t.Fatalf("printed a secret literal %q", out.Stdout)
	}
}

func TestLintAllow(t *testing.T) {
	for src, kept := range map[string]bool{
		"exit 0\necho a #lint:allow unreachable kept for the demo":    false,
		"exit 0\necho a # lint:allow unreachable kept for the demo":   false,
		"let x = 1 #lint:allow unused-binding shown in docs":          false,
		"exit 0\necho a #lint:allow unreachable":                      true,
		"exit 0\necho a #lint:allow constant-check wrong rule":        true,
		"exit 0 #lint:allow unreachable wrong line\necho a":           true,
		"exit 0\necho a #lint:allow nosuch-rule reason":               true,
		"$cmd #lint:allow incomplete-needs errors cannot be allowed":  true,
		"declare -s t = a\necho $t #lint:allow secret-to-sink no way": true,
	} {
		out := Lint(src)
		if (out.Stdout != "") != kept {
			t.Errorf("%q kept %v: %q", src, kept, out.Stdout)
		}
	}
}

// Taint follows a call into the parameter: the sink inside the body is
// reported on its own line, not only the call.
func TestLintTaintReachesFnBody(t *testing.T) {
	rows := lintRows(t, Lint("declare -s tok = abc\nfn h(p) {\n  echo $p\n}\nh $tok"))
	for _, r := range rows {
		if r.line == "3" && r.rule == "secret-to-sink" && r.what == "secret p reaches echo" {
			return
		}
	}
	t.Fatalf("%+v", rows)
}
