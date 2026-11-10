package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

// Every reserved name is listed, and every listed command or verb is one
// the evaluator dispatches.
func TestBuiltinTableCoversTheLanguage(t *testing.T) {
	first := map[string]bool{}
	for _, b := range Builtins() {
		first[strings.Fields(b.Form)[0]] = true
		switch b.Kind {
		case "verb", "filter", "builtin":
			if _, prob := classify(strings.Fields(b.Form), map[string]*Fn{}); prob != nil && prob.status == exitUnknown {
				t.Errorf("%q is listed but unknown to the evaluator", b.Form)
			}
		case "keyword", "session":
		default:
			t.Errorf("%q has kind %q", b.Form, b.Kind)
		}
		if (b.Kind == "verb") != (b.Key != "" && b.Kind == "verb") {
			t.Errorf("%q verb without a key", b.Form)
		}
	}
	for name := range reservedNames {
		if !first[name] {
			t.Errorf("reserved %q is missing from the builtin table", name)
		}
	}
	for _, b := range Builtins() {
		// cat with no path is a filter on its input; with one it reads.
		argv := append(strings.Fields(b.Form), "/var/log/x")
		if b.Kind == "verb" && knownKey(argv) != b.Key {
			t.Errorf("%q key %q, preview says %q", b.Form, b.Key, knownKey(argv))
		}
	}
}

func TestBuiltinCommand(t *testing.T) {
	out := Run("builtin unit", exprGrant(), &listParent{}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "unit status") || !strings.Contains(out.Stdout, "unit.restart") || strings.Contains(out.Stdout, "ps") {
		t.Fatalf("status %d out %q", out.Status, out.Stdout)
	}
	out = Run("builtin |> where kind == verb |> select name", exprGrant(), &listParent{}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "package install") || strings.Contains(out.Stdout, "while") {
		t.Fatalf("status %d out %q", out.Status, out.Stdout)
	}
	if out := Run("builtin nosuch", exprGrant(), &listParent{}, Options{}); out.Status != 1 || out.Stdout != "" {
		t.Fatalf("missing: %+v", out)
	}
	if out := Run("fn builtin() { echo x }", exprGrant(), &listParent{}, Options{}); out.Status != exitType {
		t.Fatalf("shadow: %+v", out)
	}
	p := &listParent{}
	Run("builtin", grantNone(), p, Options{})
	if len(p.calls) != 0 {
		t.Fatalf("builtin called the parent")
	}
}

func grantNone() grant.Grant { return grant.Grant{} }
