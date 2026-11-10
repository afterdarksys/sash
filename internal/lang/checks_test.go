package lang

import (
	"regexp"
	"strings"
	"testing"
)

func TestChecks(t *testing.T) {
	st := NewState()
	out := Run(`check "two is two" [ 1 + 1 == 2 ]
check "no root" [ "root" in ["ryan"] ]
echo after`, exprGrant(), &rowsParent{}, Options{State: st})
	if out.Stdout != "ok two is two\nFAIL no root\nafter\n" || out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	out = Run("checks |> to json", exprGrant(), &rowsParent{}, Options{State: st})
	if !strings.Contains(out.Stdout, `"result": "fail"`) || strings.Count(out.Stdout, "check") != 2 {
		t.Fatalf("checks: %+v", out)
	}
	if out := Run("set -e\ncheck \"x\" [ false ]\necho no", exprGrant(), &rowsParent{}, Options{}); strings.Contains(out.Stdout, "no") {
		t.Fatalf("errexit stops at a failed check: %+v", out)
	}
	for _, bad := range []string{`check "x"`, `check [ true ]`, `check "` + strings.Repeat("n", 300) + `" [ true ]`} {
		if out := Run(bad, exprGrant(), &rowsParent{}, Options{}); out.Status != exitType {
			t.Errorf("%.40s: %+v", bad, out)
		}
	}
}

func TestLog(t *testing.T) {
	now, _ := fakeClock(t)
	_ = now
	out := Run(`log info "deploy start" [ {"host": "web1", "n": 2} ]
log warn "two
lines"`, exprGrant(), &rowsParent{}, Options{})
	want := "2026-11-10T00:00:00Z info deploy start host=web1 n=2\n2026-11-10T00:00:00Z warn two lines\n"
	if out.Status != 0 || out.Stderr != want || out.Stdout != "" {
		t.Fatalf("%+v", out)
	}
	for _, bad := range []string{`log loud "x"`, `log info`, `log info "x" [ [1] ]`} {
		if out := Run(bad, exprGrant(), &rowsParent{}, Options{}); out.Status != exitType {
			t.Errorf("%s: %+v", bad, out)
		}
	}
}

func TestEncodingFunctions(t *testing.T) {
	out := Run(`echo [ sha256("abc") ] [ base64("hi") ] [ unbase64("aGk=") ] [ hex("hi") ]`, exprGrant(), &rowsParent{}, Options{})
	if out.Stdout != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad aGk= hi 6869\n" {
		t.Fatalf("%+v", out)
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\n$`)
	a := Run(`echo [ uuid() ]`, exprGrant(), &rowsParent{}, Options{}).Stdout
	b := Run(`echo [ uuid() ]`, exprGrant(), &rowsParent{}, Options{}).Stdout
	if !re.MatchString(a) || a == b {
		t.Fatalf("uuid %q %q", a, b)
	}
	if out := Run(`echo [ unbase64("!!") ]`, exprGrant(), &rowsParent{}, Options{}); out.Status != exitType {
		t.Fatalf("bad base64: %+v", out)
	}
}
