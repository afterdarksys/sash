package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/parent"
)

type fileParent struct {
	files map[string]string
	calls []parent.Verb
}

func (p *fileParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls = append(p.calls, v)
	if v.Key == "file.read" && len(v.Argv) > 0 && v.Argv[0] == "cat" {
		if body, ok := p.files[v.Path]; ok {
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte(body)}, nil
		}
		return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
	}
	return parent.Result{Kind: parent.KindRows, Cols: []string{"pid"}, Rows: []parent.Row{{"pid": "1"}}}, nil
}

func TestUseLoadsDefinitionsOnly(t *testing.T) {
	p := &fileParent{files: map[string]string{
		"/var/log/lib.sash":  "fn twice(x) { return [ $x * 2 ] }\ndeclare -r limit = [ 10 ]\n",
		"/var/log/bad.sash":  "fn ok() { echo }\nps\n",
		"/var/log/pipe.sash": "declare -r p = (ps)\n",
		"/var/log/glob.sash": "fn g() { echo * }\n",
	}}
	st := NewState()
	out := Run("use /var/log/lib.sash\ntwice $limit", exprGrant(), p, Options{State: st})
	if out.Status != 0 || out.Stdout != "20\n" {
		t.Fatalf("%+v", out)
	}
	if out := Run("let limit = 1", exprGrant(), p, Options{State: st}); out.Status != exitType {
		t.Fatalf("readonly from use: %+v", out)
	}
	for _, f := range []string{"bad", "pipe"} {
		p.calls = nil
		st := NewState()
		out := Run("use /var/log/"+f+".sash", exprGrant(), p, Options{State: st})
		if out.Status != exitType || !strings.Contains(out.Stderr, "only fn and declare -r") {
			t.Errorf("%s: %+v", f, out)
		}
		if len(st.fns) != 0 {
			t.Errorf("%s defined %v before refusing", f, st.fns)
		}
		for _, c := range p.calls {
			if c.Key == "ps" {
				t.Errorf("%s ran ps", f)
			}
		}
	}
	// The file is not expanded: a * in it stays a word until the fn runs.
	p.calls = nil
	if out := Run("use /var/log/glob.sash", exprGrant(), p, Options{}); out.Status != 0 || len(p.calls) != 1 {
		t.Fatalf("glob at load: %+v calls %v", out, p.calls)
	}
}

func TestUseIsBehindFileRead(t *testing.T) {
	p := &fileParent{files: map[string]string{"/etc/x.sash": "fn a() { echo }\n"}}
	for _, g := range []struct {
		name string
		src  string
	}{{"outside roots", "use /etc/x.sash"}, {"missing", "use /var/log/none.sash"}} {
		p.calls = nil
		out := Run(g.src, exprGrant(), p, Options{})
		if out.Status == 0 {
			t.Errorf("%s: %+v", g.name, out)
		}
	}
	p.calls = nil
	if out := Run("use /etc/x.sash", exprGrant(), p, Options{}); out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("denied use called the parent: %+v %v", out, p.calls)
	}
	if pv := Preview("use /etc/x.sash", exprGrant(), Options{}); !pv.Block || !pv.Denied {
		t.Fatalf("preview: %+v", pv)
	}
	if out := Why("use /etc/x.sash", exprGrant(), Options{}); !strings.Contains(out.Stdout, "outside_roots") {
		t.Fatalf("why: %q", out.Stdout)
	}
}
