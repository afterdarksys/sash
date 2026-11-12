package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

func TestContains(t *testing.T) {
	for src, want := range map[string]string{
		`echo [ contains("hello", "ell") ]`:                   "true",
		`echo [ contains("hello", "xyz") ]`:                   "false",
		`echo [ contains("", "") ]`:                           "true",
		`echo [ contains([1, 2, 3], 2) ]`:                     "true",
		`echo [ contains([1, 2, 3], 2.0) ]`:                   "true",
		`echo [ contains([1, 2, 3], "2") ]`:                   "true",
		`echo [ contains([1, 2, 3], "x") ]`:                   "false",
		`echo [ contains([[1, 2], {"a": [3]}], {"a": [3]}) ]`: "true",
		`echo [ contains([[1, 2], {"a": [3]}], [1, 2]) ]`:     "true",
		`echo [ contains([[1, 2]], [2, 1]) ]`:                 "false",
		`echo [ contains([], null) ]`:                         "false",
		`echo [ contains([null], null) ]`:                     "true",
	} {
		out := Run(src, grant.Grant{}, &countParent{}, Options{})
		if out.Status != 0 || out.Stdout != want+"\n" {
			t.Errorf("%s: %+v want %s", src, out, want)
		}
	}
	for _, src := range []string{
		`echo [ contains({"a": 1}, "a") ]`,
		`echo [ contains("abc", 1) ]`,
		`echo [ contains(1, 1) ]`,
		`echo [ contains("a") ]`,
	} {
		if out := Run(src, grant.Grant{}, &countParent{}, Options{}); out.Status != exitType {
			t.Errorf("%s: %+v", src, out)
		}
	}
}

// secretCalls is a call with a secret argument for every expression
// function. A function missing here fails the test, so a new one cannot
// skip the taint check. uuid takes no argument and so reads no secret.
var secretCalls = map[string]string{
	"len": "len($s)", "upper": "upper($s)", "lower": "lower($s)", "trim": "trim($s)",
	"split": `split($s, "b")`, "join": `join($l, ",")`, "replace": `replace($s, "a", "z")`,
	"starts": `starts($s, "a")`, "ends": `ends($s, "c")`, "pad": "pad($s, 9)", "repeat": "repeat($s, 2)",
	"match": `match($s, "a.")`, "find": `find($s, "b")`, "groups": `groups($s, "(a)(b)")`,
	"sub": `sub($s, "a", "z")`, "int": "int($n)", "float": "float($n)", "str": "str($n)",
	"bool": "bool($s)", "type": "type($s)", "sum": "sum($l)", "avg": "avg($l)", "min": "min($l)",
	"max": "max($l)", "sort": "sort($l)", "uniq": "uniq($l)", "reverse": "reverse($l)",
	"first": "first($l, 1)", "last": "last($l, 1)", "range": "range($n, 5)", "keys": "keys($m)",
	"values": "values($m)", "has": `has($m, "a")`, "get": `get($m, "a", 0)`, "merge": `merge($m, {"b": 2})`,
	"sha256": "sha256($s)", "base64": "base64($s)", "hex": "hex($s)", "unbase64": "unbase64($b)",
	"contains": `contains($s, "b")`,
}

// Every expression function returns a secret for a secret argument, and
// used as an if condition the line exits 2: no function is an oracle.
func TestEveryFunctionTaints(t *testing.T) {
	const setup = "declare -s s = abc\ndeclare -s n = [ 3 ]\ndeclare -s l = [ [1, 2, 3] ]\ndeclare -s m = [ {\"a\": 1} ]\ndeclare -s b = YWJj\n"
	for name := range exprFuncs {
		if name == "uuid" {
			continue
		}
		call, ok := secretCalls[name]
		if !ok {
			t.Errorf("%s has no secret-taint case", name)
			continue
		}
		for _, arg := range []string{"$s", "$n", "$l", "$m", "$b"} {
			if strings.Contains(call, arg) {
				// The plain call works, so a failure below is the taint.
				plain := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(call, "$s", `"abc"`), "$n", "3"), "$l", "[1, 2, 3]"), "$m", `{"a": 1}`), "$b", `"YWJj"`)
				if out := Run("let r = [ "+plain+" ]", grant.Grant{}, &countParent{}, Options{}); out.Status != 0 {
					t.Errorf("%s plain: %+v", plain, out)
				}
			}
		}
		st := NewState()
		out := Run(setup+"let r = [ "+call+" ]\necho [ $r ]", grant.Grant{}, &countParent{}, Options{State: st})
		if out.Status != 0 || out.Stdout != redacted+"\n" || st.SecretCount() != 6 {
			t.Errorf("%s: result not secret: %+v (secrets %d)", call, out, st.SecretCount())
		}
		out = Run(setup+"if [ "+call+" ] { echo yes } else { echo no }", grant.Grant{}, &countParent{}, Options{})
		if out.Status != exitType || out.Stdout != "" {
			t.Errorf("%s as a condition: %+v", call, out)
		}
	}
	if exprFuncs["uuid"].max != 0 {
		t.Fatal("uuid takes an argument now: add its taint case")
	}
}
