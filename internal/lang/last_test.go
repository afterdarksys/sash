package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

func TestLastHoldsPerStageStatuses(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"echo hi | egrep zz | head -n 1\necho $last.status $last.stages $last.keys $last.failed_stage", "1 [0,1,0] [\"echo\",\"egrep\",\"head\"] 1\n"},
		{"echo hi\necho $last.status $last.failed_stage $last.code", "hi\n0 null \n"},
		{"echo \"{bad\" | from json\necho never", ""},
	} {
		out := Run(tc.src, allow(), &fake{}, Options{})
		if tc.want != "" && (out.Status != 0 || out.Stdout != tc.want) {
			t.Errorf("%q: %+v want %q", tc.src, out, tc.want)
		}
	}
	if out := Run("let last = 1", allow(), &fake{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "readonly last") {
		t.Fatalf("rebind: %+v", out)
	}
}

func TestCaptureHoldsOrdinaryFailures(t *testing.T) {
	out := Run("let r = capture { echo x | from json }\necho $r.status $r.code $r.err $r.stages $r.failed_stage\necho still", allow(), &fake{}, Options{})
	if out.Status != 0 || out.Stdout != "2 runtime_error json syntax [0,2] 1\nstill\n" || strings.Contains(out.Stderr, "json syntax") {
		t.Fatalf("%+v", out)
	}
	ok := Run("let r = capture { echo ok }\necho $r.out$r.status", allow(), &fake{}, Options{})
	if ok.Status != 0 || ok.Stdout != "ok\n0\n" {
		t.Fatalf("ok: %+v", ok)
	}
}

// A refusal is never captured: it fails exactly as it would without capture.
func TestCaptureDoesNotHideRefusals(t *testing.T) {
	for _, tc := range []struct {
		src    string
		g      grant.Grant
		status int
		stderr string
	}{
		{"let r = capture { unit status nginx }\necho after", grant.Grant{}, exitDenied, "denied unit.status"},
		{"let r = capture { nosuchverb }\necho after", allow(), exitUnknown, "unknown"},
		{"set verbmax 1\nps\nlet r = capture { ps }\necho after", allow(), exitType, "verb max"},
	} {
		out := Run(tc.src, tc.g, &fake{}, Options{})
		if out.Status != tc.status || strings.Contains(out.Stdout, "after") || !strings.Contains(out.Stderr, tc.stderr) {
			t.Errorf("%q: %+v", tc.src, out)
		}
	}
}

// A secret in a captured pipeline stays redacted in the map.
func TestCaptureKeepsSecretsRedacted(t *testing.T) {
	out := Run("declare -s tok = hidden\nlet r = capture { echo [ $tok ] }\necho $r.out", allow(), &fake{}, Options{})
	if strings.Contains(out.Stdout, "hidden") || strings.Contains(out.Stderr, "hidden") {
		t.Fatalf("leak: %+v", out)
	}
}
