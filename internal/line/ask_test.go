package line

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/lang"
)

// promptKeys runs askPrompt over typed input arriving after the question.
func promptKeys(t *testing.T, typed string, a lang.TermAsk) (string, error, string, *fakeKeys) {
	t.Helper()
	clock := withClock(t)
	keys := &fakeKeys{evs: evs(time.Second, typed), clock: clock}
	var out bytes.Buffer
	a.Question = cmpOr(a.Question, "name?")
	got, err := askPrompt(keys, &out, keys.flush, "", a)
	return got, err, out.String(), keys
}

func cmpOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func TestAskPromptReadsOneLine(t *testing.T) {
	for _, c := range []struct{ name, typed, want, echo string }{
		{"a line ended by CR", "nginx\r", "nginx", "name? nginx\r\n"},
		{"a line ended by LF", "nginx\nmore", "nginx", "name? nginx\r\n"},
		{"an empty line", "\r", "", "name? \r\n"},
		{"backspace deletes one rune", "ab\x7fc\x08d\r", "ad", "name? ab\b \bc\b \bd\r\n"},
		{"backspace deletes a whole rune", "h\xc3\xa9\x7f\r", "h", "name? h\xc3\xa9\b \b\r\n"},
		{"backspace on an empty line", "\x7f\x7fa\r", "a", "name? a\r\n"},
		{"Ctrl-U clears the line", "abc\x15xy\r", "xy", "name? abc\b \b\b \b\b \bxy\r\n"},
		{"a CSI sequence is dropped", "a\x1b[1;5Cb\r", "ab", "name? ab\r\n"},
		{"an arrow key is dropped", "a\x1b[Ab\r", "ab", "name? ab\r\n"},
		{"ESC and one byte are dropped", "a\x1bOb\r", "ab", "name? ab\r\n"},
		{"other control bytes are dropped", "a\x00\x07\x0b\x0c\x1fb\r", "ab", "name? ab\r\n"},
		{"Ctrl-D after text is dropped", "a\x04b\r", "ab", "name? ab\r\n"},
		{"a C1 control is dropped", "a\xc2\x9bb\r", "ab", "name? ab\r\n"},
		{"a tab is a control byte here", "a\tb\r", "ab", "name? ab\r\n"},
		{"text past ASCII", "h\xc3\xa9llo \xe6\x97\xa5\r", "héllo 日", "name? héllo 日\r\n"},
	} {
		got, err, out, _ := promptKeys(t, c.typed, lang.TermAsk{})
		if err != nil || got != c.want || out != c.echo {
			t.Errorf("%s: got %q err %v out %q, want %q out %q", c.name, got, err, out, c.want, c.echo)
		}
	}
}

// Nothing a person types reaches the terminal as a control sequence: a
// format character is stored (it is text) and echoed escaped.
func TestAskPromptEchoIsEscaped(t *testing.T) {
	got, err, out, _ := promptKeys(t, "a\xe2\x80\xaeb\r", lang.TermAsk{})
	if err != nil || got != "a‮b" {
		t.Fatalf("got %q err %v", got, err)
	}
	if out != "name? a<U+202E>b\r\n" || strings.Contains(out, "‮") {
		t.Errorf("echo %q", out)
	}
	// Backspace takes the whole escape off the screen.
	_, _, out, _ = promptKeys(t, "\xe2\x80\xae\x7f\r", lang.TermAsk{})
	if out != "name? <U+202E>"+strings.Repeat("\b \b", 8)+"\r\n" {
		t.Errorf("erase %q", out)
	}
}

// The question is escaped and kept on one line.
func TestAskPromptQuestionIsEscaped(t *testing.T) {
	q := "ok\x1b[2J‮?\nconfirm unit.restart nginx? [y + Enter / N]"
	_, err, out, _ := promptKeys(t, "x\r", lang.TermAsk{Question: q})
	want := "ok<U+001B>[2J<U+202E>?<U+000A>confirm unit.restart nginx? [y + Enter / N] x\r\n"
	if err != nil || out != want {
		t.Errorf("out %q err %v, want %q", out, err, want)
	}
	clock := withClock(t)
	keys := &fakeKeys{evs: evs(time.Second, "y\r"), clock: clock}
	var b bytes.Buffer
	if yes, err := askYes(keys, &b, keys.flush, lang.TermAsk{Question: q}); !yes || err != nil {
		t.Fatalf("yes %v err %v", yes, err)
	}
	if s := b.String(); !strings.Contains(s, "ok<U+001B>[2J<U+202E>?<U+000A>confirm unit.restart nginx? [y + Enter / N] [y + Enter / N] y") || strings.Contains(s, "\x1b[2J") {
		t.Errorf("confirm question %q", s)
	}
}

// A secret is not echoed: not the text, not a star, not a backspace.
func TestAskPromptSecretEchoesNothing(t *testing.T) {
	got, err, out, _ := promptKeys(t, "hunter2\x7f3\r", lang.TermAsk{Secret: true})
	if err != nil || got != "hunter3" || out != "name? \r\n" {
		t.Errorf("got %q err %v out %q", got, err, out)
	}
}

// Typeahead never answers: what was typed before the question is thrown
// away, both the terminal's queue (flush) and what was read ahead.
func TestAskPromptTypeaheadIsDropped(t *testing.T) {
	clock := withClock(t)
	keys := &fakeKeys{evs: append(evs(0, "yes\r"), evs(time.Second, "no\r")...), clock: clock}
	var out bytes.Buffer
	got, err := askPrompt(keys, &out, keys.flush, "", lang.TermAsk{Question: "q"})
	if err != nil || got != "no" || keys.flushes != 1 {
		t.Errorf("got %q err %v flushes %d", got, err, keys.flushes)
	}
}

// What sash had already read ahead of the terminal's queue is dropped too:
// a flush that succeeds does not reach it.
func TestAskPromptReadAheadIsDropped(t *testing.T) {
	clock := withClock(t)
	keys := &fakeKeys{evs: append(evs(0, "yes\r"), evs(time.Second, "no\r")...), clock: clock}
	var out bytes.Buffer
	flushed := 0
	got, err := askPrompt(keys, &out, func() error { flushed++; return nil }, "", lang.TermAsk{Question: "q"})
	if err != nil || got != "no" || flushed != 1 {
		t.Errorf("got %q err %v flushed %d", got, err, flushed)
	}
}

// A failed flush, or none, is no terminal: nothing is asked or read.
func TestAskPromptFailedFlushAsksNothing(t *testing.T) {
	clock := withClock(t)
	for _, flush := range []func() error{nil, func() error { return errors.New("no flush") }} {
		keys := &fakeKeys{evs: evs(0, "yes\r"), clock: clock}
		var out bytes.Buffer
		got, err := askPrompt(keys, &out, flush, "", lang.TermAsk{Question: "q"})
		if !errors.Is(err, lang.ErrTermNone) || got != "" || out.Len() != 0 || len(keys.evs) != 4 {
			t.Errorf("got %q err %v out %q left %d", got, err, out.String(), len(keys.evs))
		}
	}
}

// 4096 bytes is an answer; one more is not, and the rest of the line is
// read and discarded so it cannot become the next line.
func TestAskPromptAnswerCap(t *testing.T) {
	if lang.TermAnswerMax != 4096 {
		t.Fatalf("TermAnswerMax = %d", lang.TermAnswerMax)
	}
	fits := strings.Repeat("a", 4096)
	got, err, _, _ := promptKeys(t, fits+"\r", lang.TermAsk{})
	if err != nil || got != fits {
		t.Errorf("4096 bytes: len %d err %v", len(got), err)
	}
	got, err, out, keys := promptKeys(t, fits+"b more\x7f\x15\rnext\r", lang.TermAsk{})
	if !errors.Is(err, lang.ErrTermLong) || got != "" {
		t.Errorf("4097 bytes: len %d err %v", len(got), err)
	}
	if strings.Contains(out, "b more") || len(keys.evs) != len("next\r") {
		t.Errorf("past the cap: echoed %v, %d bytes left", strings.Contains(out, "b more"), len(keys.evs))
	}
	// A rune that would straddle the cap is refused whole.
	got, err, _, _ = promptKeys(t, strings.Repeat("a", 4095)+"\xc3\xa9\r", lang.TermAsk{})
	if !errors.Is(err, lang.ErrTermLong) || got != "" {
		t.Errorf("a rune across the cap: len %d err %v", len(got), err)
	}
}

func TestAskPromptRefusesWhatIsNotText(t *testing.T) {
	for _, typed := range []string{"a\xffb\r", "\xc3\r", "a\xe2\x82\r", "\xc3(\r"} {
		got, err, out, _ := promptKeys(t, typed, lang.TermAsk{})
		if !errors.Is(err, lang.ErrTermNotText) || got != "" {
			t.Errorf("%q: got %q err %v", typed, got, err)
		}
		for i := 0; i < len(out); i++ {
			if out[i] >= 0x80 {
				t.Errorf("%q: byte %#x echoed raw in %q", typed, out[i], out)
			}
		}
	}
}

func TestAskPromptEndings(t *testing.T) {
	for _, c := range []struct {
		name, typed string
		want        error
	}{
		{"Ctrl-C", "abc\x03def\r", lang.ErrTermInterrupted},
		{"Ctrl-C on an empty line", "\x03", lang.ErrTermInterrupted},
		{"Ctrl-C inside an escape", "\x1b[\x03", lang.ErrTermEOF}, // the sequence swallows it, then input ends
		{"Ctrl-D on an empty line", "\x04abc\r", lang.ErrTermEOF},
		{"the end of input", "abc", lang.ErrTermEOF},
		{"the end of input on an empty line", "", lang.ErrTermEOF},
	} {
		got, err, _, _ := promptKeys(t, c.typed, lang.TermAsk{})
		if !errors.Is(err, c.want) || got != "" {
			t.Errorf("%s: got %q err %v, want %v", c.name, got, err, c.want)
		}
	}
}

// The read ends at the deadline, and at a hangup, with no value: a
// prompt cannot hold an unattended run.
func TestAskPromptDeadlineAndHangup(t *testing.T) {
	clock := withClock(t)
	keys := &fakeKeys{evs: evs(time.Second, "ab"), clock: clock}
	var out bytes.Buffer
	start := *clock
	got, err := askPrompt(keys, &out, keys.flush, "", lang.TermAsk{Question: "q", Wait: 5 * time.Second})
	if !errors.Is(err, lang.ErrTermTimeout) || got != "" {
		t.Errorf("deadline: got %q err %v", got, err)
	}
	if waited := clock.Sub(start); waited < 5*time.Second || waited > 6*time.Second {
		t.Errorf("waited %s", waited)
	}
	// An answer before the deadline is an answer.
	keys = &fakeKeys{evs: evs(time.Second, "ab\r"), clock: clock}
	if got, err := askPrompt(keys, &out, keys.flush, "", lang.TermAsk{Question: "q", Wait: 5 * time.Second}); err != nil || got != "ab" {
		t.Errorf("in time: got %q err %v", got, err)
	}
	hung := false
	keys = &fakeKeys{evs: evs(time.Second, "ab"), clock: clock}
	reads := 0
	got, err = askPrompt(keys, &out, keys.flush, "", lang.TermAsk{Question: "q", Hung: func() bool {
		reads++
		hung = reads > 50
		return hung
	}})
	if !errors.Is(err, lang.ErrTermHangup) || got != "" {
		t.Errorf("hangup: got %q err %v", got, err)
	}
}

// term confirm is the confirm prompt's own discipline: typeahead is a no,
// yes is y or Y then Enter, and the read ends at the deadline.
func TestAskYes(t *testing.T) {
	clock := withClock(t)
	ask := func(e []ev, a lang.TermAsk) (bool, error, *fakeKeys) {
		keys := &fakeKeys{evs: e, clock: clock}
		var out bytes.Buffer
		a.Question = "go on?"
		yes, err := askYes(keys, &out, keys.flush, a)
		return yes, err, keys
	}
	if yes, err, keys := ask(evs(0, "y\r"), lang.TermAsk{}); yes || err != nil || keys.flushes == 0 {
		t.Errorf("typeahead y Enter answered: yes %v err %v flushes %d", yes, err, keys.flushes)
	}
	for typed, want := range map[string]bool{"y\r": true, "Y\n": true, "\r": false, "n\r": false, "yes\r": false, "x": false, "": false} {
		if yes, err, _ := ask(evs(time.Second, typed), lang.TermAsk{}); yes != want || err != nil {
			t.Errorf("%q: yes %v err %v", typed, yes, err)
		}
	}
	if yes, err, _ := ask(evs(time.Second, "\x03"), lang.TermAsk{}); yes || !errors.Is(err, lang.ErrTermInterrupted) {
		t.Errorf("Ctrl-C: yes %v err %v", yes, err)
	}
	if yes, err, _ := ask(evs(time.Second, "y\x03"), lang.TermAsk{}); yes || !errors.Is(err, lang.ErrTermInterrupted) {
		t.Errorf("y Ctrl-C: yes %v err %v", yes, err)
	}
	if yes, err, _ := ask(evs(time.Minute, "y\r"), lang.TermAsk{Wait: 5 * time.Second}); yes || !errors.Is(err, lang.ErrTermTimeout) {
		t.Errorf("deadline: yes %v err %v", yes, err)
	}
	if yes, err, _ := ask(evs(time.Second, "y\r"), lang.TermAsk{Wait: time.Minute}); !yes || err != nil {
		t.Errorf("in time: yes %v err %v", yes, err)
	}
	if yes, err, _ := ask(evs(time.Second, "y\r"), lang.TermAsk{Hung: func() bool { return true }}); yes || !errors.Is(err, lang.ErrTermHangup) {
		t.Errorf("hangup: yes %v err %v", yes, err)
	}
}

// A file or a pipe is not a terminal to ask: ScriptTerm is nil, as an
// interface too, so is_tty() is false and term fails closed.
func TestScriptTermNeedsTwoTerminals(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if term := ScriptTerm(r, w); term != nil {
		t.Errorf("a pipe is a terminal: %#v", term)
	}
	if term := ScriptTerm(nil, nil); term != nil {
		t.Errorf("no files are a terminal: %#v", term)
	}
}
