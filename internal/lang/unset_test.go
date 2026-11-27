package lang

import "testing"

// An unbound name is always exit 2 and the message names it, in word
// expansion as in a [ ] expression; set -u adds missing map keys.
func TestUnboundNameIsNamed(t *testing.T) {
	for _, pre := range []string{"", "set -u\n"} {
		for _, src := range []string{
			"echo $nope",
			"echo a-$nope-b",
			"unit status $nope",
			"echo $nope.x",
			"echo [ $nope ]",
		} {
			f := &fake{}
			out := Run(pre+src, allow(), f, Options{})
			if out.Status != exitType || out.Stderr != "unset nope\n" || len(f.calls) != 0 {
				t.Errorf("%q: status %d err %q calls %v", pre+src, out.Status, out.Stderr, f.calls)
			}
		}
	}
}

func TestNounsetMissingKeyInWord(t *testing.T) {
	f := &fake{}
	out := Run("set -u; let m = [ {\"a\": 1} ]; echo $m.b", allow(), f, Options{})
	if out.Status != exitType || out.Stderr != "unset key b\n" {
		t.Fatalf("status %d err %q", out.Status, out.Stderr)
	}
	out = Run("let m = [ {\"a\": 1} ]; echo $m.b", allow(), f, Options{})
	if out.Status != 0 || out.Stdout != "null\n" {
		t.Fatalf("no -u: status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
}

// A whole row as a word keeps its message: it is bound, only the wrong
// shape.
func TestRowAsWordStillTypeValue(t *testing.T) {
	out := Run(`for r in ps { echo $r }`, exprGrant(), &rowsParent{}, Options{})
	if out.Status != exitType || out.Stderr != "type value\n" {
		t.Fatalf("status %d err %q", out.Status, out.Stderr)
	}
}
