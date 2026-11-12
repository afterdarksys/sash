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
echo end`, tplGrant(), p, Options{State: st})
	all := out.Stdout + out.Stderr
	if strings.Contains(all, "s3cr3t") {
		t.Fatalf("leaked: %q", all)
	}
	if !strings.Contains(out.Stderr, "info auth t=***") {
		t.Fatalf("log: %q", out.Stderr)
	}
	if out.Status != 0 || !strings.Contains(out.Stdout, "*** *** *** *** ***\n***\n") || !strings.Contains(out.Stdout, "end") {
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

// Review: a secret decides nothing, and an error computed from one ends
// the line where try cannot catch it.
func TestSecretsDecideNothing(t *testing.T) {
	extract := `declare -s tok = s3cr
let k = ""
for i in [ range(0, 3) ] { for c in [ split("abcdefghijklmnopqrstuvwxyz0123456789", "") ] {
  if [ starts($tok, $k + $c) ] { let k = [ $k + $c ]; break } } }
echo $k`
	for _, src := range []string{
		extract,
		`declare -s tok = s3cr` + "\n" + `while [ starts($tok, "s") ] { echo y; break }`,
		`declare -s tok = s3cr` + "\n" + `check "x" [ len($tok) > 2 ]`,
		`declare -s tok = s3cr` + "\n" + `echo '[{"a":1}]' | from json |> filter [ starts($tok, "s") ]`,
		`declare -s tok = s3cr` + "\n" + `try { echo [ 1 / (len($tok) - 4) ] } catch e { echo len4 }`,
		`declare -s tok = s3cr` + "\n" + `try { echo [ int($tok) ] } catch e { echo notnum }`,
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status == 0 || strings.Contains(out.Stdout, "s3") || strings.Contains(out.Stdout, "len4") || strings.Contains(out.Stdout, "notnum") || strings.Contains(out.Stdout, "y") {
			t.Errorf("%.50q: %+v", src, out)
		}
	}
	if _, err := renderTemplate("{{ if starts($t, \"s\") }}x{{ end }}", mapOf("t", secretVal{"s3"}), 10); err == nil || !strings.Contains(err.Error(), "cannot decide") {
		t.Fatalf("template if: %v", err)
	}
}

func mapOf(k string, v any) any {
	o := newOmap()
	o.set(k, v)
	return o
}

// History never copies a declare -s line: !! and !$ refuse it, in a run
// and in needs, and the refusal does not echo the secret.
func TestHistoryRefusesASecretLine(t *testing.T) {
	for _, decl := range []string{
		`declare -s tok = "hunter2"`,
		`declare -rs tok = hunter2`,
		`declare -r -s tok = hunter2`,
		`declare "-s" tok = hunter2`,
		`de"cl"are -s tok = hunter2`,
		`echo x; declare -s tok = hunter2`,
		`  declare  -s  tok = hunter2`,
	} {
		for _, use := range []string{"unit status !$", "!!", `echo "!$"`, "echo !!"} {
			src := decl + "\n" + use
			p := &listParent{}
			out := Run(src, allow(), p, Options{})
			if out.Status != exitType || len(p.calls) != 0 || strings.Contains(out.Stdout+out.Stderr, "hunter2") {
				t.Errorf("run %q: %+v calls %d", src, out, len(p.calls))
			}
			n := Needs(src, NeedsOptions{})
			if n.Status != exitType || n.Stdout != "" || strings.Contains(n.Stderr, "hunter2") {
				t.Errorf("needs %q: %+v", src, n)
			}
		}
	}
	// The session history the editor passes is checked the same way.
	out := Run("unit status !$", allow(), &listParent{}, Options{History: []string{"declare -s tok = hunter2"}})
	if out.Status != exitType || strings.Contains(out.Stderr, "hunter2") {
		t.Fatalf("session history: %+v", out)
	}
	// A line that only reads a secret by name is history as before.
	out = Run("declare -s tok = hunter2\necho $tok\n!!", allow(), &listParent{}, Options{})
	if out.Status != 0 || strings.Contains(out.Stdout, "hunter2") {
		t.Fatalf("by name: %+v", out)
	}
	if DeclaresSecret("declare -r x = 1") || DeclaresSecret("echo -s declare") {
		t.Fatalf("false match")
	}
}
