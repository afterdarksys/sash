package lang

import (
	"strings"
	"testing"
	"time"
)

func TestExprFunctions(t *testing.T) {
	for src, want := range map[string]string{
		`echo [ split("root:x:0", ":")[2] ] [ join(["a", 1, true], "-") ]`:                                  "0 a-1-true",
		`echo [ upper("ab") ] [ lower("AB") ] [ trim("  x  ") ] [ len("héllo") ]`:                           "AB ab x 5",
		`echo [ replace("a.b.c", ".", "/") ] [ starts("web01", "web") ] [ ends("x.log", ".log") ]`:          "a/b/c true true",
		`echo [ pad("x", 3) + "|" ] [ pad("x", -3) ] [ repeat("ab", 2) ]`:                                   "x  |   x abab",
		`echo [ match("web01", "^web[0-9]+$") ] [ find("a12b345", "[0-9]+") ] [ find("x", "[0-9]") ]`:       "true 12 null",
		`echo [ groups("user=ryan uid=501", "user=(\\w+) uid=(\\d+)") ] [ sub("a1b22", "[0-9]+", "<$0>") ]`: `["ryan","501"] a<1>b<22>`,
		`echo [ int("42") + int(3.9) ] [ float("2") ] [ str(5) + "x" ] [ bool("") ] [ type([]) ]`:           "45 2 5x false list",
		`echo [ sum([1, 2, "3"]) ] [ avg([1, 2]) ] [ min([3, 1]) ] [ max(["b", "a"]) ] [ avg([]) ]`:         "6 1.5 1 b null",
		`echo [ sort([3, 1, 2]) ] [ uniq([1, 1, "1", 2]) ] [ reverse([1, 2]) ] [ reverse("ab") ]`:           `[1,2,3] [1,"1",2] [2,1] ba`,
		`echo [ first([1, 2, 3], 2) ] [ last([1, 2, 3], 5) ] [ range(1, 3) ]`:                               "[1,2] [1,2,3] [1,2,3]",
		`echo [ keys({"b": 1, "a": 2}) ] [ values({"b": 1}) ] [ has({"a": 1}, "a") ] [ get({}, "x", 0) ]`:   `["b","a"] [1] true 0`,
		`echo [ merge({"a": 1, "b": 2}, {"b": 3, "c": 4}) ]`:                                                `{"a":1,"b":3,"c":4}`,
		`let r = (ps); echo [ len($r) ] [ $r[0].user ]`:                                                     "2 ryan",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestExprFunctionErrors(t *testing.T) {
	for src, msg := range map[string]string{
		`echo [ nope(1) ]`:                     "unknown function nope",
		`echo [ upper() ]`:                     "usage upper",
		`echo [ split("a") ]`:                  "usage split",
		`echo [ upper(1) ]`:                    "type text(number)",
		`echo [ sum(["a"]) ]`:                  "type sum(string)",
		`echo [ int("x") ]`:                    "type int(string)",
		`echo [ int(99999999999999999999.0) ]`: "overflow",
		`echo [ match("a", "(") ]`:             "type regex",
		`echo [ match("a", "` + strings.Repeat("a", 1100) + `") ]`: "regex too long",
		`echo [ repeat("ab", 99999999) ]`:                          "repeat",
		`echo [ sort([1, "a"]) ]`:                                  "compare",
		`echo [ replace("a", "", "x") ]`:                           "replace",
		`echo [ len(1) ]`:                                          "len(number)",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v want %q", src, out, msg)
		}
	}
}

// RE2 is linear: a pattern that is exponential for a backtracking engine
// finishes at once on a long input.
func TestRegexIsLinear(t *testing.T) {
	src := `echo [ match("` + strings.Repeat("a", 30000) + `!", "(a+)+$") ]`
	start := time.Now()
	out := Run(src, exprGrant(), &rowsParent{}, Options{})
	if out.Status != 0 || time.Since(start) > 3*time.Second {
		t.Fatalf("%v %+v", time.Since(start), out.Stderr)
	}
}

func TestFunctionsCallNoVerbAndAreBudgeted(t *testing.T) {
	p := &listParent{}
	for name := range exprFuncs {
		Run("echo [ "+name+"(\"x\", \"y\", \"z\") ]", exprGrant(), p, Options{})
		Run("echo [ "+name+"([1], 1) ]", exprGrant(), p, Options{})
	}
	if len(p.calls) != 0 {
		t.Fatalf("a function called the parent: %v", p.calls)
	}
	out := Run(`echo [ split(repeat("a,", 4000000), ",") ]`, exprGrant(), p, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "too large") {
		t.Fatalf("budget: %+v", out.Stderr)
	}
	if out := Run("fn upper() { echo }", exprGrant(), p, Options{}); out.Status != 0 {
		t.Fatalf("a command named like a function is fine: %+v", out)
	}
	if out := Run("builtin sub", exprGrant(), p, Options{}); !strings.Contains(out.Stdout, "sub()  function") {
		t.Fatalf("builtin lists functions: %q", out.Stdout)
	}
}
