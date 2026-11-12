package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/parent"
)

type statParent struct{ calls []parent.Verb }

func (p *statParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls = append(p.calls, v)
	if v.Argv[0] == "stat" {
		return parent.Result{Kind: parent.KindRows, Rows: []parent.Row{{"type": "dir"}}}, nil
	}
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte(v.Path + "\n")}, nil
}

func TestWithRestoresOnEveryWayOut(t *testing.T) {
	for _, body := range []string{
		"echo in",
		"nosuch",
		"for i in [ [1] ] { with set -e { break } }",
		"fn f() { with set -e { return 1 } }\nf",
		"try { with set -e { nosuch } } catch { }",
		"try { with set -e, loopmax 3, x = inner { while [ true ] { let x = 1 } } } catch { }",
	} {
		st := NewState()
		st.vars["x"] = value{kind: valStr, s: "outer"}
		src := strings.Replace(body, "with set -e {", "with set -e -u, verbmax 7, x = inner {", 1)
		if !strings.Contains(src, "with") {
			src = "with set -e -u, verbmax 7, x = inner { " + body + " }"
		}
		Run(src, exprGrant(), &statParent{}, Options{State: st})
		if st.errexit || st.nounset || st.verbMax != verbMaxDefault || st.loopMax != loopMaxDefault || st.vars["x"].s != "outer" {
			t.Errorf("%q left state behind: errexit %v nounset %v verbmax %d loopmax %d x %q", body, st.errexit, st.nounset, st.verbMax, st.loopMax, st.vars["x"].s)
		}
	}
	st := NewState()
	out := Run("with x = 5 { echo [ $x + 1 ] }\necho [ has({}, \"x\") ]", exprGrant(), &statParent{}, Options{State: st})
	if out.Stdout != "6\nfalse\n" {
		t.Fatalf("%+v", out)
	}
	if _, ok := st.vars["x"]; ok {
		t.Fatal("x leaked")
	}
}

func TestWithPrefix(t *testing.T) {
	p := &statParent{}
	out := Run("with prefix /var/log { cat app.log }\ncat x", exprGrant(), p, Options{Prefix: "/var/log/sub"})
	if out.Status != 0 || !strings.HasPrefix(out.Stdout, "/var/log/app.log\n/var/log/sub/x") {
		t.Fatalf("%+v", out)
	}
	p.calls = nil
	out = Run("with prefix /etc { echo x }", exprGrant(), p, Options{})
	if out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("prefix outside roots: %+v %v", out, p.calls)
	}
}

func TestWithRefuses(t *testing.T) {
	st := NewState()
	runIn(st, "declare -r r = 1")
	for src, msg := range map[string]string{
		"with r = 2 { echo }":           "readonly r",
		"with set -f { echo }":          "noglob is per line",
		"with set -ef { echo }":         "noglob is per line",
		"with set -o noglob { echo }":   "noglob is per line",
		"with env LANG=C { echo }":      "env not in this series",
		"with loopmax 0 { echo }":       "usage set loopmax",
		"with set { echo }":             "needs a flag",
		"with set +o pipefail { echo }": "pipefail is always on",
	} {
		out := runIn(st, src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v want %q", src, out, msg)
		}
	}
	if pv := Preview("with x = 1 { unit restart nginx }", exprGrant(), Options{}); !pv.Denied {
		t.Fatalf("preview inside with: %+v", pv)
	}
}
