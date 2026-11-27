package lang

import (
	"bytes"
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
		t.Skipf("the header is chained to its script: %v", err)
	}
	if out := Run(src, grant.Grant{}, skillParent{t}, Options{Tape: replay, Term: &fakeTerm{}}); out.Status != exitType || !strings.Contains(out.Stderr, "replay_diverged") {
		t.Errorf("a replay with no tty event: %+v", out)
	}
}
