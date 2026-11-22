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
	} {
		f := &fake{}
		out := Run(setup+src, allow(), f, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "a secret cannot leave sash") {
			t.Errorf("%q: %+v", src, out)
		}
		for _, c := range f.calls {
			if c.Key == "file.write" {
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
