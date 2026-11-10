package lang

import (
	"strings"
	"testing"
)

func TestSecretsNeverPrint(t *testing.T) {
	p := &tplParent{files: map[string]string{"/srv/ops/tok": "s3cr3t-value\n", "/srv/ops/t.tpl": "{{ $tok }}", "/srv/ops/v.json": `{"a": 1}`}}
	st := NewState()
	st.vars["unused"] = value{kind: valStr, s: "x"}
	out := Run(`declare -s tok = (cat /srv/ops/tok)
echo $tok [ $tok ] [ "Bearer " + $tok ] [ len($tok) > 5 ] [ [$tok] ]
let copy = $tok
echo $copy
declare -p tok
log info "auth" [ {"t": $tok} ]
check "token set" [ len($tok) > 0 ]
if [ $tok != "" ] { echo branch }`, tplGrant(), p, Options{State: st})
	all := out.Stdout + out.Stderr
	if strings.Contains(all, "s3cr3t") {
		t.Fatalf("leaked: %q", all)
	}
	if !strings.Contains(out.Stderr, "info auth t=***") {
		t.Fatalf("log: %q", out.Stderr)
	}
	if out.Status != 0 || !strings.Contains(out.Stdout, "*** *** *** *** ***\n***\n") || !strings.Contains(out.Stdout, "ok token set") || !strings.Contains(out.Stdout, "branch") {
		t.Fatalf("%+v", out)
	}
}

func TestSecretsCannotLeave(t *testing.T) {
	p := &tplParent{files: map[string]string{"/srv/ops/t.tpl": "{{ $tok }}", "/srv/ops/v.json": `{"a": 1}`}}
	for src, msg := range map[string]string{
		`cat /srv/ops/$tok`:                                "a secret cannot leave sash",
		`echo $tok > /srv/ops/out/x`:                       "a secret cannot leave sash",
		`echo [ $tok ] > /srv/ops/out/x`:                   "a secret cannot leave sash",
		`fn f(a) { echo $a }` + "\n" + `f $tok`:            "a secret cannot leave sash",
		`echo [ {"tok": $tok} ] | template /srv/ops/t.tpl`: "",
		`let x = "pre-$tok"`:                               "a secret in a word",
	} {
		st := NewState()
		Run(`declare -s tok = abc`, tplGrant(), p, Options{State: st})
		p.writes = nil
		out := Run(src, tplGrant(), p, Options{State: st})
		if out.Status == 0 || !strings.Contains(out.Stderr, msg) || p.writes != nil || strings.Contains(out.Stdout+out.Stderr, "abc") {
			t.Errorf("%s: %+v writes %v", src, out, p.writes)
		}
	}
	// The template refuses a secret var by name.
	st := NewState()
	Run(`declare -s tok = abc`, tplGrant(), p, Options{State: st})
	if _, err := renderTemplate("{{ $tok }}", mustMap(t, st), 10); err == nil || !strings.Contains(err.Error(), "cannot leave") {
		t.Fatalf("template: %v", err)
	}
	if out := Run("declare -s z", tplGrant(), p, Options{State: NewState()}); out.Status != exitType {
		t.Fatalf("needs a value: %+v", out)
	}
}

func mustMap(t *testing.T, st *State) any {
	t.Helper()
	o := newOmap()
	o.set("tok", toDatum(st.vars["tok"]))
	return o
}
