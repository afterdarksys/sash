package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

func exprGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Purpose: "terminal:open", Entitlements: []string{"ps", "file.read"}, FileRead: []string{"/var/log"}}
}

func runOut(t *testing.T, src string) Outcome {
	t.Helper()
	return Run(src, exprGrant(), &listParent{}, Options{Prefix: "/var/log"})
}

func TestExprValuesAndOperators(t *testing.T) {
	for src, want := range map[string]string{
		`echo [ 2 + 3 * 4 ]`:                             "14",
		`echo [ (2 + 3) * 4 ]`:                           "20",
		`echo [ 10 / 4 ] [ 10 / 5 ] [ 7 % 3 ]`:           "2.5 2 1",
		`echo [ -3 + 1 ] [ 1.5 * 2 ]`:                    "-2 3",
		`echo [ "ab" + "cd" ] [ "ab" * 3 ]`:              "abcd ababab",
		`echo [ [1, 2] + [3] ]`:                          "[1,2,3]",
		`echo [ 3 in [1, 2, 3] ] [ "a" in {"a": 1} ]`:    "true true",
		`echo [ "ell" in "hello" ] [ 4 in [1] ]`:         "true false",
		`echo [ 1 == 1.0 ] [ "10" == 10 ] [ "a" < "b" ]`: "true true true",
		`echo [ !true || false ] [ 1 && "x" ]`:           "false true",
		`echo [ 1..4 ] [ 3..1 ]`:                         "[1,2,3,4] [3,2,1]",
		`echo [ null ] [ {"b": 1, "a": 2} ]`:             `null {"b":1,"a":2}`,
		`echo [ [1, 2, 3][-1] ] [ {"a": {"b": 5}}.a.b ]`: "3 5",
		`echo [ "say \"hi\"\n" ]`:                        "say \"hi\"\n",
	} {
		out := runOut(t, src)
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q, want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestExprLetIfAndPaths(t *testing.T) {
	src := `let cfg = [ {"server": {"port": 8080, "hosts": ["a", "b"]}} ]
let n = [ $cfg.server.port + 1 ]
echo $n $cfg.server.hosts[1] $cfg.server["port"] $cfg.missing
if [ $n > 8080 && "b" in $cfg.server.hosts ] { echo yes } else { echo no }
let s = 41
echo [ $s + 1 ]`
	out := runOut(t, src)
	if out.Status != 0 || out.Stdout != "8081 b 8080 null\nyes\n42\n" {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
}

func TestExprRowsAreLists(t *testing.T) {
	p := &rowsParent{}
	src := `let ps = (ps)
echo [ $ps[0].pid > 100 ] [ $ps[1].user ]
for r in ps { if [ $r.pid > 100 ] { echo $r.cmd } }`
	out := Run(src, exprGrant(), p, Options{})
	if out.Status != 0 || out.Stdout != "true root\nsshd\n" {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
}

type rowsParent struct{}

func (rowsParent) Do(v parent.Verb) (parent.Result, error) {
	return parent.Result{Kind: parent.KindRows, Cols: []string{"user", "pid", "cmd"}, Rows: []parent.Row{
		{"user": "ryan", "pid": "1234", "cmd": "sshd"},
		{"user": "root", "pid": "1", "cmd": "init"},
	}}, nil
}

func TestExprErrorsExitTwo(t *testing.T) {
	for src, msg := range map[string]string{
		`echo [ $nope ]`:                   "unset nope",
		`echo [ 1 / 0 ]`:                   "div zero",
		`echo [ 1 % 0 ]`:                   "div zero",
		`echo [ 9223372036854775807 + 1 ]`: "overflow",
		`echo [ 9223372036854775807 * 2 ]`: "overflow",
		`echo [ [1][5] ]`:                  "index range",
		`echo [ "a" - 1 ]`:                 "type expr",
		`echo [ "a" < 1 ]`:                 "compare",
		`echo [ 1..20000000 ]`:             "range too large",
		`echo [ "x" * 99999999 ]`:          "repeat",
		`echo [ foo ]`:                     "unknown name",
		`echo [ 1 + ]`:                     "incomplete",
		`echo [ {"a": 1, "a": 2} ]`:        "duplicate key",
		`echo [ 1 2 ]`:                     "trailing",
		`echo [ "\q" ]`:                    "escape",
		`echo [ ` + strings.Repeat("(", 80) + "1" + strings.Repeat(")", 80) + ` ]`: "too deep",
		`echo [ ` + strings.Repeat("[", 80) + strings.Repeat("]", 80) + ` ]`:       "too deep",
	} {
		out := runOut(t, src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: status %d err %q, want %q", src, out.Status, out.Stderr, msg)
		}
	}
}

// Inside [ ], "*" is multiplication, "{" a map, and "!" a not: none of them
// reaches glob, brace, or history expansion, so nothing is listed.
func TestExprTextIsNotExpanded(t *testing.T) {
	p := &listParent{rows: map[string][]parent.Row{"/var/log": {{"name": "a.log"}}}}
	opt := Options{Prefix: "/var/log", History: []string{"rm everything"}}
	for src, want := range map[string]string{
		`echo [ 2 * 3 ]`:                  "6",
		`echo [ {"a": 1, "b": 2} ]`:       `{"a":1,"b":2}`,
		`echo [ !false ] [ 1 != 2 ]`:      "true true",
		`echo [ "*" + "{x,y}" + "!!" ]`:   "*{x,y}!!",
		`echo [ [1, [2, [3]]][1][1][0] ]`: "3",
	} {
		got, prob := Expand(src, exprGrant(), p, opt)
		if prob != nil || got != src {
			t.Errorf("Expand(%s) = %q %v", src, got, prob)
		}
		out := Run(src, exprGrant(), p, opt)
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q", src, out.Status, out.Stdout, out.Stderr)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("parent calls %v", p.calls)
	}
	// Outside [ ], the same characters still expand.
	if got, _ := Expand("echo {x,y}", exprGrant(), p, opt); got != "echo x y" {
		t.Fatalf("brace outside %q", got)
	}
}

func TestBracketWithoutSpaceStaysAWord(t *testing.T) {
	out := Run(`let x = hi; echo [0-9]+ a[1] "$x[abc]"`, exprGrant(), &listParent{}, Options{})
	if out.Status != 0 || out.Stdout != "[0-9]+ a[1] hi[abc]\n" {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
}

func TestWholeRowIsStillNotAWord(t *testing.T) {
	out := Run(`for r in ps { echo $r }`, exprGrant(), &rowsParent{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "type value") {
		t.Fatalf("status %d err %q", out.Status, out.Stderr)
	}
}

// An expression calls no verb: a verb name inside [ ] is not a call.
func TestExprCallsNoVerb(t *testing.T) {
	p := &listParent{}
	out := Run(`echo [ ps ]`, exprGrant(), p, Options{})
	if out.Status != exitType || len(p.calls) != 0 {
		t.Fatalf("status %d calls %d", out.Status, len(p.calls))
	}
}
