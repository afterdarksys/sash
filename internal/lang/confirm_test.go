package lang

import (
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type confirmer struct {
	answers []string // "y", "n", "^C"
	prompts []string
	onAsk   func()
}

func (c *confirmer) ask(p string) (bool, bool) {
	c.prompts = append(c.prompts, p)
	if c.onAsk != nil {
		c.onAsk()
	}
	a := "n"
	if len(c.answers) > 0 {
		a, c.answers = c.answers[0], c.answers[1:]
	}
	return a == "y", a == "^C"
}

func confirmGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"ps", "unit.restart", "file.write", "file.read"}, FileWrite: []string{"/tmp"}, FileRead: []string{"/tmp", "/var/log"}}
}

// confirmFiles serves a spec and a template for template_file.
type confirmFiles struct{}

func (confirmFiles) Do(v parent.Verb) (parent.Result, error) {
	body := ""
	switch v.Path {
	case "/var/log/s.yaml":
		body = "a: 1\n"
	case "/var/log/t":
		body = "plain\n"
	}
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte(body)}, nil
}

func confirmRun(src string, c *confirmer, p *countParent) Outcome {
	opt := Options{State: NewState()}
	if c != nil {
		opt.Confirm = c.ask
	}
	return Run("set -o confirm\n"+src, confirmGrant(), p, opt)
}

// Without a terminal, confirm fails closed: setting it is exit 2, and a
// State that has it on calls no mutating verb.
func TestConfirmNeedsATerminal(t *testing.T) {
	for _, src := range []string{"set -o confirm", "shopt -s confirm", "with set -o confirm { ps }"} {
		p := &countParent{}
		out := Run(src+"\nunit restart nginx", confirmGrant(), p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, msgConfirmTerminal) || p.n != 0 {
			t.Errorf("%q: %+v calls %d", src, out, p.n)
		}
	}
	st := NewState()
	st.confirm = true
	p := &countParent{}
	out := Run("ps\nunit restart nginx\necho after", confirmGrant(), p, Options{State: st})
	if out.Status != exitType || !strings.Contains(out.Stderr, msgConfirmTerminal) || p.n != 1 || strings.Contains(out.Stdout, "after") {
		t.Fatalf("state on, no terminal: %+v calls %d", out, p.n)
	}
}

func TestConfirmAsksEachMutatingVerb(t *testing.T) {
	c := &confirmer{answers: []string{"y", "y", "y"}}
	p := &countParent{}
	out := confirmRun("ps\nfor i in [ 1..3 ] { unit restart nginx }", c, p)
	if out.Status != 0 || p.n != 4 || len(c.prompts) != 3 || c.prompts[0] != "confirm unit.restart unit restart nginx? [y + Enter / N]" {
		t.Fatalf("%+v calls %d prompts %q", out, p.n, c.prompts)
	}
	c = &confirmer{answers: []string{"y", "y"}}
	out = Run("set -o confirm\necho hi > /tmp/a\ntemplate_file /var/log/s.yaml /var/log/t /tmp/o", confirmGrant(), confirmFiles{}, Options{State: NewState(), Confirm: c.ask})
	if len(c.prompts) < 1 || c.prompts[0] != "confirm file.write > /tmp/a? [y + Enter / N]" {
		t.Fatalf("redirect %+v prompts %q", out, c.prompts)
	}
	found := false
	for _, pr := range c.prompts[1:] {
		found = found || strings.HasPrefix(pr, "confirm file.write > /tmp/o")
	}
	if !found {
		t.Fatalf("template_file write not asked: %q (%+v)", c.prompts, out)
	}
}

func TestConfirmNoIsDeclined(t *testing.T) {
	c := &confirmer{answers: []string{"n"}}
	p := &countParent{}
	out := confirmRun("unit restart nginx\necho after", c, p)
	if out.Status != exitDenied || out.Stderr != "declined unit.restart\n" || p.n != 0 || out.Stdout != "" {
		t.Fatalf("%+v calls %d", out, p.n)
	}
	// retry does not ask again.
	c = &confirmer{answers: []string{"n", "y", "y"}}
	p = &countParent{}
	out = confirmRun("retry 3 { unit restart nginx }", c, p)
	if out.Status != exitDenied || len(c.prompts) != 1 || p.n != 0 {
		t.Fatalf("retry: %+v prompts %d calls %d", out, len(c.prompts), p.n)
	}
	// try catches it with key set.
	c = &confirmer{answers: []string{"n"}}
	out = confirmRun("try { unit restart nginx } catch e { echo $e.key $e.status }\necho after", c, &countParent{})
	if out.Status != 0 || out.Stdout != "unit.restart 126\nafter\n" {
		t.Fatalf("try: %+v", out)
	}
	// Ctrl-C is no and stops the line; try does not catch it.
	c = &confirmer{answers: []string{"^C"}}
	p = &countParent{}
	out = confirmRun("try { unit restart nginx } catch e { echo caught }\necho after", c, p)
	if out.Status != exitDenied || out.Stdout != "" || p.n != 0 || !strings.Contains(out.Stderr, "declined unit.restart") {
		t.Fatalf("ctrl-c: %+v", out)
	}
}

// Waiting for the answer counts against timeout: the deadline is checked
// again after it, before the parent call.
func TestConfirmTimeoutAcrossASlowAnswer(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := nowFn
	nowFn = func() time.Time { return now }
	defer func() { nowFn = old }()
	c := &confirmer{answers: []string{"y"}, onAsk: func() { now = now.Add(time.Minute) }}
	p := &countParent{}
	out := confirmRun("timeout 30s { unit restart nginx }", c, p)
	if out.Status != exitType || !strings.Contains(out.Stderr, "timeout") || p.n != 0 || len(c.prompts) != 1 {
		t.Fatalf("%+v calls %d", out, p.n)
	}
}

// Plan mode stops a mutating verb before confirm, so it asks nothing.
func TestConfirmPlanAsksNothing(t *testing.T) {
	c := &confirmer{}
	p := &countParent{}
	out := confirmRun("set -o plan\nunit restart nginx\necho hi > /tmp/x", c, p)
	if out.Status != 0 || len(c.prompts) != 0 || p.n != 0 || !strings.Contains(out.Stderr, "plan: would") {
		t.Fatalf("%+v prompts %q", out, c.prompts)
	}
}

// The prompt escapes hostile argv.
func TestConfirmPromptEscapes(t *testing.T) {
	c := &confirmer{answers: []string{"n"}}
	confirmRun("unit restart 'x\x1b]0;pwn\x07\u202ey'", c, &countParent{})
	if len(c.prompts) != 1 || strings.ContainsAny(c.prompts[0], "\x1b\x07\u202e") || !strings.Contains(c.prompts[0], `x\x1b]0;pwn\x07\u202ey`) {
		t.Fatalf("prompt %q", c.prompts)
	}
}

// needs, lint, and why read the script as before.
func TestConfirmLeavesStaticToolsAlone(t *testing.T) {
	src := "set -o confirm\nunit restart nginx"
	if out := Needs(src, NeedsOptions{}); out.Status != 0 || !strings.Contains(out.Stdout, "unit.restart") {
		t.Fatalf("needs %+v", out)
	}
	if out := Lint(src); out.Status == exitType {
		t.Fatalf("lint %+v", out)
	}
	if out := Why("unit restart nginx", confirmGrant(), Options{State: func() *State { s := NewState(); s.confirm = true; return s }()}); out.Status == exitType {
		t.Fatalf("why %+v", out)
	}
}

// Once on, confirm is a latch: no route in the language turns it off, and
// the verb after the attempt is still asked about (or never runs).
func TestConfirmIsALatch(t *testing.T) {
	for _, src := range []string{
		"set +o confirm; unit restart nginx",
		"set +o confirm\nunit restart nginx",
		"shopt -u confirm\nunit restart nginx",
		"with set +o confirm { unit restart nginx }",
		"fn off() { shopt -u confirm }\noff\nunit restart nginx",
		"fn off() { set +o confirm }\ntry { off } catch { echo caught }\nunit restart nginx",
		"try { set +o confirm } catch { echo caught }\nunit restart nginx",
		"for i in [ 1..2 ] { set +o confirm }\nunit restart nginx",
	} {
		c := &confirmer{answers: []string{"n", "n"}}
		p := &countParent{}
		out := confirmRun(src, c, p)
		if p.n != 0 {
			t.Errorf("%q reached the parent: %+v", src, out)
		}
		if !strings.Contains(out.Stderr, msgConfirmLocked) && !strings.Contains(out.Stdout, "caught") {
			t.Errorf("%q: no lock refusal: %+v", src, out)
		}
	}
	// set +o confirm alone is exit 2.
	if out := confirmRun("set +o confirm", &confirmer{}, &countParent{}); out.Status != exitType || out.Stderr != msgConfirmLocked+"\n" {
		t.Fatalf("%+v", out)
	}
	// A with block that turns it on leaves it on.
	st := NewState()
	c := &confirmer{answers: []string{"n", "n"}}
	Run("with set -o confirm { ps }", confirmGrant(), &countParent{}, Options{State: st, Confirm: c.ask})
	if !st.Confirm() {
		t.Fatal("with dropped the latch")
	}
	st.ConfirmOff()
	if st.Confirm() {
		t.Fatal("ConfirmOff")
	}
}

// A cell with a space, or an empty one, is quoted in the prompt.
func TestConfirmPromptQuotesCells(t *testing.T) {
	c := &confirmer{answers: []string{"n"}}
	confirmRun("echo hi > '/tmp/a b'", c, &countParent{})
	if len(c.prompts) != 1 || c.prompts[0] != "confirm file.write > '/tmp/a b'? [y + Enter / N]" {
		t.Fatalf("%q", c.prompts)
	}
	c = &confirmer{answers: []string{"n"}}
	confirmRun("unit restart ''", c, &countParent{})
	if len(c.prompts) != 1 || c.prompts[0] != "confirm unit.restart unit restart ''? [y + Enter / N]" {
		t.Fatalf("%q", c.prompts)
	}
}
