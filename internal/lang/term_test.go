package lang

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
)

// fakeTerm is a terminal with scripted answers. Every question it was
// asked is kept.
type fakeTerm struct {
	answers  []string
	errs     []error
	yes      []bool
	asked    []TermAsk
	confirms []TermAsk
}

func (f *fakeTerm) Prompt(a TermAsk) (string, error) {
	f.asked = append(f.asked, a)
	i := len(f.asked) - 1
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i < len(f.answers) {
		return f.answers[i], err
	}
	return "", err
}

func (f *fakeTerm) Confirm(a TermAsk) (bool, error) {
	f.confirms = append(f.confirms, a)
	i := len(f.confirms) - 1
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i < len(f.yes) {
		return f.yes[i], err
	}
	return false, err
}

// is_tty() is false without a terminal and true with one, in a word, a
// condition, and a row filter.
func TestIsTTY(t *testing.T) {
	const src = "echo [ is_tty() ]\nif [ is_tty() ] { echo asked } else { echo unattended }\n"
	out := Run(src, grant.Grant{}, skillParent{t}, Options{})
	if out.Status != 0 || out.Stdout != "false\nunattended\n" {
		t.Errorf("no terminal: %+v", out)
	}
	out = Run(src, grant.Grant{}, skillParent{t}, Options{Term: &fakeTerm{}})
	if out.Status != 0 || out.Stdout != "true\nasked\n" {
		t.Errorf("a terminal: %+v", out)
	}
	out = Run("echo a | lines |> filter [ is_tty() ]\n", grant.Grant{}, skillParent{t}, Options{Term: &fakeTerm{}})
	if out.Status != 0 || out.Stdout != "a\n" {
		t.Errorf("filter: %+v", out)
	}
	// It takes no argument.
	out = Run("echo [ is_tty(1) ]\n", grant.Grant{}, skillParent{t}, Options{})
	if out.Status != exitType {
		t.Errorf("is_tty(1): %+v", out)
	}
	// builtin lists it with the other functions.
	out = Run("builtin is_tty\n", grant.Grant{}, skillParent{t}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "is_tty()") {
		t.Errorf("builtin: %+v", out)
	}
}

// is_tty() reads no name but is not a constant: lint does not call a
// branch on it dead.
func TestIsTTYIsNotConstantToLint(t *testing.T) {
	if got := Lint("if [ is_tty() ] { echo a } else { echo b }\n"); strings.Contains(got.Stdout, "constant") || strings.Contains(got.Stdout, "unreachable") {
		t.Errorf("lint calls is_tty() constant: %q", got.Stdout)
	}
}

// A recording pins is_tty() like the clock: a replay answers from the
// tape, whatever terminal it runs at, and a recording without the reading
// diverges.
func TestIsTTYIsRecordedAndReplayed(t *testing.T) {
	const src = "echo [ is_tty() ]\n"
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: tape, Term: &fakeTerm{}}); out.Status != 0 || out.Stdout != "true\n" {
		t.Fatalf("record: %+v", out)
	}
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"kind":"tty","resp":{"value":"true"}`) {
		t.Fatalf("no tty event: %s", buf.String())
	}
	// Replayed with no terminal at all: still true, from the tape.
	replay, err := LoadReplay(bytes.NewReader(buf.Bytes()), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: replay}); out.Status != 0 || out.Stdout != "true\n" {
		t.Errorf("replay: %+v", out)
	}
	if err := replay.Finish(); err != nil {
		t.Errorf("replay finish: %v", err)
	}
	// A recording of another script has no tty event here: it diverges.
	const other = "echo [ 1 + 1 ]\n"
	buf.Reset()
	tape, err = NewRecorder(&buf, other, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	Run(other, grant.Grant{}, skillParent{t}, Options{Tape: tape})
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	rec := strings.Replace(buf.String(), sha([]byte(other)), sha([]byte(src)), 1)
	replay, err = LoadReplay(strings.NewReader(rec), src, time.Now())
	if err != nil {
		t.Fatalf("a recording with its script hash swapped no longer loads: %v", err)
	}
	if out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: replay, Term: &fakeTerm{}}); out.Status != exitType || !strings.Contains(out.Stderr, "replay_diverged") {
		t.Errorf("a replay with no tty event: %+v", out)
	}
}

func termGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"unit.restart", "unit.status", "file.read", "file.write"}, FileRead: []string{"/srv"}, FileWrite: []string{"/srv"}}
}

// term prompt gives the line as a value; term confirm only a status.
func TestTermPromptAndConfirm(t *testing.T) {
	term := &fakeTerm{answers: []string{"nginx", ""}}
	out := Run("let a = (term prompt \"unit?\")\necho got $a\nlet b = (term prompt \"again?\")\necho [ len($b) ]\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if out.Status != 0 || out.Stdout != "got nginx\n0\n" || out.Stderr != "term prompt: empty answer\n" {
		t.Errorf("prompt: %+v", out)
	}
	if len(term.asked) != 2 || term.asked[0].Question != "unit?" || term.asked[0].Secret || term.asked[0].Hung == nil || term.asked[0].Wait != 0 {
		t.Errorf("asked %+v", term.asked)
	}
	term = &fakeTerm{yes: []bool{true, false}}
	out = Run("term confirm \"go?\" && echo yes\nterm confirm \"again?\" || echo no\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if out.Status != 0 || out.Stdout != "yes\nno\n" || len(term.confirms) != 2 || term.confirms[1].Question != "again?" {
		t.Errorf("confirm: %+v asked %+v", out, term.confirms)
	}
	// A no is status 1, and the script goes on.
	out = Run("term confirm \"go?\"\necho after\n", grant.Grant{}, skillParent{t}, Options{Term: &fakeTerm{}})
	if out.Status != 0 || out.Stdout != "after\n" {
		t.Errorf("no: %+v", out)
	}
	if out = Run("term confirm \"go?\"\n", grant.Grant{}, skillParent{t}, Options{Term: &fakeTerm{}}); out.Status != 1 {
		t.Errorf("no as the last status: %+v", out)
	}
	// The question is a word like any other: a value expands.
	term = &fakeTerm{answers: []string{"x"}}
	Run("let u = nginx\nlet a = (term prompt \"restart $u?\")\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if len(term.asked) != 1 || term.asked[0].Question != "restart nginx?" {
		t.Errorf("asked %+v", term.asked)
	}
}

// Without a terminal both forms are exit 2 and stop the script; try does
// not catch it, and nothing after it runs.
func TestTermNeedsATerminal(t *testing.T) {
	for _, form := range []string{`term prompt "q"`, `term confirm "q"`, `let a = (term prompt "q")`, `if term confirm "q" { echo in }`} {
		src := "echo before\ntry { " + form + " } catch e { echo caught }\necho after\n"
		out := Run(src, grant.Grant{}, skillParent{t}, Options{})
		if out.Status != exitType || out.Stdout != "before\n" || !strings.Contains(out.Stderr, "term needs a terminal") {
			t.Errorf("%s: %+v", form, out)
		}
	}
}

// What an answer can never do, part one: a yes to term confirm is not the
// answer to confirm mode. The mutating verb after it still asks, and a no
// there declines it.
func TestTermConfirmIsNotConfirmModesAnswer(t *testing.T) {
	const src = "set -o confirm\nterm confirm \"restart nginx?\" && unit restart nginx\n"
	for _, c := range []struct {
		answer bool
		calls  int
		status int
	}{{true, 1, 0}, {false, 0, exitDenied}} {
		asked := 0
		p := &countParent{}
		out := Run(src, termGrant(), p, Options{
			Term:    &fakeTerm{yes: []bool{true}},
			Confirm: func(string) (bool, bool) { asked++; return c.answer, false },
		})
		if asked != 1 || p.n != c.calls || out.Status != c.status {
			t.Errorf("confirm answers %v: asked %d times, %d calls, %+v", c.answer, asked, p.n, out)
		}
	}
	// With no confirm prompt at all the verb fails closed, whatever term said.
	p := &countParent{}
	out := Run("term confirm \"restart?\" && unit restart nginx\n", termGrant(), p, Options{Term: &fakeTerm{yes: []bool{true}}, State: confirmState(t)})
	if p.n != 0 || out.Status != exitType || !strings.Contains(out.Stderr, msgConfirmTerminal) {
		t.Errorf("no confirm prompt: %d calls, %+v", p.n, out)
	}
}

// confirmState is a State with confirm mode on.
func confirmState(t *testing.T) *State {
	t.Helper()
	st := NewState()
	if out := Run("set -o confirm\n", grant.Grant{}, &countParent{}, Options{State: st, Confirm: func(string) (bool, bool) { return false, false }}); out.Status != 0 || !st.Confirm() {
		t.Fatalf("confirm on: %+v", out)
	}
	return st
}

// Part two: no answer turns a safety mode off.
func TestTermCannotTurnASafetyModeOff(t *testing.T) {
	st := confirmState(t)
	out := Run("term confirm \"off?\" && set +o confirm\n", grant.Grant{}, &countParent{}, Options{State: st, Term: &fakeTerm{yes: []bool{true}}, Confirm: func(string) (bool, bool) { return true, false }})
	if out.Status != exitType || !strings.Contains(out.Stderr, "confirm is locked") || !st.Confirm() {
		t.Errorf("set +o confirm after a yes: %+v confirm %v", out, st.Confirm())
	}
	st = NewState()
	st.LockPlan()
	p := &countParent{}
	out = Run("term confirm \"real run?\"\nset +o plan\nunit restart nginx\n", termGrant(), p, Options{State: st, Term: &fakeTerm{yes: []bool{true}}})
	if out.Status != exitType || !strings.Contains(out.Stderr, "plan is locked") || p.n != 0 {
		t.Errorf("set +o plan after a yes: %d calls %+v", p.n, out)
	}
	// Plan mode asks as a real run does, and stays on.
	st = NewState()
	st.LockPlan()
	term := &fakeTerm{yes: []bool{true}}
	out = Run("term confirm \"go?\" && unit restart nginx\n", termGrant(), p, Options{State: st, Term: term})
	if out.Status != 0 || len(term.confirms) != 1 || p.n != 0 || !strings.Contains(out.Stderr, "plan: would unit restart nginx") {
		t.Errorf("plan: %d calls %+v", p.n, out)
	}
}

// Part three: an answer is a value, never a command word, and term's own
// second word and flags are literals.
func TestTermAnswerNeverNamesACommand(t *testing.T) {
	p := &countParent{}
	for src, want := range map[string]string{
		"let ans = (term prompt \"cmd?\")\n$ans restart nginx\n": msgCommandFromValue,
		"let op = prompt\nterm $op \"q\"\n":                      msgCommandFromValue,
		"let op = confirm\nterm $op \"q\"\n":                     msgCommandFromValue,
		"let f = -s\nterm prompt $f \"q\"\n":                     msgCommandFromValue,
		"term\n":                                                 "usage term",
		"term prompt\n":                                          "usage term",
		"term ask \"q\"\n":                                       "usage term",
		"term prompt a b\n":                                      "usage term",
		"term confirm a b\n":                                     "usage term",
		"echo a | term prompt \"q\"\n":                           "usage term",
		"echo a | term confirm \"q\"\n":                          "usage term",
		"term prompt \"q\" < /srv/in\n":                          "usage term",
		"term prompt \"q\" <<< text\n":                           "usage term",
		"term prompt \"" + strings.Repeat("q", 1025) + "\"\n":    "usage term: question over 1024 bytes",
		"let q = [ repeat(\"q\", 1025) ]\nterm confirm $q\n":     "usage term: question over 1024 bytes",
		"fn term() { echo x }\n":                                 "usage fn",
	} {
		term := &fakeTerm{answers: []string{"unit"}, yes: []bool{true}}
		out := Run(src, termGrant(), p, Options{Term: term})
		if out.Status != exitType || !strings.Contains(out.Stderr, want) {
			t.Errorf("%q: want %q, got %+v", src, want, out)
		}
		if want != msgCommandFromValue || !strings.HasPrefix(src, "let ans") {
			if len(term.asked)+len(term.confirms) != 0 {
				t.Errorf("%q: a refused form asked the person", src)
			}
		}
	}
	if p.n != 0 {
		t.Errorf("%d parent calls", p.n)
	}
	// 1024 bytes is a question.
	term := &fakeTerm{answers: []string{"x"}}
	if out := Run("term prompt \""+strings.Repeat("q", 1024)+"\"\n", grant.Grant{}, p, Options{Term: term}); out.Status != 0 || len(term.asked) != 1 {
		t.Errorf("1024 bytes: %+v", out)
	}
}

// Each way a question ends without an answer.
func TestTermEndings(t *testing.T) {
	run := func(src string, err error) (Outcome, *fakeTerm) {
		term := &fakeTerm{errs: []error{err}}
		return Run(src, grant.Grant{}, skillParent{t}, Options{Term: term}), term
	}
	// The end of input is status 1 with an empty value; the script goes on.
	out, _ := run("let a = (term prompt \"q\")\necho [ len($a) ] after\n", ErrTermEOF)
	if out.Status != 0 || out.Stdout != "0 after\n" || !strings.Contains(out.Stderr, "term prompt: end of input") {
		t.Errorf("eof: %+v", out)
	}
	if out, _ = run("term prompt \"q\"\n", ErrTermEOF); out.Status != 1 {
		t.Errorf("eof status: %+v", out)
	}
	// try catches it where it catches any status 1: under set -e.
	out, _ = run("set -e\ntry { let a = (term prompt \"q\") } catch e { echo caught [ $e.status ] }\n", ErrTermEOF)
	if out.Status != 0 || out.Stdout != "caught 1\n" {
		t.Errorf("eof under try: %+v", out)
	}
	// retry does not ask again.
	out, term := run("retry 5 { term prompt \"q\" }\n", ErrTermEOF)
	if out.Status != 1 || len(term.asked) != 1 {
		t.Errorf("eof under retry: asked %d times, %+v", len(term.asked), out)
	}
	// An empty line is no answer, the same as the end of input: status 1
	// with an empty value, set -e stops, try catches it, retry does not
	// ask again, and an empty value never reaches the next statement with
	// status 0.
	empty := func(src string) (Outcome, *fakeTerm) {
		term := &fakeTerm{answers: []string{""}}
		return Run(src, grant.Grant{}, skillParent{t}, Options{Term: term}), term
	}
	for _, src := range []string{
		"let a = (term prompt \"q\")\necho [ len($a) ] after\n",
		"term prompt \"q\"\n",
		"set -e\nlet a = (term prompt \"q\")\necho RAN $a\n",
		"set -e\ntry { let a = (term prompt \"q\") } catch e { echo caught [ $e.status ] }\n",
		"retry 5 { term prompt \"q\" }\n",
		"declare -s tok = (term prompt -s \"q\")\necho after\n",
	} {
		got, gterm := empty(src)
		want, wterm := run(src, ErrTermEOF)
		if got.Status != want.Status || got.Stdout != want.Stdout || len(gterm.asked) != len(wterm.asked) ||
			strings.Replace(got.Stderr, "term prompt: empty answer", "term prompt: end of input", 1) != want.Stderr {
			t.Errorf("%q: empty %+v asked %d, eof %+v asked %d", src, got, len(gterm.asked), want, len(wterm.asked))
		}
		if strings.Contains(got.Stdout, "RAN") || (!strings.Contains(src, "try") && !strings.Contains(got.Stderr, "term prompt: empty answer")) {
			t.Errorf("%q: %+v", src, got)
		}
	}
	// Ctrl-C is 130 and stops the script: try does not catch it and retry
	// does not repeat it.
	for _, src := range []string{
		"try { term prompt \"q\" } catch e { echo caught }\necho after\n",
		"try { term confirm \"q\" } catch e { echo caught }\necho after\n",
		"retry 5 { term prompt \"q\" }\necho after\n",
	} {
		out, term = run(src, ErrTermInterrupted)
		if out.Status != 130 || out.Stdout != "" || !strings.Contains(out.Stderr, "term: interrupted") || len(term.asked)+len(term.confirms) != 1 {
			t.Errorf("%q: %+v", src, out)
		}
	}
	// A hangup ends the run as a hangup.
	if out, _ = run("term prompt \"q\"\necho after\n", ErrTermHangup); out.Status != exitHangup || out.Stdout != "" {
		t.Errorf("hangup: %+v", out)
	}
	for err, want := range map[error]string{
		ErrTermLong:    "term prompt: answer over 4096 bytes",
		ErrTermNotText: "term prompt: answer is not text",
		ErrTermTimeout: "timeout",
		ErrTermNone:    "term needs a terminal",
	} {
		out, _ = run("let a = (term prompt \"q\")\necho after $a\n", err)
		if out.Status != exitType || out.Stdout != "" || !strings.Contains(out.Stderr, want) {
			t.Errorf("%v: %+v", err, out)
		}
	}
	// An unknown error from the terminal fails closed.
	if out, _ = run("term confirm \"q\" && echo yes\n", errTimeout); out.Status != exitType || out.Stdout != "" {
		t.Errorf("unknown error: %+v", out)
	}
}

// Waiting counts against timeout: the question carries what is left of
// the deadline, and a deadline already past asks nothing.
func TestTermWaitsNoLongerThanTheDeadline(t *testing.T) {
	term := &fakeTerm{answers: []string{"x"}}
	out := Run("timeout 5s { term prompt \"q\" }\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if out.Status != 0 || len(term.asked) != 1 || term.asked[0].Wait <= 0 || term.asked[0].Wait > 5*time.Second {
		t.Errorf("wait %+v %+v", term.asked, out)
	}
	old := nowFn
	now := time.Unix(1_800_000_000, 0)
	nowFn = func() time.Time { now = now.Add(4 * time.Second); return now }
	defer func() { nowFn = old }()
	term = &fakeTerm{answers: []string{"x"}}
	out = Run("timeout 5s { term prompt \"q\" }\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if out.Status != exitType || len(term.asked) != 0 || !strings.Contains(out.Stderr, "timeout") {
		t.Errorf("past the deadline: asked %d %+v", len(term.asked), out)
	}
}

// term is refused in par, literally at parse and through a fn at run
// time, and in atomic, before the agent is asked anything.
func TestTermRefusedInParAndAtomic(t *testing.T) {
	for _, src := range []string{
		"par 2 { term confirm \"a\"\necho b }\n",
		"par 2 { echo a\nif term confirm \"a\" { echo b } }\n",
		"par 2 { let a = (term prompt \"q\")\necho b }\n",
		"par 2 for x in [ [1, 2] ] { term prompt \"q\" }\n",
		"par 2 { fn f() { term confirm \"a\" }\necho b }\n",
	} {
		term := &fakeTerm{yes: []bool{true}}
		out := Run(src, grant.Grant{}, skillParent{t}, Options{Term: term})
		if out.Status != exitType || !strings.Contains(out.Stderr, "parse: term inside par") || len(term.asked)+len(term.confirms) != 0 {
			t.Errorf("%q: %+v", src, out)
		}
	}
	term := &fakeTerm{yes: []bool{true}, answers: []string{"x"}}
	out := Run("fn ask() { term confirm \"a\" }\npar 2 { ask\necho b }\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if out.Status == 0 || !strings.Contains(out.Stderr, "term inside par") || strings.Contains(out.Stderr, "parse:") || len(term.confirms) != 0 {
		t.Errorf("through a fn: %+v", out)
	}
	for src, want := range map[string]string{
		"atomic { term confirm \"a\"\necho x > /srv/f }\n":                   "atomic_irreversible: term",
		"atomic { let a = (term prompt \"a\")\necho x > /srv/f }\n":          "atomic_irreversible: term",
		"fn ask() { term confirm \"a\" }\natomic { ask\necho x > /srv/f }\n": "atomic_irreversible: term (in fn ask)",
		"atomic { if term confirm \"a\" { echo x > /srv/f } }\n":             "atomic_irreversible: term",
		"fn a() { b }\nfn b() { term prompt \"q\" }\natomic { a }\n":         "atomic_irreversible: term (in fn b) (in fn a)",
	} {
		term := &fakeTerm{yes: []bool{true}}
		p := &countParent{}
		out := Run(src, termGrant(), p, Options{Term: term})
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || p.n != 0 || len(term.asked)+len(term.confirms) != 0 {
			t.Errorf("%q: %d calls, %+v", src, p.n, out)
		}
	}
}

// The run-time backstop: a machine inside a transaction refuses term even
// when the static check did not see it.
func TestTermAtomicBackstop(t *testing.T) {
	term := &fakeTerm{yes: []bool{true}}
	m := newMachine(grant.Grant{}, &countParent{}, nil)
	m.term = term
	m.atomic = &atomicTx{}
	_, st := m.runTerm(expStage{argv: []string{"term", "confirm", "q"}})
	if st != exitType || !strings.Contains(m.err.String(), "atomic_irreversible: term") || len(term.confirms) != 0 {
		t.Errorf("status %d stderr %q", st, m.err.String())
	}
}

// retry asks again on each attempt.
func TestTermInRetryAsksAgain(t *testing.T) {
	term := &fakeTerm{yes: []bool{false, false, true}}
	out := Run("retry 5 { term confirm \"ready?\" }\necho done\n", grant.Grant{}, skillParent{t}, Options{Term: term})
	if out.Status != 0 || len(term.confirms) != 3 || out.Stdout != "done\n" {
		t.Errorf("asked %d times, %+v", len(term.confirms), out)
	}
}

// A plain answer is recorded in clear as a term event, and a replay
// answers from the tape with no terminal at all.
func TestTermIsRecordedAndReplayed(t *testing.T) {
	const src = "let a = (term prompt \"unit?\")\nterm confirm \"sure?\" && echo yes $a\n"
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: tape, Term: &fakeTerm{answers: []string{"nginx"}, yes: []bool{true}}})
	if out.Status != 0 || out.Stdout != "yes nginx\n" {
		t.Fatalf("record: %+v", out)
	}
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	rec := buf.String()
	for _, want := range []string{`"kind":"term","resp":{"op":"prompt","value":"nginx"}`, `"kind":"term","resp":{"op":"confirm","value":"yes"}`} {
		if !strings.Contains(rec, want) {
			t.Errorf("recording lacks %s:\n%s", want, rec)
		}
	}
	if strings.Contains(rec, "unit?") || strings.Contains(rec, "sure?") {
		t.Errorf("the question is in the event: %s", rec)
	}
	replay, err := LoadReplay(strings.NewReader(rec), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A terminal that would answer otherwise is never asked.
	other := &fakeTerm{answers: []string{"apache"}, yes: []bool{false}}
	out = Run(src, grant.Grant{}, skillParent{t}, Options{Tape: replay, Term: other})
	if out.Status != 0 || out.Stdout != "yes nginx\n" || len(other.asked)+len(other.confirms) != 0 {
		t.Errorf("replay: %+v asked %d", out, len(other.asked)+len(other.confirms))
	}
	if err := replay.Finish(); err != nil {
		t.Errorf("replay finish: %v", err)
	}
	// A recorded no terminal replays as no terminal.
	buf.Reset()
	const one = "term confirm \"q\"\n"
	tape, err = NewRecorder(&buf, one, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	Run(one, grant.Grant{}, skillParent{t}, Options{Tape: tape})
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"error":"no_terminal"`) {
		t.Errorf("no terminal is not recorded: %s", buf.String())
	}
	replay, err = LoadReplay(bytes.NewReader(buf.Bytes()), one, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out = Run(one, grant.Grant{}, skillParent{t}, Options{Tape: replay, Term: &fakeTerm{yes: []bool{true}}}); out.Status != exitType || !strings.Contains(out.Stderr, "term needs a terminal") {
		t.Errorf("replay of no terminal: %+v", out)
	}
}

// A replay whose question is another kind than the recorded one diverges.
func TestTermReplayDivergesOnAnotherQuestionKind(t *testing.T) {
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, "x", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := tape.term("prompt", func() (string, string) { return "yes", "" }); !ok {
		t.Fatal("record")
	}
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	replay, err := LoadReplay(bytes.NewReader(buf.Bytes()), "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	asked := false
	if _, _, ok := replay.term("confirm", func() (string, string) { asked = true; return "yes", "" }); ok || asked || replay.Err() == nil || !strings.Contains(replay.Err().Error(), "replay_diverged") {
		t.Errorf("ok %v asked %v err %v", ok, asked, replay.Err())
	}
}

// origin names an answer as a person's: key term prompt, with no hash.
func TestTermAnswerHasAnOrigin(t *testing.T) {
	out := Run("let a = (term prompt \"q\")\nlet b = [ $a + \"x\" ]\norigin b\n", grant.Grant{}, skillParent{t}, Options{Term: &fakeTerm{answers: []string{"v"}}})
	if out.Status != 0 || !strings.Contains(out.Stdout, "term prompt") || !strings.Contains(out.Stdout, "  1  -") {
		t.Errorf("origin: %+v", out)
	}
}

func TestTermLintRules(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
		not  []string
	}{
		{"let u = (term prompt \"unit?\")\nunit restart $u\n", []string{"1 prompt-needs-terminal info", "2 prompt-to-mutation warning", "($u) reaches unit.restart"}, []string{"prompt-in-retry"}},
		{"let u = (term prompt \"u?\")\nlet v = [ $u + \".service\" ]\nunit restart $v\n", []string{"3 prompt-to-mutation", "($v)"}, nil},
		{"let p = (term prompt \"path?\")\necho x > $p\n", []string{"prompt-to-mutation", "a > redirect"}, nil},
		{"let b = (term prompt \"body?\")\necho $b | egrep x > /srv/out\n", []string{"prompt-to-mutation", "a > redirect"}, nil},
		{"term prompt \"body?\" > /srv/out\n", []string{"prompt-to-mutation", "the answer"}, nil},
		{"let u = (term prompt \"u?\")\nfn f(x) { unit restart $x }\nf $u\n", []string{"prompt-to-mutation", "($x)"}, nil},
		{"declare -r u = (term prompt \"u?\")\nsysctl set vm.swappiness $u\n", []string{"prompt-to-mutation", "sysctl.set"}, nil},
		{"let u = (term prompt \"unit?\")\nunit status $u\necho $u\n", []string{"prompt-needs-terminal"}, []string{"prompt-to-mutation"}},
		{"term confirm \"go?\" && unit restart nginx\n", []string{"prompt-needs-terminal"}, []string{"prompt-to-mutation"}},
		{"retry 3 { term confirm \"ready?\" }\n", []string{"1 prompt-in-retry warning", "prompt-needs-terminal"}, nil},
		{"fn ask() { term confirm \"ready?\" }\nretry 3 { ask }\n", []string{"2 prompt-in-retry"}, nil},
		{"retry 3 { unit status nginx }\nterm confirm \"a\"\nterm confirm \"b\"\n", []string{"2 prompt-needs-terminal"}, []string{"prompt-in-retry", "3 prompt-needs-terminal"}},
		{"let u = nginx\nunit restart $u\n", nil, []string{"prompt-"}},
		{"let x = (term prompt \"q\")\nunit $x nginx\n", []string{"2 prompt-subcommand error", "($x) selects what unit does"}, nil},
		{"let x = (term prompt \"q\")\nlet y = \"$x\"\npackage $y curl\n", []string{"3 prompt-subcommand error"}, nil},
		{"let x = (term prompt \"q\")\nsysctl $x vm.swappiness 1\n", []string{"prompt-subcommand"}, nil},
		{"let x = (term prompt \"q\")\nunit status $x\n", nil, []string{"prompt-subcommand"}},
		{"fn ask() { let a = (term prompt \"q\"); return $a }\nlet u = (ask)\nunit restart $u\n", []string{"3 prompt-to-mutation", "($u) reaches unit.restart"}, nil},
		{"fn inner() { let a = (term prompt \"q\"); return $a }\nfn ask() { let b = (inner); return $b }\nlet u = (ask)\nunit restart $u\n", []string{"4 prompt-to-mutation"}, nil},
		{"fn ask() { term confirm \"q\"; return \"nginx\" }\nlet u = (ask)\nunit restart $u\n", nil, []string{"prompt-to-mutation"}},
	} {
		got := Lint(c.src)
		// Column widths vary with the rules reported.
		got.Stdout = strings.Join(strings.Fields(got.Stdout), " ")
		for _, w := range c.want {
			if !strings.Contains(got.Stdout, w) {
				t.Errorf("%q: want %q in\n%s", c.src, w, got.Stdout)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got.Stdout, n) {
				t.Errorf("%q: do not want %q in\n%s", c.src, n, got.Stdout)
			}
		}
	}
	// Info alone does not fail a lint.
	if got := Lint("term confirm \"go?\"\n"); got.Status != 0 {
		t.Errorf("info only: %+v", got)
	}
	if got := Lint("let u = (term prompt \"u?\")\nunit restart $u #lint:allow prompt-to-mutation the person names the unit\n"); strings.Contains(got.Stdout, "prompt-to-mutation") {
		t.Errorf("allow: %s", got.Stdout)
	}
}

// term needs no key: --needs has no row for it, why calls it in process,
// and the closed table lists both forms.
func TestTermNeedsNoKey(t *testing.T) {
	if got := Needs("let a = (term prompt \"q\")\nterm confirm \"q\"\n", NeedsOptions{}); got.Status != 0 || strings.Contains(got.Stdout, "term") || strings.Contains(got.Stdout, "-  ") {
		t.Errorf("needs: %+v", got)
	}
	// A verb target built from an answer is dynamic.
	if got := Needs("let u = (term prompt \"u?\")\nunit restart $u\n", NeedsOptions{}); !strings.Contains(got.Stdout, "unit.restart") || !strings.Contains(got.Stdout, "dynamic") {
		t.Errorf("needs of an answer: %+v", got)
	}
	for _, src := range []string{`term prompt "q"`, `term confirm "q"`} {
		if got := Why(src, grant.Grant{}, Options{}); !strings.Contains(got.Stdout, "in_process") {
			t.Errorf("why %s: %+v", src, got)
		}
	}
	out := Run("builtin term\n", grant.Grant{}, skillParent{t}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "term prompt") || !strings.Contains(out.Stdout, "term confirm") {
		t.Errorf("builtin: %+v", out)
	}
}

// term prompt -s reads a secret with no echo, only as the whole value of
// a declare -s: the value prints as ***, and cannot be a verb argument or
// a > body.
func TestTermPromptSecret(t *testing.T) {
	term := &fakeTerm{answers: []string{"hunter2-SECRET"}}
	p := &countParent{}
	out := Run("declare -s tok = (term prompt -s \"token?\")\necho $tok\necho [ len($tok) ]\n", termGrant(), p, Options{Term: term})
	if out.Status != 0 || out.Stdout != "***\n***\n" || strings.Contains(out.Stdout+out.Stderr, "hunter2") {
		t.Errorf("secret: %+v", out)
	}
	if len(term.asked) != 1 || !term.asked[0].Secret || term.asked[0].Question != "token?" {
		t.Errorf("asked %+v", term.asked)
	}
	for src, want := range map[string]string{
		"declare -s tok = (term prompt -s \"t?\")\nunit restart $tok\n":             "a secret cannot leave sash",
		"declare -s tok = (term prompt -s \"t?\")\necho $tok > /srv/out\n":          "a secret cannot leave sash",
		"declare -s tok = (term prompt -s \"t?\")\nif [ $tok == \"x\" ] { echo }\n": "a secret cannot decide a branch",
	} {
		p := &countParent{}
		out := Run(src, termGrant(), p, Options{Term: &fakeTerm{answers: []string{"hunter2-SECRET"}}})
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || p.n != 0 || strings.Contains(out.Stdout+out.Stderr, "hunter2") {
			t.Errorf("%q: %d calls %+v", src, p.n, out)
		}
	}
	// Flags may stand together, and the plain prompt is not secret.
	out = Run("declare -rs tok = (term prompt -s \"t?\")\necho $tok\nlet a = (term prompt \"plain?\")\necho $a\n", grant.Grant{}, p, Options{Term: &fakeTerm{answers: []string{"s3cret-value", "seen"}}})
	if out.Status != 0 || out.Stdout != "***\nseen\n" {
		t.Errorf("declare -rs: %+v", out)
	}
	// origin shows no hash for it, and a later plain answer keeps its own.
	out = Run("declare -s tok = (term prompt -s \"t?\")\nlet a = (term prompt \"p?\")\norigin tok\norigin a\n", grant.Grant{}, p, Options{Term: &fakeTerm{answers: []string{"s3cret-value", "plain"}}})
	if out.Status != 0 || !strings.Contains(out.Stdout, "  1  ***") || !strings.Contains(out.Stdout, "  2  -") {
		t.Errorf("origin: %+v", out)
	}
	// No terminal, and each way a question ends, is the same as for a prompt.
	if out = Run("declare -s tok = (term prompt -s \"t?\")\necho after\n", grant.Grant{}, p, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "term needs a terminal") || out.Stdout != "" {
		t.Errorf("no terminal: %+v", out)
	}
	if out = Run("declare -s tok = (term prompt -s \"t?\")\necho after\n", grant.Grant{}, p, Options{Term: &fakeTerm{errs: []error{ErrTermInterrupted}}}); out.Status != 130 || out.Stdout != "" {
		t.Errorf("interrupted: %+v", out)
	}
}

// Every place -s may not stand is a parse error, so nothing before it
// runs and nobody is asked.
func TestTermPromptSecretMisplaced(t *testing.T) {
	const only = "term prompt -s: only as declare -s name = (term prompt -s \"question\")"
	for name, src := range map[string]string{
		"a let":                     "let tok = (term prompt -s \"t?\")\n",
		"a bare statement":          "term prompt -s \"t?\"\n",
		"a declare without -s":      "declare -r tok = (term prompt -s \"t?\")\n",
		"a declare with no flag":    "declare tok = (term prompt -s \"t?\")\n",
		"piped on":                  "declare -s tok = (term prompt -s \"t?\" | head -n 1)\n",
		"piped into":                "declare -s tok = (echo x | term prompt -s \"t?\")\n",
		"redirected":                "declare -s tok = (term prompt -s \"t?\" > /srv/out)\n",
		"a redirect on a bare one":  "term prompt -s \"t?\" > /srv/out\n",
		"an if condition":           "if term prompt -s \"t?\" { echo x }\n",
		"a while condition":         "while term prompt -s \"t?\" { break }\n",
		"a for source":              "for x in term prompt -s \"t?\" { echo x }\n",
		"a chain":                   "term prompt -s \"t?\" && echo x\n",
		"a with value":              "with tok = (term prompt -s \"t?\") { echo x }\n",
		"inside a fn, in a let":     "fn f() { let tok = (term prompt -s \"t?\") }\n",
		"inside try":                "try { term prompt -s \"t?\" } catch e { echo x }\n",
		"one placed, one misplaced": "declare -s a = (term prompt -s \"a?\")\nlet b = (term prompt -s \"b?\")\n",
		"a quoted flag is the flag": "let tok = (term prompt \"-s\" \"t?\")\n",
	} {
		term := &fakeTerm{answers: []string{"hunter2-SECRET"}}
		out := Run("echo before\n"+src, termGrant(), &countParent{}, Options{Term: term})
		if out.Status != exitType || !strings.Contains(out.Stderr, "parse: "+only) || out.Stdout != "" || len(term.asked) != 0 {
			t.Errorf("%s: %+v asked %d", name, out, len(term.asked))
		}
		if l := Lint(src); l.Status != exitType || !strings.Contains(l.Stderr, only) {
			t.Errorf("%s: lint %+v", name, l)
		}
	}
	// A fn argument, a flag from a value, -s with confirm, after the
	// question, or twice: refused before anyone is asked.
	for src, want := range map[string]string{
		"fn f(x) { echo $x }\nf (term prompt -s \"t?\")\n":                          "parse:",
		"fn f(a, b, c, d) { echo $a }\nf term prompt -s \"t?\"\n":                   "",
		"let f = -s\ndeclare -s tok = (term prompt $f \"t?\")\n":                    msgCommandFromValue,
		"declare -s tok = (term confirm -s \"t?\")\n":                               "usage term",
		"term confirm -s \"t?\"\n":                                                  "usage term",
		"declare -s tok = (term prompt \"t?\" -s)\n":                                "usage term",
		"declare -s tok = (term prompt -s -s \"t?\")\n":                             "usage term",
		"declare -s tok = (term prompt -s)\n":                                       "usage term",
		"declare -s tok = (term prompt -s \"" + strings.Repeat("q", 1025) + "\")\n": "usage term: question over 1024 bytes",
	} {
		term := &fakeTerm{answers: []string{"hunter2-SECRET"}}
		out := Run(src, termGrant(), &countParent{}, Options{Term: term})
		if want == "" {
			// The words are a fn's arguments, not a question: nobody is asked.
			if out.Status != 0 || out.Stdout != "term\n" || len(term.asked) != 0 {
				t.Errorf("%q: %+v", src, out)
			}
			continue
		}
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || len(term.asked) != 0 {
			t.Errorf("%q: want %q, got %+v asked %d", src, want, out, len(term.asked))
		}
	}
}

// The evaluator's own backstop, with the parser bypassed: the -s stage
// refuses to read unless a declare -s value pipe is running and it is
// that pipe's single stage.
func TestTermPromptSecretEvaluatorBackstop(t *testing.T) {
	stage := expStage{argv: []string{"term", "prompt", "-s", "t?"}, solo: true}
	for name, set := range map[string]func(m *machine, st *expStage){
		"not in a declare -s":  func(m *machine, st *expStage) {},
		"not the single stage": func(m *machine, st *expStage) { m.secretPipe = true; st.solo = false },
	} {
		term := &fakeTerm{answers: []string{"hunter2-SECRET"}}
		m := newMachine(grant.Grant{}, &countParent{}, nil)
		m.term = term
		st := stage
		set(m, &st)
		v, code := m.runTerm(st)
		if code != exitType || !strings.Contains(m.err.String(), msgSecretPrompt) || len(term.asked) != 0 || v.kind != valStr || v.s != "" || v.d != nil {
			t.Errorf("%s: code %d stderr %q asked %d value %+v", name, code, m.err.String(), len(term.asked), v)
		}
	}
	// In its place it reads, and the value is a secret.
	term := &fakeTerm{answers: []string{"hunter2-SECRET"}}
	m := newMachine(grant.Grant{}, &countParent{}, nil)
	m.term, m.secretPipe = term, true
	v, code := m.runTerm(stage)
	if code != 0 || len(term.asked) != 1 || !isSecret(toDatum(v)) {
		t.Errorf("in place: code %d value %+v", code, v)
	}
}

// A recording holds no byte of a -s answer and not its length; the event
// says only that a secret was asked for. A replay refuses it.
func TestTermPromptSecretIsNotRecordedAndNotReplayed(t *testing.T) {
	const src = "declare -s tok = (term prompt -s \"token?\")\necho $tok\nlet a = (term prompt \"again?\")\n"
	record := func(secret string) string {
		t.Helper()
		var buf bytes.Buffer
		tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: tape, Term: &fakeTerm{answers: []string{secret, "say " + secret}}}); out.Status != 0 || out.Stdout != "***\n" {
			t.Fatalf("record: %+v", out)
		}
		if err := tape.Finish(); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	// The tape never holds the answer, even in memory before it is written.
	{
		var buf bytes.Buffer
		tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		Run("declare -s tok = (term prompt -s \"token?\")\n", grant.Grant{}, skillParent{t}, Options{Tape: tape, Term: &fakeTerm{answers: []string{"hunter2-SECRET"}}})
		held := 0
		for _, p := range tape.pend {
			if p.kind == "term" {
				held++
				if p.value != "" || !p.redact {
					t.Errorf("the tape holds a -s answer: %+v", p)
				}
			}
		}
		if held != 1 {
			t.Errorf("%d term events held", held)
		}
	}
	rec := record("hunter2-SECRET")
	if strings.Contains(rec, "hunter2") || strings.Contains(rec, "aHVudGVyMi1TRUNSRVQ") {
		t.Fatalf("the secret is in the recording:\n%s", rec)
	}
	if !strings.Contains(rec, `"kind":"term","resp":{"op":"prompt-s","redacted":true}`) {
		t.Errorf("no prompt-s event:\n%s", rec)
	}
	// A later plain answer that holds the secret's text is redacted too.
	if !strings.Contains(rec, `"resp":{"op":"prompt","value":"***"}`) {
		t.Errorf("a plain answer holding the secret is in clear:\n%s", rec)
	}
	// No length: a secret of another length gives the same events, byte
	// for byte, but for the chain hashes.
	strip := func(r string) string {
		var out []string
		for _, l := range strings.Split(r, "\n") {
			if i := strings.Index(l, `,"prev":`); i >= 0 {
				l = l[:i]
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}
	if a, b := strip(rec), strip(record("a-much-longer-secret-than-the-first-one-was")); a != b {
		t.Errorf("the recording depends on the secret:\n%s\n%s", a, b)
	}
	replay, err := LoadReplay(strings.NewReader(rec), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	term := &fakeTerm{answers: []string{"typed-at-replay"}}
	out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: replay, Term: term})
	if out.Status != exitType || !strings.Contains(out.Stderr, "replay_secret 1") || out.Stdout != "" || len(term.asked) != 0 {
		t.Errorf("replay: %+v asked %d", out, len(term.asked))
	}
	// A plain prompt cannot be answered from a redacted event either.
	replay, err = LoadReplay(strings.NewReader(rec), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := replay.term("prompt-s", func() (string, string) { return "x", "" }); ok || !strings.Contains(replay.Err().Error(), "replay_diverged") {
		t.Errorf("a redacted event answered a plain question: %v", replay.Err())
	}
}

// The editor keeps no declare -s line, and Tab offers -s after term prompt.
func TestTermPromptSecretLineIsNotHistory(t *testing.T) {
	if !DeclaresSecret(`declare -s tok = (term prompt -s "token?")`) {
		t.Errorf("the line would be kept in history")
	}
	for _, b := range Builtins() {
		if b.Form == "term prompt" && strings.Join(b.Flags, " ") != "-s" {
			t.Errorf("flags %q", b.Flags)
		}
	}
}

// showTerm records what Options.Show had handed over when each question
// was asked.
type showTerm struct {
	fakeTerm
	shown *[]string
	at    []string
}

func (s *showTerm) Prompt(a TermAsk) (string, error) {
	s.at = append(s.at, strings.Join(*s.shown, "|"))
	return s.fakeTerm.Prompt(a)
}

func (s *showTerm) Confirm(a TermAsk) (bool, error) {
	s.at = append(s.at, strings.Join(*s.shown, "|"))
	return s.fakeTerm.Confirm(a)
}

// What a run printed is handed to Options.Show before a question, and
// only what was not handed over is in the Outcome; stderr inside a try
// or retry waits, since the try may move it into its catch.
func TestTermShowsOutputBeforeTheQuestion(t *testing.T) {
	var shown []string
	term := &showTerm{fakeTerm: fakeTerm{answers: []string{"nginx", "x"}, yes: []bool{true}}, shown: &shown}
	show := func(o, e string) { shown = append(shown, o+"/"+e) }
	src := "echo about to wipe\nlog warn \"careful\"\nterm confirm \"ok?\" && echo yes\n" +
		"try { echo inside; log warn \"held\"; let a = (term prompt \"name?\") } catch e { echo caught }\necho $a\n" +
		"retry 2 { let b = (term prompt \"again?\") }\necho end\n"
	out := Run(src, grant.Grant{}, skillParent{t}, Options{Term: term, Show: show})
	if len(term.at) != 3 || !strings.HasPrefix(term.at[0], "about to wipe\n/") || !strings.Contains(term.at[0], "warn careful") {
		t.Fatalf("shown at each question: %q", term.at)
	}
	if !strings.HasSuffix(term.at[1], "|yes\ninside\n/") || strings.Contains(term.at[1], "held") {
		t.Errorf("inside a try, stdout is shown and stderr held: %q", term.at[1])
	}
	if !strings.Contains(term.at[2], "held") {
		t.Errorf("after the try, its stderr is shown: %q", term.at[2])
	}
	all := strings.Join(shown, "") + out.Stdout + out.Stderr
	for _, want := range []string{"about to wipe", "careful", "yes", "inside", "held", "nginx", "end"} {
		if strings.Count(all, want) != 1 {
			t.Errorf("%q appears %d times in %q", want, strings.Count(all, want), all)
		}
	}
	if out.Status != 0 || out.Stdout != "end\n" || out.Stderr != "" {
		t.Errorf("the Outcome holds only what was not shown: %+v", out)
	}
	// Without Show the Outcome holds everything, as before.
	out = Run(src, grant.Grant{}, skillParent{t}, Options{Term: &fakeTerm{answers: []string{"nginx", "x"}, yes: []bool{true}}})
	if out.Stdout != "about to wipe\nyes\ninside\nnginx\nend\n" || !strings.Contains(out.Stderr, "careful") || !strings.Contains(out.Stderr, "held") {
		t.Errorf("no Show: %+v", out)
	}
}

// The word that selects what unit, package, or sysctl does never carries
// an answer: directly, through another binding, through a fn's parameter
// or its return, or an expression, it is exit 2 before any
// call. The answer as an argument, and a literal or a value from
// elsewhere as the subcommand, run as before.
func TestTermAnswerSelectsNoSubcommand(t *testing.T) {
	refused := []string{
		"let x = (term prompt \"q\")\nunit $x nginx\n",
		"let x = (term prompt \"q\")\nlet y = \"$x\"\npackage $y curl\n",
		"let x = (term prompt \"q\")\nsysctl $x vm.swappiness 1\n",
		"fn ask() { let a = (term prompt \"q\"); return $a }\nlet u = (ask)\nunit $u nginx\n",
		"fn go(s) { unit $s nginx }\nlet x = (term prompt \"q\")\ngo $x\n",
		"let x = (term prompt \"q\")\nlet y = [ lower(upper($x)) ]\nunit $y nginx\n",
	}
	for _, src := range refused {
		p := &recParent{}
		out := Run(src, termGrant(), p, Options{Term: &fakeTerm{answers: []string{"restart", "restart"}}})
		if out.Status != exitType || out.Stderr != "command_from_value: a term prompt answer selects no subcommand\n" || len(p.calls) != 0 {
			t.Errorf("%q: %+v calls %v", src, out, p.calls)
		}
	}
	// Past 16 origins a value keeps an overflow marker: the answer it may
	// hide still selects nothing.
	var b strings.Builder
	sum := ""
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&b, "let c%d = (cat /srv/f%d)\n", i, i)
		sum += fmt.Sprintf("$c%d + ", i)
	}
	b.WriteString("let x = (term prompt \"q\")\nlet y = [ " + sum + "$x ]\nunit $y nginx\n")
	refused = append(refused, b.String())
	for _, src := range refused[len(refused)-1:] {
		out := Run(src, termGrant(), &recParent{}, Options{Term: &fakeTerm{answers: []string{"restart"}}})
		if out.Status != exitType || !strings.Contains(out.Stderr, "a term prompt answer selects no subcommand") {
			t.Errorf("overflow: %+v", out)
		}
	}
	for _, src := range []string{
		"let x = (term prompt \"q\")\nunit restart $x\n",
		"let x = restart\nunit $x nginx\n",
	} {
		p := &recParent{}
		out := Run(src, termGrant(), p, Options{Term: &fakeTerm{answers: []string{"nginx"}}})
		if out.Status != 0 || len(p.calls) != 1 || p.calls[0].Key != "unit.restart" {
			t.Errorf("%q: %+v calls %v", src, out, p.calls)
		}
	}
}
