package lang

import (
	"strings"
	"testing"
)

// Text holding a secret's *** rendering prints, but never reaches a
// redirect, a function's input or a verb: the write would replace a file
// with a placeholder (sash review L2). Nothing is written.
func TestRedactedTextNeverWrites(t *testing.T) {
	const setup = "declare -s pw = hunter2\nfn render() { echo \"pass=$pw\" }\nfn upper_in() { echo $input }\n"
	for _, src := range []string{
		"render > /var/tmp/ops/app.conf",
		"echo $pw | egrep a > /var/tmp/ops/app.conf",
		"echo \"x=$pw\" | egrep x > /var/tmp/ops/app.conf",
		"render | egrep pass > /var/tmp/ops/app.conf",
		"let x = (render | egrep pass)\necho \"c $x\" > /var/tmp/ops/app.conf",
		"render | upper_in",
		"fn outer() { render }\nouter > /var/tmp/ops/app.conf",
		"let x = (echo $pw)\nlet y = [ $x ]\necho $y > /var/tmp/ops/app.conf",
		"let x = (echo $pw)\necho [ upper($x) ] > /var/tmp/ops/app.conf",
		"let x = (echo $pw)\nfor l in [ split($x, \"\\n\") ] { echo $l > /var/tmp/ops/app.conf }",
		"let x = (echo $pw)\nlet m = [ {\"a\": $x} ]\necho $m.a > /var/tmp/ops/app.conf",
		"let r = capture { echo \"k=$pw\" | egrep k }\necho $r.out > /var/tmp/ops/app.conf",
		"fn ret() { return \"pass=$pw\" }\nret > /var/tmp/ops/app.conf",
		"fn w() { let x = (echo $pw)\nreturn $x }\nw > /var/tmp/ops/app.conf",
		"let x = (echo $pw)\nunit restart [ $x ]",
		"for r in echo $pw | lines { echo $r.line > /var/tmp/ops/app.conf }",
		"for r in echo \"a=$pw\" | parse \"{k}={v}\" { echo $r.v > /var/tmp/ops/app.conf }",
		"for r in echo $pw | lines { let z = $r.line\necho $z > /var/tmp/ops/app.conf }",
		"for r in echo $pw | lines { echo [ $r.line ] > /var/tmp/ops/app.conf }",
		"for r in echo $pw | lines { let z = [ $r.line ]\necho $z > /var/tmp/ops/app.conf }",
		"fn f() { for r in echo $pw | lines { return $r.line } }\nf > /var/tmp/ops/app.conf",
		"par 2 as r for u in echo $pw | lines { echo $u.line > /var/tmp/ops/app.conf }",
	} {
		f := &fake{}
		out := Run(setup+src, allow(), f, Options{})
		// Refused before any write: a secret rule, or a field of a secret
		// map (type value).
		if out.Status != exitType || !strings.Contains(out.Stderr, "secret") && !strings.Contains(out.Stderr, "type value") {
			t.Errorf("%q: %+v", src, out)
		}
		for _, c := range f.calls {
			if c.Key == "file.write" || c.Key == "unit.restart" {
				t.Errorf("%q: wrote %q", src, c.Body)
			}
		}
	}
	for _, c := range []struct{ src, out string }{
		{"render", "pass=***\n"},
		{"render | egrep pass", "pass=***\n"},
		{"echo $pw", "***\n"},
	} {
		out := Run(setup+c.src, allow(), &fake{}, Options{})
		if out.Status != 0 || out.Stdout != c.out {
			t.Errorf("%q: %+v", c.src, out)
		}
	}
	// A capture of redacted text keeps its statuses readable.
	if out := Run(setup+"let r = capture { render | egrep pass }\necho $r.status $r.out", allow(), &fake{}, Options{}); out.Status != 0 || out.Stdout != "0 ***\n" {
		t.Errorf("capture status: %+v", out)
	}
	// Without a secret the same forms write.
	f := &fake{}
	out := Run("fn render() { echo passx }\nrender > /var/tmp/ops/app.conf\nlet y = (render | egrep pass)\necho $y > /var/tmp/ops/b.conf", allow(), f, Options{})
	if out.Status != 0 || len(keys(f)) != 2 {
		t.Errorf("plain: %+v %v", out, keys(f))
	}
}

// An args declaration reads nothing: a glob or brace in a literal is a
// parse error, never a host listing (sash review L1).
func TestArgsLiteralsTakeNoGlob(t *testing.T) {
	for _, src := range []string{
		"args { d string default /etc/* }\necho $d",
		"args { d string default /etc/{a,b} }\necho $d",
	} {
		f := &fake{}
		out := Run(src, allow(), f, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "args needs literal values") || len(f.calls) != 0 {
			t.Errorf("%q: %+v %v", src, out, keys(f))
		}
	}
}

// A capture's map with a secret out never leaves whole (re-check of
// 9134eb1), while its statuses stay readable.
func TestCaptureMapWithSecretOutNeverWrites(t *testing.T) {
	const setup = "declare -s pw = hunter2\nfn render() { echo \"pass=$pw\" }\nfn take() { echo $input }\nlet r = capture { render | egrep p }\n"
	for _, src := range []string{
		"echo $r > /var/tmp/ops/a",
		"echo [ $r ] > /var/tmp/ops/a",
		"echo $r | json > /var/tmp/ops/a",
		"echo [ [$r] ] > /var/tmp/ops/a",
		"unit restart [ $r ]",
		"fn g() { return $r }\ng > /var/tmp/ops/a",
	} {
		f := &fake{}
		out := Run(setup+src, allow(), f, Options{})
		if out.Status != exitType {
			t.Errorf("%q: %+v", src, out)
		}
		for _, c := range f.calls {
			if c.Key == "file.write" || c.Key == "unit.restart" {
				t.Errorf("%q: called %s %q", src, c.Key, c.Body)
			}
		}
	}
	f := &fake{}
	if out := Run(setup+"if [ $r.status == 0 ] { echo ok > /var/tmp/ops/a }", allow(), f, Options{}); out.Status != 0 || len(keys(f)) != 1 {
		t.Errorf("status: %+v %v", out, keys(f))
	}
}

// str(), join() and comparisons over a map holding a secret yield a
// secret (re-check of 8a72ae1).
func TestSerializingSecretMapIsSecret(t *testing.T) {
	const setup = "declare -s pw = hunter2\nfn render() { echo \"pass=$pw\" }\nlet r = capture { render | egrep p }\nlet r2 = capture { echo $pw }\n"
	for _, src := range []string{
		"echo [ str($r) ] > /var/tmp/ops/a",
		"let j = [ str($r) ]\necho $j > /var/tmp/ops/a",
		"let j = [ str([$r]) ]\necho $j > /var/tmp/ops/a",
		"unit restart [ str($r) ]",
		"let j = [ str($r2) ]\necho $j > /var/tmp/ops/a",
		"let j = [ join(values($r), \",\") ]\necho $j > /var/tmp/ops/a",
		"if [ $r == $r2 ] { echo x }",
		"let j = [ !$r[\"out\"] ]\necho $j > /var/tmp/ops/a",
		"let j = [ $r[\"out\"] && true ]\necho $j > /var/tmp/ops/a",
		"let j = [ false || $r[\"out\"] ]\necho $j > /var/tmp/ops/a",
	} {
		f := &fake{}
		out := Run(setup+src, allow(), f, Options{})
		if out.Status != exitType {
			t.Errorf("%q: %+v", src, out)
		}
		for _, c := range f.calls {
			if c.Key == "file.write" || c.Key == "unit.restart" {
				t.Errorf("%q: called %s %q", src, c.Key, c.Body)
			}
		}
	}
	if out := Run(setup+"if [ $r.status == 0 ] { echo ok }", allow(), &fake{}, Options{}); out.Status != 0 || out.Stdout != "ok\n" {
		t.Errorf("status: %+v", out)
	}
}
