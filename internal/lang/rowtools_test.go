package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/parent"
)

type procParent struct{ calls int }

func (p *procParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls++
	return parent.Result{Kind: parent.KindRows, Cols: []string{"user", "pid", "cpu", "cmd"}, Rows: []parent.Row{
		{"user": "ryan", "pid": "900", "cpu": "5", "cmd": "zsh"},
		{"user": "root", "pid": "1", "cpu": "90", "cmd": "init"},
		{"user": "ryan", "pid": "1200", "cpu": "40", "cmd": "sshd"},
		{"user": "www", "pid": "77", "cpu": "40", "cmd": "nginx"},
	}}, nil
}

func procs(t *testing.T, src string) Outcome {
	t.Helper()
	st := NewState()
	return Run(src, exprGrant(), &procParent{}, Options{State: st})
}

func TestRowTools(t *testing.T) {
	for src, want := range map[string]string{
		"ps |> sort by pid |> select pid":                                   "1\n77\n900\n1200",
		"ps |> sort by cpu desc pid |> select cmd":                          "init\nnginx\nsshd\nzsh",
		"ps |> uniq user |> select user":                                    "ryan\nroot\nwww",
		"ps |> group by user |> count |> sort by count desc user":           "ryan  2\nroot  1\nwww  1",
		"ps |> group by user |> sum cpu":                                    "ryan  45\nroot  90\nwww  40",
		"ps |> group by user |> avg cpu |> filter [ $it.user == \"ryan\" ]": "ryan  22.5",
		"ps |> max cpu":               "90",
		"ps |> min cmd":               "init",
		"ps |> count":                 "4",
		"ps |> first 2 |> select cmd": "zsh\ninit",
		"ps |> last 1 |> select cmd":  "nginx",
		"ps |> skip 3 |> select cmd":  "nginx",
		"ps |> first 99 |> count":     "4",
		"ps |> filter [ $it.cpu >= 40 && $it.user != \"root\" ] |> select cmd":   "sshd\nnginx",
		"let lim = 50; ps |> filter [ $it.cpu > $lim ] |> select cmd":            "init",
		"ps |> each [ {\"who\": upper($it.user)} ] |> select who pid |> first 1": "RYAN  900",
		"ps |> each [ $it.pid * 2 ] |> first 1":                                  "1800",
		"echo \"a:1\nb:2\" | lines |> each [ split($it.line, \":\")[0] ]":        "a\nb",
	} {
		out := procs(t, src)
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestRowToolErrors(t *testing.T) {
	for src, msg := range map[string]string{
		"ps |> sort cpu":            "usage sort",
		"ps |> sort by desc":        "usage sort",
		"ps |> group user":          "usage group",
		"ps |> first x":             "usage first",
		"ps |> skip -1":             "usage skip",
		"ps |> sum":                 "usage sum",
		"ps |> sum cmd":             "type sum cmd",
		"ps |> filter x":            "usage filter",
		"ps |> each [ $it.nope.x ]": "",
		"ps |> each [ $it.pid == \"1\" ? 1 : 2 ]": "",
		"ps | sort by pid":                        "type |",
		"sort by pid":                             "type rows",
		"echo x |> lines":                         "type |>",
		"fn count() { echo }":                     "usage fn",
	} {
		out := procs(t, src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v want %q", src, out, msg)
		}
	}
	if out := procs(t, "ps |> each [ $it.pid > 100 ? {} : 1 ]"); out.Status != exitType {
		t.Errorf("ternary is not syntax: %+v", out)
	}
	st := NewState()
	st.loopMax = 2
	out := Run("ps |> filter [ true ] |> count", exprGrant(), &procParent{}, Options{State: st})
	if out.Status != exitType || !strings.Contains(out.Stderr, "loop max") {
		t.Fatalf("filter rows count against loopmax: %+v", out)
	}
	out = procs(t, "ps |> each [ $it.pid > 100 && {} || 1 ]")
	if out.Status != 0 {
		t.Fatalf("each with bool: %+v", out)
	}
}

// Row tools call no verb: one ps call feeds the whole pipeline.
func TestRowToolsCallNoVerb(t *testing.T) {
	p := &procParent{}
	Run("ps |> filter [ true ] |> each [ {\"a\": 1} ] |> group by user |> count |> sort by count |> uniq |> first 1", exprGrant(), p, Options{})
	if p.calls != 1 {
		t.Fatalf("calls %d", p.calls)
	}
}
