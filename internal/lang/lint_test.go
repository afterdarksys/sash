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
	quiet := !strings.Contains(strings.ToLower(body), "secret")
	shown := !strings.Contains(body, "*") && strings.Contains(run.Stdout+run.Stderr, redacted)
	fault := quiet && strings.Contains(run.Stderr, errSecretFault.Error())
	refused := quiet && !fault && strings.Contains(run.Stderr, "secret")
	if !shown && !fault && !refused {
		return false
	}
	out := Lint(src)
	if out.Status == exitType {
		return true
	}
	rows := lintRows(t, out)
	// *** or a refusal is a disclosure: it needs the error. A secret that
	// ended the line on a failed evaluation needs at least the warning.
	sink := hasRule(rows, "secret-to-sink", "error")
	if (shown || refused) && !sink {
		t.Errorf("run showed or refused a secret, no secret-to-sink:\n%s\nrun %+v\nlint %+v", src, run, out)
	}
	if fault && !sink && !hasRule(rows, "secret-fault", "warning") {
		t.Errorf("a secret ended the line, lint is clean:\n%s\nrun %+v\nlint %+v", src, run, out)
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

// An evaluation over a secret that may fail ends the line at run time
// (errSecretFault, exit 2): lint reports it as secret-fault, a warning
// that #lint:allow can suppress, not as a sink.
func TestLintSecretFaultingEvaluation(t *testing.T) {
	for _, src := range []string{
		"declare -s tok = abc\nlet _a = [ {\"k\": $tok.A} ]",
		"declare -s tok = abc\nlet _a = $tok.A",
		"declare -s tok = abc\nlet _a = [ $tok[0] ]",
		"declare -s tok = abc\nlet _a = [ $tok + 1 ]",
		"declare -s tok = abc\nlet _a = [ len($tok) ]",
		"declare -s tok = abc\nlet _a = [ -$tok ]",
	} {
		run := Run(src, exprGrant(), &rowsParent{}, Options{})
		rows := lintRows(t, Lint(src))
		if !hasRule(rows, "secret-fault", "warning") || hasRule(rows, "secret-to-sink", "") {
			t.Errorf("%q: run %+v, lint %+v", src, run, rows)
		}
		if out := Lint(src + " #lint:allow secret-fault the value is always a string"); hasRule(lintRows(t, out), "secret-fault", "") {
			t.Errorf("%q: allow kept %s", src, out.Stdout)
		}
	}
}

// A fault inside a real sink is both: the sink stays an error.
func TestLintFaultInASinkIsStillASink(t *testing.T) {
	for _, src := range []string{"declare -s tok = abc\nfor _x in $tok.list { let _y = 1 }", "declare -s tok = abc\necho [ upper($tok) ]"} {
		rows := lintRows(t, Lint(src+" #lint:allow secret-fault reason"))
		if !hasRule(rows, "secret-to-sink", "error") {
			t.Errorf("%q: %+v", src, rows)
		}
	}
}

// A secret that only travels and never reaches a sink is not reported,
// and neither is a plain value in every sink.
func TestLintSecretNoFalseError(t *testing.T) {
	for _, src := range []string{
		"declare -s tok = abc\nlet _a = $tok\nlet _b = [ $_a ]\nlet _c = [ [$_a, {\"k\": $tok}] ]",
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
			[]string{"if [ true ] { echo }", "if [ 1 + 2 == 3 ] { echo }", "if [ upper(\"a\") == \"A\" ] { echo }", "if [ contains([1, 2], 2) ] { echo }", "check \"c\" [ contains(\"abc\", \"b\") ]", "check \"c\" [ true ]", "while [ true ] { echo }", "do { echo } while [ false ]", "let x = 1\nif [ $x ] { echo } else if [ false ] { echo }"},
			[]string{"while [ true ] { break }", "if [ uuid() == \"x\" ] { echo }", "let x = 1\nif [ $x == 1 ] { echo }", "let x = 1\ncheck \"c\" [ $x > 0 ]", "if ps { echo }"}},
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
		"exit 0\necho a #lint:allow unreachable kept for the demo":                          false,
		"exit 0\necho a # lint:allow unreachable kept for the demo":                         false,
		"let x = 1 #lint:allow unused-binding shown in docs":                                false,
		"exit 0\necho a #lint:allow unreachable":                                            true,
		"exit 0\necho a #lint:allow constant-check wrong rule":                              true,
		"exit 0 #lint:allow unreachable wrong line\necho a":                                 true,
		"exit 0\necho a #lint:allow nosuch-rule reason":                                     true,
		"$cmd #lint:allow incomplete-needs errors cannot be allowed":                        true,
		"declare -s t = a\necho $t #lint:allow secret-to-sink no way":                       true,
		"declare -s t = a\nlet _u = [ upper($t) ] #lint:allow secret-fault always a string": false,
		"declare -s t = a\nlet _u = [ upper($t) ] #lint:allow secret-fault":                 true,
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

// { } nesting past 64 is a parse error for every reader of the source;
// 64 itself parses.
func TestBlockDepthCap(t *testing.T) {
	nest := func(n int) string { return strings.Repeat("if [ true ] { ", n) + "echo in" + strings.Repeat(" }", n) }
	if out := Run(nest(blockDepthMax), grant.Grant{}, &countParent{}, Options{}); out.Status != 0 || out.Stdout != "in\n" {
		t.Fatalf("64 deep: %+v", out)
	}
	for _, src := range []string{nest(blockDepthMax + 1), strings.Repeat("fn f() { ", 65) + "ps" + strings.Repeat(" }", 65), strings.Repeat("try { ", 65) + "ps" + strings.Repeat(" } catch { }", 65)} {
		const msg = "parse: too deep"
		if out := Run(src, grant.Grant{}, &countParent{}, Options{}); out.Status != exitType || out.Stderr != msg+"\n" {
			t.Errorf("run %+v", out)
		}
		if pv := Preview(src, grant.Grant{}, Options{}); !pv.Block || pv.Smile != msg {
			t.Errorf("preview %+v", pv)
		}
		for name, out := range map[string]Outcome{"why": Why(src, grant.Grant{}, Options{}), "needs": Needs(src, NeedsOptions{}), "lint": Lint(src)} {
			if out.Status != exitType || out.Stdout != "" || !strings.Contains(out.Stderr, msg) {
				t.Errorf("%s %+v", name, out)
			}
		}
	}
}

// A catch name is tainted by any secret its try body reads, at any depth
// and through nested tries, even when the body sinks nothing.
func TestLintCatchTaint(t *testing.T) {
	for _, src := range []string{
		"declare -s tok = abc\ntry { let _q = [ $tok ] } catch e { let _r = 1 }\necho $e",
		"declare -s tok = abc\ntry { try { let _q = [ $tok ] } catch _f { let _r = 1 } } catch e { let _s = 1 }\necho $e",
		"declare -s tok = abc\ntry { try { let _q = 1 } catch { let _r = [ $tok ] } } catch e { let _s = 1 }\necho $e",
	} {
		found := false
		for _, r := range lintRows(t, Lint(src)) {
			found = found || (r.line == "3" && r.what == "secret e reaches echo")
		}
		if !found {
			t.Errorf("%q: %s", src, Lint(src).Stdout)
		}
	}
	src := "declare -s tok = abc\ntry { let _q = 1 } catch e { let _r = [ $tok ] }\necho $e"
	if strings.Contains(Lint(src).Stdout, "secret e") {
		t.Errorf("catch body taints its own name: %s", Lint(src).Stdout)
	}
}

// The channel rules: each fires once on its case, and the read-only
// section 1 example gets none of them. The send to recv edge carries
// secrets and answers, so the existing rules see through a channel.
func TestLintChannels(t *testing.T) {
	readOnly := `par 4 chan jobs(16), done(16) {
  branch {
    for p in ps |> where cmd == nginx { send jobs $p }
    for p in ps |> where cmd == redis { send jobs $p }
  }
  for j in recv jobs { unit status $j.cmd; send done [ {"cmd": $j.cmd} ] }
  for j in recv jobs { unit status $j.cmd; send done [ {"cmd": $j.cmd} ] }
  for r in recv done { echo $r.cmd }
}`
	for _, r := range lintRows(t, Lint(readOnly)) {
		if strings.HasPrefix(r.rule, "chan-") || r.rule == "unused-binding" {
			t.Errorf("read-only example: %+v", r)
		}
	}
	cases := []struct {
		src  string
		want lintRow
	}{
		{"par 2 chan c(1) { for p in ps { send c $p.cmd }; for j in recv c { unit restart $j } }",
			lintRow{"1", "chan-to-mutation", "warning", "received value ($j) reaches unit.restart"}},
		{"par 2 chan c(1) { for p in ps { send c $p.cmd }; for j in recv c { let u = [ $j ]; echo x > /var/tmp/ops/$u } }",
			lintRow{"1", "chan-to-mutation", "warning", "received value ($u) reaches a > redirect"}},
		{"par 3 chan c(1) { for p in ps { send c $p.cmd }; for j in recv c { echo $j }; for j in recv c { echo $j } }\npar 3 chan c(1) { for p in ps { send c $p.cmd }\nfor j in recv c { unit status nginx; unit restart nginx }\nfor j in recv c { unit restart nginx } }",
			lintRow{"2", "chan-order", "warning", "chan c: 2 receiving branches mutate; which acts on which item, and when, varies by run"}},
		{"declare -s tok = hunter2-long\npar 2 chan c(1) { send c $tok; for j in recv c { echo ok } }",
			lintRow{"2", "chan-secret", "warning", "secret tok is sent on c: it stays secret, in more branches"}},
		{"par 2 chan c(1) { send c 1; for j in recv c { if [ $j == 1 ] { break }; for k in [ [1] ] { break } } }",
			lintRow{"1", "chan-unread-risk", "info", "a receiver of c can break: values left at the join fail the par"}},
		{"par 2 chan c(1) { send c 1; try { for j in recv c { echo $j } } catch { echo no } }",
			lintRow{"1", "chan-unread-risk", "info", "a try around a receiver of c can stop it early: values left at the join fail the par"}},
		// Through the edge: a secret reaches a sink, an answer a mutation
		// and a subcommand, two channels deep.
		{"declare -s tok = hunter2-long\npar 3 chan a(1), b(1) { send a $tok; for x in recv a { send b $x }; for y in recv b { echo $y } }",
			lintRow{"2", "secret-to-sink", "error", "secret y reaches echo"}},
		{"let a = (term prompt \"op?\")\npar 2 chan c(1) { send c $a; for j in recv c { unit $j nginx } }",
			lintRow{"2", "prompt-subcommand", "error", "what a person typed at term prompt ($j) selects what unit does: exit 2 command_from_value"}},
		{"let a = (term prompt \"op?\")\npar 2 chan c(1) { send c $a; for j in recv c { unit restart $j } }",
			lintRow{"2", "prompt-to-mutation", "warning", "what a person typed at term prompt ($j) reaches unit.restart"}},
	}
	for _, c := range cases {
		rows := lintRows(t, Lint(c.src))
		n := 0
		for _, r := range rows {
			if r.rule == c.want.rule {
				n++
				if r != c.want {
					t.Errorf("%q: %+v, want %+v", c.src, r, c.want)
				}
			}
		}
		if n != 1 {
			t.Errorf("%q: %d %s rows: %+v", c.src, n, c.want.rule, rows)
		}
	}
	// A send reads its word: a name read only there is used.
	for _, r := range lintRows(t, Lint("let v = 1\npar 2 chan c(1) { send c $v; for j in recv c { echo $j } }")) {
		if r.rule == "unused-binding" && strings.Contains(r.what, " v") {
			t.Errorf("send read: %+v", r)
		}
	}
	// #lint:allow works for the warning.
	src := "par 2 chan c(1) { for p in ps { send c $p.cmd }; for j in recv c { unit restart $j } } #lint:allow chan-to-mutation the list is ours"
	if hasRule(lintRows(t, Lint(src)), "chan-to-mutation", "") {
		t.Errorf("allow")
	}
}
