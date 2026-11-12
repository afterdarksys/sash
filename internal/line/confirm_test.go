package line

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/lang"
)

// ev is one byte arriving gap after the one before it.
type ev struct {
	gap time.Duration
	b   byte
}

// fakeKeys is a terminal on a test clock: an event with gap 0 has already
// arrived (typeahead), later ones arrive as the clock reaches them.
type fakeKeys struct {
	evs     []ev
	clock   *time.Time
	flushes int
}

func evs(gap time.Duration, s string) []ev {
	var out []ev
	for i := 0; i < len(s); i++ {
		out = append(out, ev{gap, s[i]})
	}
	return out
}

func (f *fakeKeys) ReadByte() (byte, error) {
	if len(f.evs) == 0 {
		return 0, io.EOF
	}
	*f.clock = f.clock.Add(f.evs[0].gap)
	b := f.evs[0].b
	f.evs = f.evs[1:]
	return b, nil
}

func (f *fakeKeys) ReadByteTimeout(d time.Duration) (byte, bool, error) {
	if len(f.evs) == 0 {
		*f.clock = f.clock.Add(d)
		return 0, false, nil
	}
	if f.evs[0].gap <= d {
		b, err := f.ReadByte()
		return b, true, err
	}
	*f.clock = f.clock.Add(d)
	f.evs[0].gap -= d
	return 0, false, nil
}

func (f *fakeKeys) Peek() (byte, bool) {
	if len(f.evs) > 0 && f.evs[0].gap == 0 {
		return f.evs[0].b, true
	}
	return 0, false
}

func (f *fakeKeys) Discard() {
	for len(f.evs) > 0 && f.evs[0].gap == 0 {
		f.evs = f.evs[1:]
	}
}

func (f *fakeKeys) flush() error {
	f.flushes++
	f.Discard()
	return nil
}

func withClock(t *testing.T) *time.Time {
	t.Helper()
	clock := time.Unix(1_800_000_000, 0)
	old := nowFn
	nowFn = func() time.Time { return clock }
	t.Cleanup(func() { nowFn = old })
	return &clock
}

func ask(t *testing.T, keys []ev) (bool, bool, string, *fakeKeys) {
	t.Helper()
	f := &fakeKeys{evs: keys, clock: withClock(t)}
	var out bytes.Buffer
	yes, intr := askConfirm(f, &out, f.flush, "confirm unit.restart unit restart nginx? [y + Enter / N]")
	return yes, intr, out.String(), f
}

const human = 300 * time.Millisecond

func cat(parts ...[]ev) []ev {
	var out []ev
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestConfirmAnswers(t *testing.T) {
	cases := []struct {
		name string
		keys []ev
		yes  bool
		intr bool
	}{
		{"quiet, y, Enter", evs(human, "y\r"), true, false},
		{"quiet, Y, LF", evs(human, "Y\n"), true, false},
		{"Enter alone", evs(human, "\r"), false, false},
		{"y then another key", evs(human, "yy"), false, false},
		{"y then x", evs(human, "yx"), false, false},
		{"y, no Enter", evs(human, "y"), false, false},
		{"other first key", evs(human, "n\r"), false, false},
		{"Ctrl-C", evs(human, "\x03"), false, true},
		{"y then Ctrl-C", evs(human, "y\x03"), false, true},
		{"arrow key", evs(human, "\x1b[A"), false, false},
		{"bracketed paste", cat(evs(human, "\x1b"), evs(0, "[200~y\n\x1b[201~")), false, false},
		{"y and Enter together (the flush drops the Enter)", cat(evs(human, "y"), evs(0, "\r")), false, false},
		{"typeahead before the prompt", evs(0, "y\ry\r"), false, false},
		{"paste tail in the quiet window", cat(evs(50*time.Millisecond, "y"), evs(10*time.Millisecond, "\r")), false, false},
		{"paste tail, then Enter alone", cat(evs(50*time.Millisecond, "y"), []ev{{time.Second, '\r'}}), false, false},
		{"paste tail, then the person says y Enter", cat(evs(50*time.Millisecond, "y\r"), []ev{{time.Second, 'y'}, {human, '\r'}}), true, false},
		{"input never quiet", evs(100*time.Millisecond, strings.Repeat("y", 80)+"\r"), false, false},
		{"EOF", nil, false, false},
	}
	for _, c := range cases {
		yes, intr, out, f := ask(t, c.keys)
		if yes != c.yes || intr != c.intr {
			t.Errorf("%s: yes %v interrupted %v", c.name, yes, intr)
		}
		if !strings.HasPrefix(out, "\x1b[?2004h") || !strings.HasSuffix(out, "\x1b[?2004l") {
			t.Errorf("%s: bracketed paste not set and restored: %q", c.name, out)
		}
		if c.yes && (!strings.Contains(strings.ToLower(out), "[y + enter / n] y") || f.flushes != 2) {
			t.Errorf("%s: y not echoed or no second flush: %q flushes %d", c.name, out, f.flushes)
		}
	}
	_, _, out, _ := ask(t, evs(human, "\x1bx"))
	if strings.Contains(strings.TrimPrefix(out, "\x1b[?2004h"), "\x1bx") {
		t.Fatalf("echo is not escaped: %q", out)
	}
	f := &fakeKeys{evs: evs(human, "y\r"), clock: withClock(t)}
	if yes, _ := askConfirm(f, io.Discard, func() error { return errors.New("no") }, "q? [y + Enter / N]"); yes {
		t.Fatal("yes without a flush")
	}
}

// A pasted confirm off and a verb on the next line: confirm off asks, the
// rest of the paste meets the question and is a no, and nothing runs.
func TestConfirmOffPasteDoesNotClearTheLatch(t *testing.T) {
	for name, tail := range map[string][]ev{
		"together":             evs(0, "unit restart nginx\n"),
		"in the quiet window":  evs(20*time.Millisecond, "unit restart nginx\n"),
		"after the quiet time": cat([]ev{{time.Second, 'u'}}, evs(0, "nit restart nginx\n")),
	} {
		clock := withClock(t)
		p := &countParent{}
		e := New(srvGrant(), p)
		keys := &fakeKeys{evs: cat(evs(0, "confirm off\n"), tail), clock: clock}
		var out, errOut bytes.Buffer
		e.opt.Confirm = func(prompt string) (bool, bool) { return askConfirm(keys, &out, keys.flush, prompt) }
		lang.Run("set -o confirm", e.g, p, lang.Options{State: e.opt.State, Confirm: e.opt.Confirm})
		e.loop(keys, &out, &errOut)
		if !e.opt.State.Confirm() || p.n != 0 {
			t.Errorf("%s: confirm on %v calls %d", name, e.opt.State.Confirm(), p.n)
		}
	}
	// A person who means it: confirm off, a pause, y, Enter.
	clock := withClock(t)
	e := New(srvGrant(), &countParent{})
	keys := &fakeKeys{evs: cat(evs(0, "confirm off\n"), []ev{{human, 'y'}, {human, '\r'}}), clock: clock}
	var out bytes.Buffer
	e.opt.Confirm = func(prompt string) (bool, bool) { return askConfirm(keys, &out, keys.flush, prompt) }
	lang.Run("set -o confirm", e.g, &countParent{}, lang.Options{State: e.opt.State, Confirm: e.opt.Confirm})
	e.loop(keys, &out, io.Discard)
	if e.opt.State.Confirm() {
		t.Fatal("a confirmed confirm off left it on")
	}
}

// confirm off is an editor-only whole line, and it clears the latch only
// on a yes to its own question; no other form clears it.
func TestEditorConfirmOff(t *testing.T) {
	p := &countParent{}
	e := New(srvGrant(), p)
	var asked []string
	answer := false
	e.opt.Confirm = func(q string) (bool, bool) { asked = append(asked, q); return answer, false }
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
	if !e.opt.State.Confirm() || e.note != "confirm stays on" || asked[len(asked)-1] != "confirm off? [y + Enter / N]" {
		t.Fatalf("a no cleared it: on %v note %q asked %q", e.opt.State.Confirm(), e.note, asked)
	}
	answer = true
	e.Feed([]byte("confirm off\r"))
	if e.opt.State.Confirm() || e.Runs != runs || e.note != "confirm off" {
		t.Fatalf("confirm off: on %v note %q", e.opt.State.Confirm(), e.note)
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

// The real key source waits with a timeout and discards what has come.
func TestTTYSourceTimeoutAndDiscard(t *testing.T) {
	r, w := io.Pipe()
	s := newTTYSource(r)
	if _, ok, err := s.ReadByteTimeout(20 * time.Millisecond); ok || err != nil {
		t.Fatalf("timeout: ok %v err %v", ok, err)
	}
	go func() {
		if _, err := w.Write([]byte("ab")); err != nil {
			t.Error(err)
		}
	}()
	if b, ok, err := s.ReadByteTimeout(time.Second); !ok || err != nil || b != 'a' {
		t.Fatalf("read %q %v %v", b, ok, err)
	}
	if b, ok := s.Peek(); !ok || b != 'b' {
		t.Fatalf("peek %q %v", b, ok)
	}
	s.Discard()
	if _, ok := s.Peek(); ok {
		t.Fatal("discard kept a byte")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadByte(); err != io.EOF {
		t.Fatalf("eof %v", err)
	}
}

// Ctrl-C in the quiet window is not discarded: it is a no that stops the
// line.
func TestConfirmCtrlCInQuietWindow(t *testing.T) {
	for _, keys := range [][]ev{
		evs(50*time.Millisecond, "\x03"),
		cat(evs(10*time.Millisecond, "abc"), evs(10*time.Millisecond, "\x03")),
	} {
		if yes, intr, _, _ := ask(t, keys); yes || !intr {
			t.Fatalf("%v: yes %v interrupted %v", keys, yes, intr)
		}
	}
}

// The terminal guard restores once, however often it is called.
func TestTermGuardIdempotent(t *testing.T) {
	n := 0
	var out bytes.Buffer
	g := &termGuard{restore: func() error { n++; return nil }, out: &out}
	for i := 0; i < 3; i++ {
		if err := g.Restore(); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 || out.String() != "\x1b[?2004l" {
		t.Fatalf("restores %d out %q", n, out.String())
	}
}
