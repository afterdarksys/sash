package lang

import (
	"strings"
	"testing"
)

// A command name never comes from a value: a let, a parameter, a loop
// name, a declare, or a [ ] cannot pick the verb. The stage is refused
// before classify and before any parent call, on every path that runs one.
func TestCommandFromValueIsRefused(t *testing.T) {
	for _, src := range []string{
		"let c = \"unit\"\n$c restart nginx",
		"let c = unit\n$c status nginx",
		"fn f(p) { $p restart nginx }\nf unit",
		"for c in [ [\"unit\"] ] { $c restart nginx }",
		"declare -r c = unit\n$c restart nginx",
		"let c = unit\nlet x = ($c restart nginx)",
		"let c = unit\ndeclare -r x = ($c restart nginx)",
		"let c = unit\nretry 2 { $c restart nginx }",
		"let c = unit\nif $c restart nginx { echo y }",
		"let c = unit\nwith x = 1 { $c restart nginx }",
		"let c = cat\necho x | $c",
		"let c = un\n${c}it restart nginx",
		"[ \"unit\" ] restart nginx",
		"let c = unit\n\"$c\" restart nginx",
		"let c = unit\ntimeout 5s { $c restart nginx }",
	} {
		p := &listParent{}
		out := Run(src, allow(), p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "command_from_value") || len(p.calls) != 0 {
			t.Errorf("%q: status %d stderr %q calls %d", src, out.Status, out.Stderr, len(p.calls))
		}
	}
	// try may catch the refusal; still no call.
	p := &listParent{}
	out := Run("let c = unit\ntry { $c restart nginx } catch e { echo $e.message }", allow(), p, Options{})
	if len(p.calls) != 0 || !strings.Contains(out.Stdout+out.Stderr, "command_from_value") {
		t.Fatalf("try: %+v calls %d", out, len(p.calls))
	}
	// Plan mode refuses it too, before the plan prints a would.
	st := NewState()
	st.LockPlan()
	p = &listParent{}
	out = Run("let c = unit\n$c restart nginx", allow(), p, Options{State: st})
	if out.Status != exitType || strings.Contains(out.Stderr, "would") || len(p.calls) != 0 {
		t.Fatalf("plan: %+v", out)
	}
	// A literal name still runs.
	p = &listParent{}
	if out := Run("unit restart nginx", allow(), p, Options{}); len(p.calls) != 1 {
		t.Fatalf("literal: %+v", out)
	}
}

func TestCommandFromValueInPreviewAndWhy(t *testing.T) {
	st := NewState()
	Run("let c = unit", allow(), &listParent{}, Options{State: st})
	pv := Preview("$c restart nginx", allow(), Options{State: st})
	if pv.Smile != "command_from_value" {
		t.Fatalf("preview %+v", pv)
	}
	out := Why("$c restart nginx", allow(), Options{State: st})
	if !strings.Contains(out.Stdout, "command_from_value") || !strings.Contains(out.Stdout, "a command name from a value does not run") {
		t.Fatalf("why %q", out.Stdout)
	}
}
