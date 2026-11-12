package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

func flow(t *testing.T, src string) Outcome {
	t.Helper()
	return Run(src, exprGrant(), &rowsParent{}, Options{})
}

func TestFlowForms(t *testing.T) {
	for src, want := range map[string]string{
		"let i = 0; while [ $i < 3 ] { echo $i; let i = [ $i + 1 ] }":                          "0\n1\n2\n",
		"do { echo once } while [ false ]":                                                     "once\n",
		"let i = 0; do { let i = [ $i + 1 ] } while [ $i < 5 ]; echo $i":                       "5\n",
		"for x in [ [\"a\", \"b\"] ] { echo $x }":                                              "a\nb\n",
		"for i, x in [ [\"a\", \"b\"] ] { echo $i $x }":                                        "0 a\n1 b\n",
		"let m = [ {\"k\": 1, \"j\": 2} ]; for k in $m { echo $k }":                            "k\nj\n",
		"let m = [ {\"k\": 1} ]; for k, v in $m { echo $k $v }":                                "k 1\n",
		"for i in [ 1..10 ] { if [ $i == 2 ] { continue }; if [ $i > 4 ] { break }; echo $i }": "1\n3\n4\n",
		"for i in [ 1..2 ] { for j in [ 1..3 ] { if [ $j == 2 ] { break }; echo $i $j } }":     "1 1\n2 1\n",
		"for i, r in ps { echo $i $r.cmd }":                                                    "0 sshd\n1 init\n",
		"if [ 1 > 2 ] { echo a } else if [ 2 > 1 ] { echo b } else { echo c }":                 "b\n",
		"if echo x { echo a }\necho b":                                                         "a\nb\n",
		"if [ false ] { echo a }\nelse { echo c }":                                             "c\n",
		"let x = keep; for x in [ [1] ] { echo $x }; echo $x":                                  "1\nkeep\n",
	} {
		out := flow(t, src)
		if out.Status != 0 || out.Stdout != want {
			t.Errorf("%q: status %d out %q err %q, want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestFlowFunctionsReturnValues(t *testing.T) {
	out := flow(t, "fn add(a, b) { return [ $a + $b ] }\nadd 2 3\nlet s = (add 4 5)\necho got [ $s * 2 ]\nfn none() { echo side; return }\nnone")
	if out.Status != 0 || out.Stdout != "5\ngot 18\nside\n" {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
	out = flow(t, "fn firstOf(l) { for x in [ [1, 2, 3] ] { return $x } }\nfirstOf q")
	if out.Status != 0 || out.Stdout != "1\n" {
		t.Fatalf("return from loop: status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
}

func TestFlowExit(t *testing.T) {
	out := flow(t, "echo a\nexit 7\necho b")
	if out.Status != 7 || !out.Exit || out.Stdout != "a\n" {
		t.Fatalf("exit: %+v", out)
	}
	out = flow(t, "fn f() { exit 3 }\nf\necho no")
	if out.Status != 3 || !out.Exit || out.Stdout != "" {
		t.Fatalf("exit in fn: %+v", out)
	}
	out = flow(t, "try { exit 4 } catch e { echo caught }")
	if out.Status != 4 || !out.Exit || out.Stdout != "" {
		t.Fatalf("exit through try: %+v", out)
	}
	for _, bad := range []string{"exit 256", "exit -1", "exit x", "exit [ 1.5 ]"} {
		if out := flow(t, bad); out.Status != exitType || out.Exit {
			t.Errorf("%s: %+v", bad, out)
		}
	}
}

func TestFlowJumpsOutsideTheirPlace(t *testing.T) {
	for src, msg := range map[string]string{
		"break":    "usage break",
		"continue": "usage continue",
		"return":   "usage return",
		"fn f() { break }\nfor x in [ [1] ] { f }": "usage break",
		"do { echo x }":                  "do needs while",
		"for x in [ 5 ] { echo $x }":     "type for number",
		"for x in [ \"s\" ] { echo $x }": "type for string",
	} {
		out := flow(t, src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%q: status %d err %q want %q", src, out.Status, out.Stderr, msg)
		}
	}
}

// TestBareTryIsParseError: a try with no catch would drop a failure and
// its stderr. Every reader of the source refuses it the same way.
func TestBareTryIsParseError(t *testing.T) {
	for _, src := range []string{
		"try { nosuch }\necho after",
		"try { echo \"{bad\" | from json }",
		"try { unit restart nginx }; echo after",
		"retry 2 { try { nosuch } }",
		"fn f() { try { nosuch } }",
		"if [ true ] { try { nosuch } } else { echo no }",
		"try { nosuch } catch e { try { nosuch } }",
		"try { nosuch }\n\n",
		"try { nosuch } # catch { }",
	} {
		c := &countParent{}
		out := Run(src, exprGrant(), c, Options{})
		if out.Status != exitType || out.Stderr != "parse: try needs catch\n" || out.Stdout != "" || c.n != 0 {
			t.Errorf("run %q: %+v calls %d", src, out, c.n)
		}
		pv := Preview(src, exprGrant(), Options{})
		if !pv.Block || pv.Smile != "parse: try needs catch" || len(pv.Spans) != 0 {
			t.Errorf("preview %q: %+v", src, pv)
		}
		why := Why(src, exprGrant(), Options{})
		if why.Status != exitType || !strings.Contains(why.Stderr, "try needs catch") || why.Stdout != "" {
			t.Errorf("why %q: %+v", src, why)
		}
		needs := Needs(src, NeedsOptions{})
		if needs.Status != exitType || !strings.Contains(needs.Stderr, "try needs catch") || needs.Stdout != "" {
			t.Errorf("needs %q: %+v", src, needs)
		}
	}
	out := flow(t, "try { nosuch }\ncatch e { echo $e.status }")
	if out.Status != 0 || out.Stdout != "127\n" {
		t.Fatalf("catch on the next line: %+v", out)
	}
}

func TestFlowTry(t *testing.T) {
	out := flow(t, "try { unit restart nginx } catch e { echo $e.key $e.status $e.message }\necho after")
	if out.Status != 0 || out.Stdout != "unit.restart 126 denied unit.restart\nafter\n" || out.Stderr != "" {
		t.Fatalf("caught denial: %+v", out)
	}
	out = flow(t, "try { nosuch } catch { }\necho after")
	if out.Status != 0 || out.Stdout != "after\n" || out.Stderr != "" {
		t.Fatalf("empty catch: %+v", out)
	}
	out = flow(t, "let e = keep\ntry { echo [ 1 / 0 ] } catch e { echo $e.status }\necho $e")
	if out.Status != 0 || out.Stdout != "2\nkeep\n" {
		t.Fatalf("catch name scope: %+v", out)
	}
	out = flow(t, "for i in [ 1..3 ] { try { if [ $i == 2 ] { break } } catch { echo no }; echo $i }")
	if out.Status != 0 || out.Stdout != "1\n" {
		t.Fatalf("break through try: %+v", out)
	}
	out = flow(t, "try { nosuch } catch e { unit restart x }")
	if out.Status != exitDenied || !strings.Contains(out.Stderr, "denied unit.restart") {
		t.Fatalf("failure in catch: %+v", out)
	}
}

func TestFlowLoopMax(t *testing.T) {
	out := flow(t, "while [ true ] { let x = 1 }")
	if out.Status != exitType || !strings.Contains(out.Stderr, "loop max") {
		t.Fatalf("while: %+v", out)
	}
	out = flow(t, "for i in [ 1..100001 ] { let x = 1 }")
	if out.Status != exitType || !strings.Contains(out.Stderr, "loop max") {
		t.Fatalf("for: %+v", out)
	}
	if out := flow(t, "for i in [ 1..100000 ] { let x = 1 }"); out.Status != 0 {
		t.Fatalf("at the cap: %+v", out)
	}
}

type tallyParent struct{ n int }

func (p *tallyParent) Do(parent.Verb) (parent.Result, error) {
	p.n++
	return parent.Result{Kind: parent.KindRows, Cols: []string{"pid"}, Rows: []parent.Row{{"pid": "1"}}}, nil
}

// A loop of verbs stops at verbMax: the parent sees no call past it.
func TestFlowVerbMax(t *testing.T) {
	p := &tallyParent{}
	out := Run("for i in [ 1..20000 ] { let r = (ps) }", grant.Grant{V: "sash/1", Entitlements: []string{"ps"}}, p, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "verb max") || p.n != verbMaxDefault {
		t.Fatalf("status %d err %q calls %d", out.Status, out.Stderr, p.n)
	}
}

// Preview still finds a denial inside a while body or a try.
func TestPreviewSeesInsideNewBlocks(t *testing.T) {
	g := exprGrant()
	for _, src := range []string{
		"while [ true ] { unit restart nginx }",
		"try { unit restart nginx } catch { echo x }",
		"try { echo x } catch { unit restart nginx }",
		"do { unit restart nginx } while [ false ]",
	} {
		if pv := Preview(src, g, Options{}); !pv.Denied || !pv.Block {
			t.Errorf("%q: %+v", src, pv)
		}
	}
}
