package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

func runIn(st *State, src string) Outcome {
	return Run(src, exprGrant(), &rowsParent{}, Options{State: st})
}

func TestStateCarriesNamesFunctionsAndOptions(t *testing.T) {
	st := NewState()
	runIn(st, "let x = 41")
	runIn(st, "fn inc(n) { return [ $n + 1 ] }")
	runIn(st, "set -u")
	out := runIn(st, "inc $x")
	if out.Status != 0 || out.Stdout != "42\n" {
		t.Fatalf("%+v", out)
	}
	if out := runIn(st, "echo [ {}.missing ]"); out.Status != exitType || !strings.Contains(out.Stderr, "unset") {
		t.Fatalf("nounset carried: %+v", out)
	}
	if out := Run("echo [ {}.missing ]", exprGrant(), &rowsParent{}, Options{}); out.Status != 0 {
		t.Fatalf("a fresh run is not strict: %+v", out)
	}
}

func TestSetAndShoptShareOneTable(t *testing.T) {
	st := NewState()
	out := runIn(st, "set -e -o nounset; shopt -s noglob; set -x; set loopmax 50")
	if out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	out = runIn(st, "shopt")
	for _, want := range []string{"errexit  on", "nounset  on", "noglob   on", "xtrace   on", "pipefail  on", "loopmax  50"} {
		if !strings.Contains(strings.Join(strings.Fields(out.Stdout), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("missing %q in\n%s", want, out.Stdout)
		}
	}
	runIn(st, "set +e +x; shopt -u noglob")
	if st.errexit || st.xtrace || st.noglob || !st.nounset {
		t.Fatalf("%+v", st)
	}
	for _, bad := range []string{"set -z", "set -o nope", "set +o pipefail", "set loopmax 0", "set verbmax 10000001", "set loopmax x", "shopt -s", "shopt -q x"} {
		if out := runIn(NewState(), bad); out.Status != exitType {
			t.Errorf("%s: %+v", bad, out)
		}
	}
	if out := runIn(NewState(), "set |> where name == loopmax |> select value"); out.Stdout != "100000\n" {
		t.Fatalf("rows: %+v", out)
	}
}

func TestErrexit(t *testing.T) {
	out := runIn(NewState(), "set -e\negrep zzz <<< hi\necho after")
	if out.Stdout != "" || out.Status == 0 {
		t.Fatalf("errexit: %+v", out)
	}
	out = runIn(NewState(), "set -e\nif egrep zzz <<< hi { echo y }\negrep zzz <<< hi || echo ok\nwhile egrep zzz <<< hi { echo w }\necho after")
	if out.Stdout != "ok\nafter\n" {
		t.Fatalf("conditions are spared: %+v", out)
	}
	out = runIn(NewState(), "set -e\ntry { egrep zzz <<< hi } catch e { echo caught $e.status }\necho after")
	if out.Stdout != "caught 1\nafter\n" {
		t.Fatalf("try catches errexit: %+v", out)
	}
}

func TestNoglobKeepsStar(t *testing.T) {
	p := &listParent{rows: map[string][]parent.Row{"/var/log": {{"name": "a", "type": "file"}}}}
	st := NewState()
	runIn(st, "set -f")
	out := Run("echo *", exprGrant(), p, Options{State: st, Prefix: "/var/log"})
	got, _ := Expand("echo *", exprGrant(), p, Options{State: st, Prefix: "/var/log"})
	if got != "echo *" || out.Stdout != "*\n" || len(p.calls) != 0 {
		t.Fatalf("expand %q out %+v calls %d", got, out, len(p.calls))
	}
}

func TestDeclare(t *testing.T) {
	st := NewState()
	out := runIn(st, "declare -r host = miniapps1\ndeclare -i n = 5\ndeclare -a l = [ [1] ]\ndeclare -A m = [ {} ]\ndeclare")
	if out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	flat := strings.Join(strings.Fields(out.Stdout), " ")
	if strings.Contains(out.Stdout, "miniapps1") || !strings.Contains(flat, "host string r") || !strings.Contains(flat, "n number i") {
		t.Fatalf("listing shows values or misses flags:\n%s", out.Stdout)
	}
	if out := runIn(st, "declare -p host"); !strings.Contains(out.Stdout, "miniapps1") {
		t.Fatalf("-p: %+v", out)
	}
	for src, msg := range map[string]string{
		"let host = x":                       "readonly host",
		"declare host = x":                   "readonly host",
		"for host in [ [1] ] { echo }":       "readonly host",
		"fn f(host) { echo }\nf a":           "readonly host",
		"try { nosuch } catch host { echo }": "readonly host",
		"let n = abc":                        "type n number",
		"let l = [ {} ]":                     "type l list",
		"let m = [ [] ]":                     "type m map",
		"declare -x e = 1":                   "no export",
		"declare -i -a q = 1":                "one of -i -a -A",
		"declare -r z":                       "needs a value",
		"declare -p nope":                    "unset nope",
	} {
		out := runIn(st, src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%q: %+v want %q", src, out, msg)
		}
	}
	if out := runIn(st, "let n = 7; echo [ $n + 1 ]"); out.Stdout != "8\n" {
		t.Fatalf("typed number: %+v", out)
	}
}

// No option weakens a check: with every option on, a denied verb is still
// denied and runs nothing.
func TestNoOptionWeakensTheGrant(t *testing.T) {
	st := NewState()
	runIn(st, "set -e -u -x -f; set loopmax 10000000; set verbmax 10000000")
	p := &listParent{}
	out := Run("unit restart nginx", grant.Grant{V: "sash/1", Entitlements: []string{"ps"}}, p, Options{State: st})
	if out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("%+v calls %d", out, len(p.calls))
	}
	out = Run("cat /etc/passwd", exprGrant(), p, Options{State: st})
	if out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("%+v calls %d", out, len(p.calls))
	}
}

// Review fixes: expansion is capped, caps cannot be caught, and names with
// brackets are quoted.
func TestCapsHoldAgainstLoopsAndTry(t *testing.T) {
	out := runIn(NewState(), "let s = x\nwhile [ true ] { let s = \"$s$s\" }")
	if out.Status != exitType || !strings.Contains(out.Stderr, "too large") {
		t.Fatalf("doubling: %+v", out)
	}
	out = runIn(NewState(), "set loopmax 10\nlet n = 0\nwhile [ true ] { try { while [ true ] { let x = 1 } } catch { let n = [ $n + 1 ] } }\necho $n")
	if out.Status != exitType || !strings.Contains(out.Stderr, "loop max") || strings.Contains(out.Stdout, "10") {
		t.Fatalf("try caught a cap: %+v", out)
	}
	if q, ok := quoteArg("/var/log/]"); !ok || q != "'/var/log/]'" {
		t.Fatalf("bracket name %q %v", q, ok)
	}
	p := hostileDir("]", "[")
	if out := Run("echo [ 1 * ]", injectGrant(), p, Options{Prefix: "/var/log"}); strings.Contains(out.Stderr, "unknown name") {
		t.Fatalf("name text reached the expression parser: %+v", out)
	}
}

// Review fixes: a scope cannot make its name read-only or typed, typed
// names cannot be scope-bound, and the TOML pre-check counts every bracket.
func TestScopesAndReadonly(t *testing.T) {
	for src, msg := range map[string]string{
		"let x = a; for x in [ [1] ] { declare -r x = 5 }": "loop, catch, or parameter",
		"try { nosuch } catch e { declare -r e = 1 }":      "loop, catch, or parameter",
		"fn f(p) { declare -i p }\nf 1":                    "loop, catch, or parameter",
		"declare -i n = 1; for n in [ [\"x\"] ] { echo }":  "typed n",
		"declare -A m = [ {} ]\nfn f(m) { echo }\nf 1":     "typed m",
	} {
		out := runIn(NewState(), src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%q: %+v want %q", src, out, msg)
		}
	}
	st := NewState()
	runIn(st, "let x = a; for y in [ [1] ] { declare -r x = 5 }")
	if out := runIn(st, "echo $x; let x = b"); out.Stdout != "5\n" || out.Status != exitType {
		t.Fatalf("declare -r of an outer name inside a loop holds: %+v", out)
	}
}

func TestFormatReviewFixes(t *testing.T) {
	crafted := "a = \"\"\"\n" + strings.Repeat("]", 20000) + "\n\"\"\"\nb = " + strings.Repeat("[", 20000) + "1" + strings.Repeat("]", 20000) + "\n"
	if out := yamlRun(t, crafted, "from toml"); out.Status != exitType || !strings.Contains(out.Stderr, "toml too deep") {
		t.Fatalf("toml brackets in a string: %+v", out)
	}
	big := strings.Repeat("a", (1<<20)+1)
	for _, f := range []string{"yaml", "toml"} {
		if out := yamlRun(t, big, "from "+f); out.Status != exitType || !strings.Contains(out.Stderr, f+" too large") {
			t.Errorf("%s input cap: %+v", f, out.Stderr)
		}
	}
	for doc, msg := range map[string]string{"a: !evil [1]\n": "yaml tag !evil", "a: !evil {b: 1}\n": "yaml tag !evil"} {
		if out := yamlRun(t, doc, "from yaml"); out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%q: %+v", doc, out)
		}
	}
	if _, err := decodeJSON([]byte("[" + strings.Repeat(`"xxxxxxxxxxxxxxxx",`, 300000) + "1]")); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("json streaming budget: %v", err)
	}
}
