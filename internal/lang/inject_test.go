package lang

import (
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
