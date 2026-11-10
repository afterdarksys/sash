package lang

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// A directory whose names are sash source. Glob output must stay data: no
// name may become a statement, an access path, or expression source.
func hostileDir(names ...string) *listParent {
	var rows []parent.Row
	for _, n := range names {
		rows = append(rows, parent.Row{"name": n, "type": "file"})
	}
	return &listParent{rows: map[string][]parent.Row{"/var/log": rows}}
}

func injectGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"ps", "file.read"}, FileRead: []string{"/var/log"}}
}

func psCalls(p *listParent) int {
	n := 0
	for _, c := range p.calls {
		if c.Key == "ps" {
			n++
		}
	}
	return n
}

func TestGlobNamesNeverBecomeSource(t *testing.T) {
	cases := []struct {
		src   string
		names []string
	}{
		{`let m = [ {} ]; echo "$m["*"]"`, []string{`a"]"; ps #`}},
		{`let m = [ {} ]; echo "$m["/var/log/*"]"`, []string{`a"]"; ps #`}},
		{`echo "\" [ "*" ]`, []string{`" + $secret + "`}},
		{`echo "\" [ "*" ] "`, []string{`"]; ps; ["`}},
		{`echo "$m[*]"`, []string{`0]"; ps #`}},
		{`echo [ "*" ] *`, []string{`]; ps; [`}},
		{"let x = [ [1,\n* ] ]", []string{`]; ps #`}},
	}
	for _, c := range cases {
		p := hostileDir(c.names...)
		out := Run(c.src, injectGrant(), p, Options{Prefix: "/var/log", History: []string{"ps"}})
		if n := psCalls(p); n != 0 {
			t.Errorf("%q ran ps %d times: %+v", c.src, n, out)
		}
		if strings.Contains(out.Stderr, "unset secret") {
			t.Errorf("%q evaluated a file name: %+v", c.src, out)
		}
	}
}

// The parser and the expanders agree on where a double-quoted string ends.
func TestDoubleQuoteEscapesAgree(t *testing.T) {
	out := Run(`echo "a\"b" "c\\d" "e\$f" "g\h"`, injectGrant(), &listParent{}, Options{})
	if out.Status != 0 || out.Stdout != "a\"b c\\d e$f g\\h\n" {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
}

func TestExpressionsAreOneLine(t *testing.T) {
	src := "let x = [ [1,\n!! ] ]"
	got, _ := Expand(src, injectGrant(), &listParent{}, Options{History: []string{"ps"}})
	if strings.Contains(got, "[ [1,\nps") {
		t.Fatalf("history ran inside a multi-line expression: %q", got)
	}
	if out := Run("let x = [ 1 +\n2 ]", injectGrant(), &listParent{}, Options{}); out.Status == 0 {
		t.Fatalf("multi-line expression accepted: %+v", out)
	}
}

func TestUnclosedBracketsAreLinear(t *testing.T) {
	src := "echo " + strings.Repeat("[ ", 200000)
	done := make(chan Outcome, 1)
	go func() { done <- Run(src, injectGrant(), &listParent{}, Options{}) }()
	select {
	case <-done:
	case <-timeAfter(5):
		t.Fatal("unclosed [ took more than 5s")
	}
}

func TestExpressionMemoryIsBudgeted(t *testing.T) {
	for _, src := range []string{
		"echo [ [" + strings.Repeat("0..300000, ", 40) + "1] ]",
		"echo [ [" + strings.Repeat(`"x" * 4000000, `, 40) + "1] ]",
		"let l = [ [1] ]; for i in [ 1..40 ] { let l = [ $l + $l ] }",
		"echo [ 1..400000 ]",
	} {
		out := Run(src, injectGrant(), &listParent{}, Options{})
		if out.Status != exitType || !(strings.Contains(out.Stderr, "too large") || strings.Contains(out.Stderr, "range")) {
			t.Errorf("%.60q: status %d err %q", src, out.Status, out.Stderr)
		}
	}
}

func TestNumericStringsCoerceOnlyAgainstNumbers(t *testing.T) {
	for src, want := range map[string]string{
		`echo [ "1" + "2" ] [ "3" * 3 ] [ "01" == "1" ] [ "0" == 0 ] [ "00" == "0" ]`: "3 9 false true false",
		`echo [ "ab" + "1" ] [ "ab" * 2 ] [ "-0" == "0" ] [ "-0" == 0 ]`:              "ab1 abab false true",
		`echo [ "10" < "9" ] [ "10" < 9 ] [ "1e3" == 1000 ]`:                          "true false true",
	} {
		out := Run(src, injectGrant(), &listParent{}, Options{})
		if out.Status != 0 || strings.TrimSpace(out.Stdout) != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func timeAfter(sec int) <-chan time.Time { return time.After(time.Duration(sec) * time.Second) }

// A quoted string across a newline is skipped whole, so an expression later
// on the next line is still found.
func TestExpressionAfterMultilineString(t *testing.T) {
	out := Run("echo \"a b\nc d\" | lines |> each [ split($it.line, \" \")[1] ]", injectGrant(), &listParent{}, Options{})
	if out.Status != 0 || out.Stdout != "b\nd\n" {
		t.Fatalf("%+v", out)
	}
	if out := Run("echo \"x\n[ 1 +\" ]", injectGrant(), &listParent{}, Options{}); strings.Contains(out.Stdout, "x") && out.Status == 0 && !strings.Contains(out.Stdout, "[ 1 +") {
		t.Fatalf("a [ inside a string became an expression: %+v", out)
	}
}

// Review: # is a comment only at a word boundary in the parser and the
// expanders alike, so a mid-word # cannot change quote parity.
func TestCommentRuleAgrees(t *testing.T) {
	p := hostileDir(";ps;")
	out := Run("echo x# it's\necho '*'", injectGrant(), p, Options{Prefix: "/var/log"})
	if n := psCalls(p); n != 0 {
		t.Fatalf("ran ps %d times: %+v", n, out)
	}
	for src, want := range map[string]string{
		"echo x#y":      "x#y",
		"echo a # note": "a",
		"echo a;# note": "a",
		"echo \"a\"#b":  "a #b",
	} {
		got := Run(src, injectGrant(), &listParent{}, Options{})
		if got.Status != 0 || strings.TrimSuffix(got.Stdout, "\n") != want {
			t.Errorf("%q: %+v want %q", src, got, want)
		}
	}
	if q, ok := quoteArg("/var/log/a#b"); !ok || q != "'/var/log/a#b'" {
		t.Fatalf("# names are quoted: %q", q)
	}
}

func TestAmplificationsAreCharged(t *testing.T) {
	st := NewState()
	cols := make([]string, 2000)
	for i := range cols {
		cols[i] = "c" + strconv.Itoa(i)
	}
	st.vars["wide"] = value{kind: valStr, s: strings.Join(cols, ",") + strings.Repeat("\n1", 20000) + "\n"}
	for src, msg := range map[string]string{
		`echo [ sub(repeat("x", 1000000), ".+", "` + strings.Repeat("$0", 100) + `") ]`: "too large",
		`echo [ sub(repeat("x", 1000000), "x*?", "yyyyyyyyyy") ]`:                       "too large",
		`echo [ join([` + strings.Repeat(`repeat("x", 100000), `, 200) + `"x"], "") ]`:  "too large",
		`echo [ split(repeat("x", 4000000), "") ]`:                                      "too large",
		`echo [ str([` + strings.Repeat(`repeat("x", 100000), `, 200) + `1]) ]`:         "too large",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%.60s: %+v", src, out.Stderr)
		}
	}
	if out := Run(`echo $wide | from csv`, exprGrant(), &rowsParent{}, Options{State: st}); out.Status != exitType || !strings.Contains(out.Stderr, "csv too large") {
		t.Errorf("wide header: %+v", out.Stderr)
	}
	st.vars["j"] = value{kind: valStr, s: "[" + strings.Repeat(`{"a":1},`, 299) + `{"a":1}]`}
	out := Run(`echo $j | from json |> each [ {"p": repeat("x", 4000000)} ]`, exprGrant(), &rowsParent{}, Options{State: st})
	if out.Status != exitType || !strings.Contains(out.Stderr, "too large") {
		t.Errorf("each total: %+v", out.Stderr)
	}
	st.vars["r"] = value{kind: valStr, s: "[" + strings.Repeat(`{"a":"`+strings.Repeat("x", 100000)+`"},`, 49) + `{"a":"x"}]`}
	out = Run(`echo $r | from json |> sub a 'x' with '$0$0$0$0'`, exprGrant(), &rowsParent{}, Options{State: st})
	if out.Status != exitType || !strings.Contains(out.Stderr, "too large") {
		t.Errorf("row sub total: %+v", out.Stderr)
	}
}

func TestCSVSafeLookalikes(t *testing.T) {
	for in, want := range map[string]string{
		" =1+1": "' =1+1",
		" =1":   "' =1",
		"＝1+1":  "'＝1+1",
		"-5":    "-5",
		" 7":    " 7",
		"plain": "plain",
	} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q want %q", in, got, want)
		}
	}
}

// Review: expansion that replaces the boundary before # must not turn the
// comment into code.
func TestCommentSurvivesExpansion(t *testing.T) {
	for _, c := range []struct {
		src   string
		names []string
	}{
		{"echo *=# note; ps", []string{"ab="}},
		{"echo {a,b}# note; ps", nil},
		{"echo *)# x; ps", []string{"q)"}},
	} {
		p := hostileDir(c.names...)
		out := Run(c.src, injectGrant(), p, Options{Prefix: "/var/log"})
		if n := psCalls(p); n != 0 {
			t.Errorf("%q ran ps: %+v", c.src, out)
		}
	}
	if out := Run("let f = x; cat /var/log/$f*", injectGrant(), hostileDir("$f"), Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "type glob $") {
		t.Fatalf("glob with $: %+v", out)
	}
}
