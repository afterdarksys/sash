package line

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeTTY is a terminal: pending is what was typed before the prompt (the
// kernel's queue); answer is what the person types after seeing it, one
// byte per read, each after its gap on the test clock.
type fakeTTY struct {
	pending, answer []byte
	gaps            []time.Duration
	prompted        bool
	flushes         int
	clock           *time.Time
}

func (f *fakeTTY) Read(p []byte) (int, error) {
	if len(f.pending) > 0 {
		n := copy(p, f.pending)
		f.pending = f.pending[n:]
		return n, nil
	}
	if f.prompted && len(f.answer) > 0 {
		if len(f.gaps) > 0 {
			*f.clock = f.clock.Add(f.gaps[0])
			f.gaps = f.gaps[1:]
		}
		n := copy(p, f.answer[:1])
		f.answer = f.answer[1:]
		return n, nil
	}
	return 0, io.EOF
}

func (f *fakeTTY) flush() error {
	f.flushes++
	f.pending = nil
	return nil
}

// ttyOut marks the terminal prompted once the question is drawn.
type ttyOut struct {
	tty *fakeTTY
	buf bytes.Buffer
}

func (o *ttyOut) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[y + Enter / N]")) {
		o.tty.prompted = true
	}
	return o.buf.Write(p)
}

func ask(t *testing.T, tty *fakeTTY, buffered int) (bool, bool, string) {
	t.Helper()
	clock := time.Unix(1_800_000_000, 0)
	tty.clock = &clock
	old := nowFn
	nowFn = func() time.Time { return clock }
	defer func() { nowFn = old }()
	r := bufio.NewReader(tty)
	if buffered > 0 {
		if _, err := r.Peek(buffered); err != nil {
			t.Fatal(err)
		}
	}
	out := &ttyOut{tty: tty}
	yes, intr := askConfirm(r, out, tty.flush, "confirm unit.restart unit restart nginx? [y + Enter / N]")
	return yes, intr, out.buf.String()
}

const human = 300 * time.Millisecond

// A y typed or pasted before the prompt never answers: not one the reader
// buffered, not one still in the terminal's queue.
func TestConfirmIgnoresTypeahead(t *testing.T) {
	tty := &fakeTTY{pending: []byte("y\ry\r")}
	if yes, _, out := ask(t, tty, 4); yes || tty.flushes != 1 || !strings.Contains(out, "[y + Enter / N]") {
		t.Fatalf("buffered y answered: yes %v flushes %d", yes, tty.flushes)
	}
	tty = &fakeTTY{pending: []byte("y\r")}
	if yes, _, _ := ask(t, tty, 0); yes {
		t.Fatal("queued y answered")
	}
	tty = &fakeTTY{pending: []byte("y\rY\r"), answer: []byte("n")}
	if yes, _, _ := ask(t, tty, 2); yes {
		t.Fatal("typeahead outvoted the answer")
	}
	r := bufio.NewReader(strings.NewReader("y\r"))
	if yes, _ := askConfirm(r, io.Discard, func() error { return errors.New("no") }, "q? [y + Enter / N]"); yes {
		t.Fatal("yes without a flush")
	}
}

// Yes is y, a pause, then Enter. A fast y and newline (a paste), a
// bracketed paste, an arrow key, or y and any other key is no.
func TestConfirmKeys(t *testing.T) {
	cases := []struct {
		answer string
		gaps   []time.Duration
		yes    bool
		intr   bool
	}{
		{"y\r", []time.Duration{human, human}, true, false},
		{"Y\n", []time.Duration{human, human}, true, false},
		{"y\r", []time.Duration{human, 50 * time.Millisecond}, true, false},
		{"y\n", []time.Duration{human, 10 * time.Millisecond}, false, false},
		{"y\r", []time.Duration{human, 49 * time.Millisecond}, false, false},
		{"y", []time.Duration{human}, false, false},
		{"yy", []time.Duration{human, human}, false, false},
		{"yx", []time.Duration{human, human}, false, false},
		{"y\x03", []time.Duration{human, human}, false, true},
		{"\x1b[200~y\n\x1b[201~", []time.Duration{human, human, human}, false, false},
		{"\x1b[A", []time.Duration{human}, false, false},
		{"n", []time.Duration{human}, false, false},
		{"\r", []time.Duration{human}, false, false},
		{"x", []time.Duration{human}, false, false},
		{"\x03", []time.Duration{human}, false, true},
		{"", nil, false, false},
	}
	for _, c := range cases {
		tty := &fakeTTY{answer: []byte(c.answer), gaps: c.gaps}
		yes, intr, out := ask(t, tty, 0)
		if yes != c.yes || intr != c.intr {
			t.Errorf("%q gaps %v: yes %v interrupted %v", c.answer, c.gaps, yes, intr)
		}
		if !strings.HasPrefix(out, "\x1b[?2004h") || !strings.HasSuffix(out, "\x1b[?2004l") {
			t.Errorf("%q: bracketed paste not set and restored: %q", c.answer, out)
		}
	}
	// The y and the newline of a paste arriving together: the flush after
	// the y drops the newline, so a later Enter must come from the person.
	tty := &fakeTTY{answer: []byte("y\r"), gaps: []time.Duration{human, 0}}
	if yes, _, _ := ask(t, tty, 0); yes || tty.flushes != 2 {
		t.Fatalf("flushes %d yes %v", tty.flushes, yes)
	}
}

// confirm off is an editor-only whole line: it clears the latch, and no
// other form does.
func TestEditorConfirmOff(t *testing.T) {
	p := &countParent{}
	e := New(srvGrant(), p)
	e.opt.Confirm = func(string) (bool, bool) { return false, false }
	e.Feed([]byte("set -o confirm\r"))
	for _, line := range []string{"confirm off; unit restart nginx", "set +o confirm; unit restart nginx", "confirm  off x", "confirm"} {
		e.Feed([]byte{0x15})
		e.Feed([]byte(line + "\r"))
		if !e.opt.State.Confirm() {
			t.Fatalf("%q turned confirm off", line)
		}
	}
	if p.n != 0 {
		t.Fatalf("calls %d", p.n)
	}
	runs := e.Runs
	e.Feed([]byte{0x15})
	e.Feed([]byte("confirm off\r"))
	if e.opt.State.Confirm() || e.Runs != runs {
		t.Fatalf("confirm off: on %v", e.opt.State.Confirm())
	}
}

// The editor passes its Confirm to every run, so set -o confirm holds
// from line to line; without one (no terminal) it is refused.
func TestEditorConfirmAcrossLines(t *testing.T) {
	asked := 0
	p := &countParent{}
	e := New(srvGrant(), p)
	e.opt.Confirm = func(string) (bool, bool) { asked++; return false, false }
	e.Feed([]byte("set -o confirm\r"))
	e.Feed([]byte("unit restart nginx\r"))
	if asked != 1 || p.n != 0 || e.Last.Status != 126 || e.Last.Stderr != "declined unit.restart\n" {
		t.Fatalf("asked %d calls %d last %+v", asked, p.n, e.Last)
	}
	plain := New(srvGrant(), p)
	plain.Feed([]byte("set -o confirm\r"))
	if plain.Last.Status != 2 || !strings.Contains(plain.Last.Stderr, "confirm needs a terminal") {
		t.Fatalf("no terminal: %+v", plain.Last)
	}
}
