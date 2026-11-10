package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type mutParent struct{ calls []parent.Verb }

func (p *mutParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls = append(p.calls, v)
	if v.Key == "unit.status" {
		return parent.Result{Kind: parent.KindRows, Cols: []string{"unit", "active"}, Rows: []parent.Row{{"unit": "nginx", "active": "false"}}}, nil
	}
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte("done\n")}, nil
}

func fullGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"unit.status", "unit.restart", "package.install", "sysctl.set", "file.read", "file.write"},
		FileRead: []string{"/srv/ops"}, FileWrite: []string{"/srv/ops/out"}}
}

func TestPlanStopsMutationsReadsRun(t *testing.T) {
	st := NewState()
	st.LockPlan()
	p := &mutParent{}
	src := `for r in unit status nginx { if [ $r.active == "false" ] { unit restart nginx } }
package install curl
sysctl set vm.swappiness 10
echo hi > /srv/ops/out/x`
	out := Run(src, fullGrant(), p, Options{State: st})
	if out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	for _, c := range p.calls {
		if mutating[c.Key] {
			t.Errorf("plan let %s through", c.Key)
		}
	}
	if len(p.calls) != 1 || p.calls[0].Key != "unit.status" {
		t.Fatalf("reads should run: %v", p.calls)
	}
	for _, want := range []string{"plan: would unit restart nginx", "plan: would package install curl", "plan: would sysctl set vm.swappiness 10", "plan: would write /srv/ops/out/x (3 bytes)"} {
		if !strings.Contains(out.Stderr, want) {
			t.Errorf("missing %q in %q", want, out.Stderr)
		}
	}
}

func TestPlanLockAndDenials(t *testing.T) {
	st := NewState()
	st.LockPlan()
	out := Run("set +o plan\nunit restart nginx", fullGrant(), &mutParent{}, Options{State: st})
	if out.Status != exitType || !strings.Contains(out.Stderr, "plan is locked") {
		t.Fatalf("lock: %+v", out)
	}
	if out := Run("shopt -u plan", fullGrant(), &mutParent{}, Options{State: st}); out.Status != exitType {
		t.Fatalf("shopt lock: %+v", out)
	}
	// Not locked: set -o plan and set +o plan both work.
	st2 := NewState()
	p := &mutParent{}
	Run("set -o plan\nunit restart a\nset +o plan\nunit restart b", fullGrant(), p, Options{State: st2})
	if len(p.calls) != 1 || p.calls[0].Argv[2] != "b" {
		t.Fatalf("unlocked: %v", p.calls)
	}
	// Plan does not widen the grant: a denied mutation is still denied.
	p = &mutParent{}
	out = Run("unit restart nginx", grant.Grant{V: "sash/1", Entitlements: []string{"ps"}}, p, Options{State: st})
	if out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("denied in plan: %+v", out)
	}
}

func TestPlanShowsTemplateDiff(t *testing.T) {
	st := NewState()
	st.LockPlan()
	p := &tplParent{files: map[string]string{"/srv/ops/v.json": `{"port": 2}`, "/srv/ops/t.tpl": "port {{ $port }}\n", "/srv/ops/out/c": "port 1\n"}}
	out := Run("template_file /srv/ops/v.json /srv/ops/t.tpl /srv/ops/out/c", tplGrant(), p, Options{State: st})
	if out.Status != 0 || p.writes != nil || !strings.Contains(out.Stderr, "- port 1") || !strings.Contains(out.Stderr, "+ port 2") || !strings.Contains(out.Stderr, "plan: would write /srv/ops/out/c") {
		t.Fatalf("%+v writes %v", out, p.writes)
	}
}
