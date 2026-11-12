package line

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeTTY is a terminal: pending is what was typed before the prompt (the
// kernel's queue), answer is what the person types after seeing it.
type fakeTTY struct {
	pending, answer []byte
	prompted        bool
	flushes         int
}

func (f *fakeTTY) Read(p []byte) (int, error) {
	if len(f.pending) > 0 {
		n := copy(p, f.pending)
		f.pending = f.pending[n:]
		return n, nil
	}
	if f.prompted && len(f.answer) > 0 {
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
	if bytes.Contains(p, []byte("[y/N]")) {
		o.tty.prompted = true
	}
	return o.buf.Write(p)
}

func ask(tty *fakeTTY, buffered int) (bool, bool, string) {
	r := bufio.NewReader(tty)
	if buffered > 0 {
		if _, err := r.Peek(buffered); err != nil {
			panic(err)
		}
	}
	out := &ttyOut{tty: tty}
	yes, intr := askConfirm(r, out, tty.flush, "confirm unit.restart unit restart nginx? [y/N]")
	return yes, intr, out.buf.String()
}

// A y typed or pasted before the prompt never answers: not one the reader
// buffered, not one still in the terminal's queue.
func TestConfirmIgnoresTypeahead(t *testing.T) {
	// Already in the reader's buffer.
	tty := &fakeTTY{pending: []byte("yyyy")}
	if yes, _, out := ask(tty, 4); yes || tty.flushes != 1 || !strings.Contains(out, "[y/N]") {
		t.Fatalf("buffered y answered: yes %v flushes %d", yes, tty.flushes)
	}
	// Still in the terminal's queue.
	tty = &fakeTTY{pending: []byte("y")}
	if yes, _, _ := ask(tty, 0); yes {
		t.Fatal("queued y answered")
	}
	// Both, and the person then says no.
	tty = &fakeTTY{pending: []byte("yYy"), answer: []byte("n")}
	if yes, _, _ := ask(tty, 1); yes {
		t.Fatal("typeahead outvoted the answer")
	}
	// A failed flush is no.
	r := bufio.NewReader(strings.NewReader("y"))
	if yes, _ := askConfirm(r, io.Discard, func() error { return errors.New("no") }, "q? [y/N]"); yes {
		t.Fatal("yes without a flush")
	}
}

func TestConfirmKeys(t *testing.T) {
	for answer, want := range map[string][2]bool{
		"y": {true, false}, "Y": {true, false}, "n": {false, false}, "\r": {false, false},
		"\n": {false, false}, "x": {false, false}, "yes": {true, false}, "\x03": {false, true},
		"\x1b": {false, false}, "": {false, false},
	} {
		tty := &fakeTTY{answer: []byte(answer)}
		yes, intr, _ := ask(tty, 0)
		if yes != want[0] || intr != want[1] {
			t.Errorf("%q: yes %v interrupted %v", answer, yes, intr)
		}
		if answer == "yes" && len(tty.answer) != 2 {
			t.Errorf("read more than one key: %q left", tty.answer)
		}
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
