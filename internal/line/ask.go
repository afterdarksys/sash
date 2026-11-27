package line

// The terminal a script asks: lang.Options.Term for the prompt (Loop) and
// for a script run at a terminal (ScriptTerm).
//
// Threats: typeahead or a paste answering for the person; a question or
// an answer driving the terminal with control sequences; a read that
// outlives a timeout or a hangup; a terminal left in raw mode. Before a
// question the input queue is flushed and what was read ahead is thrown
// away, and a failed flush asks nothing. The question and every echoed
// rune go through lang.TermLine and lang.TermText; control bytes are
// never stored. A secret is not echoed at all. In script mode raw mode
// lasts one question and is restored on every way out: SIGTERM, SIGINT
// and SIGQUIT restore it before the signal ends sash, and SIGTSTP restores
// it, stops, and enters raw mode again when continued.
// It does not protect a secret typed at a plain prompt: that is echoed
// and returned as ordinary text.

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/lang"
)

// askSlice is how often a read checks its deadline and for a hangup.
const askSlice = 50 * time.Millisecond

// watched is a keySource whose reads end at a deadline or a hangup. why
// keeps which one ended it.
type watched struct {
	keySource
	until time.Time
	hung  func() bool
	why   error
}

func (w *watched) check() error {
	if w.why != nil {
		return w.why
	}
	if w.hung != nil && w.hung() {
		w.why = lang.ErrTermHangup
	} else if !w.until.IsZero() && !nowFn().Before(w.until) {
		w.why = lang.ErrTermTimeout
	}
	return w.why
}

func (w *watched) ReadByte() (byte, error) {
	if w.until.IsZero() && w.hung == nil {
		return w.keySource.ReadByte()
	}
	for {
		if err := w.check(); err != nil {
			return 0, err
		}
		b, ok, err := w.keySource.ReadByteTimeout(askSlice)
		if err != nil {
			return 0, err
		}
		if ok {
			return b, nil
		}
	}
}

func (w *watched) ReadByteTimeout(d time.Duration) (byte, bool, error) {
	if w.until.IsZero() && w.hung == nil {
		return w.keySource.ReadByteTimeout(d)
	}
	for left := d; left > 0; left -= askSlice {
		if err := w.check(); err != nil {
			return 0, false, err
		}
		b, ok, err := w.keySource.ReadByteTimeout(min(left, askSlice))
		if err != nil || ok {
			return b, ok, err
		}
	}
	return 0, false, w.check()
}

func watch(r keySource, a lang.TermAsk) *watched {
	w := &watched{keySource: r, hung: a.Hung}
	if a.Wait > 0 {
		w.until = nowFn().Add(a.Wait)
	}
	return w
}

// askPrompt is term prompt's read: the question, then one line. Typeahead
// never answers: the input queue is flushed and what was read ahead is
// dropped first, and a failed flush is no terminal. After the question,
// input is dropped until it has been quiet for confirmQuiet (waitQuiet),
// and input that is never quiet for confirmQuietMax is no terminal. Enter (CR or LF) ends
// the line. Backspace deletes one rune and Ctrl-U the line. ESC and its
// sequence (a CSI through its final byte, else ESC and one byte), every
// other control byte, and C1 controls are dropped, never stored. Each
// stored rune is echoed through lang.TermText, or not at all for a
// secret. Past lang.TermAnswerMax bytes the rest is discarded up to Enter
// and there is no value. Ctrl-D on an empty line or the end of input is
// lang.ErrTermEOF, Ctrl-C lang.ErrTermInterrupted. lead is written before
// the question (the prompt's own line break).
func askPrompt(r keySource, out io.Writer, flush func() error, lead string, a lang.TermAsk) (answer string, err error) {
	if flush == nil || flush() != nil {
		return "", lang.ErrTermNone
	}
	r.Discard()
	w := watch(r, a)
	if werr := writeAll(out, []byte(lead+lang.TermLine(a.Question)+" ")); werr != nil {
		return "", lang.ErrTermNone
	}
	defer func() {
		if werr := writeAll(out, []byte("\r\n")); werr != nil && err == nil {
			answer, err = "", lang.ErrTermNone
		}
	}()
	type cell struct{ bytes, cols int }
	var (
		buf   []byte
		cells []cell
		pend  []byte
		over  bool
	)
	erase := func(n int) error {
		for ; n > 0; n-- {
			if werr := writeAll(out, []byte("\b \b")); werr != nil {
				return werr
			}
		}
		return nil
	}
	// store keeps one rune, or one byte that is not UTF-8, and echoes it.
	store := func(p []byte) error {
		if over {
			return nil
		}
		if len(buf)+len(p) > lang.TermAnswerMax {
			over = true
			return nil
		}
		buf = append(buf, p...)
		shown := ""
		if !a.Secret {
			shown = lang.TermText(string(p))
		}
		cells = append(cells, cell{len(p), utf8.RuneCountInString(shown)})
		return writeAll(out, []byte(shown))
	}
	// flushPend stores bytes that never became a rune, one by one.
	flushPend := func() error {
		for _, b := range pend {
			if werr := store([]byte{b}); werr != nil {
				return werr
			}
		}
		pend = pend[:0]
		return nil
	}
	fail := func(e error) (string, error) {
		if errors.Is(e, io.EOF) {
			return "", lang.ErrTermEOF
		}
		if errors.Is(e, lang.ErrTermTimeout) || errors.Is(e, lang.ErrTermHangup) {
			return "", e
		}
		return "", lang.ErrTermNone
	}
	// The question arms as a confirm does: keys are dropped until input
	// has been quiet for confirmQuiet, so the rest of an earlier answer,
	// or a key typed before the question could be read, does not answer
	// it. Input that never goes quiet is no answer.
	switch end, qerr := waitQuiet(w); {
	case qerr != nil:
		return fail(qerr)
	case end == quietCtrlC:
		return "", lang.ErrTermInterrupted
	case end == quietBusy:
		return "", lang.ErrTermNone
	}
	for {
		b, rerr := w.ReadByte()
		if rerr != nil {
			return fail(rerr)
		}
		switch {
		case b == '\r' || b == '\n':
			if werr := flushPend(); werr != nil {
				return "", lang.ErrTermNone
			}
			if over {
				return "", lang.ErrTermLong
			}
			if !utf8.Valid(buf) {
				return "", lang.ErrTermNotText
			}
			return string(buf), nil
		case b == 0x03:
			return "", lang.ErrTermInterrupted
		case b == 0x04 && len(buf) == 0 && len(pend) == 0 && !over:
			return "", lang.ErrTermEOF
		case b == 0x7f || b == 0x08:
			if werr := flushPend(); werr != nil {
				return "", lang.ErrTermNone
			}
			if n := len(cells); n > 0 && !over {
				c := cells[n-1]
				cells, buf = cells[:n-1], buf[:len(buf)-c.bytes]
				if werr := erase(c.cols); werr != nil {
					return "", lang.ErrTermNone
				}
			}
		case b == 0x15:
			pend = pend[:0]
			if !over {
				cols := 0
				for _, c := range cells {
					cols += c.cols
				}
				cells, buf = cells[:0], buf[:0]
				if werr := erase(cols); werr != nil {
					return "", lang.ErrTermNone
				}
			}
		case b == 0x1b:
			// ESC and its sequence are dropped: a CSI through its final
			// byte, anything else as ESC and one byte.
			next, rerr := w.ReadByte()
			if rerr != nil {
				return fail(rerr)
			}
			for next == '[' {
				c, rerr := w.ReadByte()
				if rerr != nil {
					return fail(rerr)
				}
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
		case b < 0x20:
			// Any other control byte is dropped.
		default:
			pend = append(pend, b)
			if !utf8.FullRune(pend) && len(pend) < utf8.UTFMax {
				continue
			}
			rn, size := utf8.DecodeRune(pend)
			if rn == utf8.RuneError && size <= 1 {
				// Not UTF-8: kept as it came, so Enter refuses the answer.
				if werr := flushPend(); werr != nil {
					return "", lang.ErrTermNone
				}
				continue
			}
			p := append([]byte(nil), pend[:size]...)
			pend = pend[:0]
			if rn >= 0x80 && rn <= 0x9f {
				continue // a C1 control is dropped
			}
			if werr := store(p); werr != nil {
				return "", lang.ErrTermNone
			}
		}
	}
}

// askYes is term confirm's read: the whole askConfirm discipline, with
// the read bounded by the deadline and a hangup. A no, the end of input,
// and a failed flush are all no.
func askYes(r keySource, out io.Writer, flush func() error, a lang.TermAsk) (bool, error) {
	w := watch(r, a)
	yes, interrupted := askConfirm(w, out, flush, lang.TermLine(a.Question)+" [y + Enter / N]")
	switch {
	case w.why != nil:
		return false, w.why
	case interrupted:
		return false, lang.ErrTermInterrupted
	}
	return yes, nil
}

// asker is lang.Options.Term at a real terminal. raw is nil at the
// prompt, which is in raw mode already; in script mode it enters raw mode
// for one question.
type asker struct {
	mu    sync.Mutex
	in    *os.File
	out   io.Writer
	keys  keySource
	lead  string
	raw   func(fd int) (func() error, error)
	flush func() error
	// lost is set when raw mode could not be entered again after SIGTSTP.
	lostMu sync.Mutex
	lost   error
}

func (k *asker) ready() (done func() error, err error) {
	if k.keys == nil {
		k.keys = newTTYSource(k.in)
	}
	k.lost = nil
	if k.raw == nil {
		return func() error { return nil }, nil
	}
	fd := int(k.in.Fd())
	restore, err := k.raw(fd)
	if err != nil {
		return nil, err
	}
	// A confirm question may leave bracketed paste on; the guard turns it
	// off and restores the mode on SIGTERM, SIGINT and SIGQUIT (sent with
	// kill: in raw mode Ctrl-C is a key) before the signal's own end. On
	// SIGTSTP it restores the mode, the process stops, and when continued
	// raw mode is entered again; if that fails the question fails closed.
	// SIGHUP is the run's own hangup path: the read ends and this returns
	// normally.
	guard := &rawGuard{fd: fd, raw: k.raw, restore: restore, out: k.out}
	stop := watchSignalsOf(guard.Restore, reraise, askEnds...)
	stopTSTP := watchStop(guard.Restore, func() {
		if rerr := guard.Resume(); rerr != nil {
			k.lostMu.Lock()
			k.lost = rerr
			k.lostMu.Unlock()
		}
	})
	return func() error {
		stopTSTP()
		stop()
		return guard.Restore()
	}, nil
}

// lostRaw is the error of re-entering raw mode after a stop, if any.
func (k *asker) lostRaw() error {
	k.lostMu.Lock()
	defer k.lostMu.Unlock()
	return k.lost
}

// rawGuard holds a question's raw mode: Restore puts the terminal back
// (bracketed paste off first), once until Resume enters raw mode again.
type rawGuard struct {
	mu      sync.Mutex
	fd      int
	raw     func(fd int) (func() error, error)
	restore func() error // nil while restored
	out     io.Writer
	err     error // the last Restore's
}

func (g *rawGuard) Restore() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.restore == nil {
		return g.err
	}
	werr := writeAll(g.out, []byte("\x1b[?2004l"))
	g.err = g.restore()
	g.restore = nil
	if g.err == nil {
		g.err = werr
	}
	return g.err
}

func (g *rawGuard) Resume() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.restore != nil {
		return nil
	}
	restore, err := g.raw(g.fd)
	if err != nil {
		return err
	}
	g.restore = restore
	return nil
}

func (k *asker) Prompt(a lang.TermAsk) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	done, err := k.ready()
	if err != nil {
		return "", lang.ErrTermNone
	}
	answer, err := askPrompt(k.keys, k.out, k.flush, k.lead, a)
	if derr := done(); derr != nil {
		return "", k.notRestored()
	}
	if k.lostRaw() != nil {
		// Raw mode could not be entered again after a stop: what was
		// read since is not trusted as an answer.
		return "", lang.ErrTermNone
	}
	return answer, err
}

func (k *asker) Confirm(a lang.TermAsk) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	done, err := k.ready()
	if err != nil {
		return false, lang.ErrTermNone
	}
	yes, err := askYes(k.keys, k.out, k.flush, a)
	if derr := done(); derr != nil {
		return false, k.notRestored()
	}
	if k.lostRaw() != nil {
		return false, lang.ErrTermNone
	}
	return yes, err
}

// notRestored says the terminal mode could not be put back, and fails
// the question closed.
func (k *asker) notRestored() error {
	if err := writeAll(k.out, []byte("sash: terminal not restored\n")); err != nil {
		return lang.ErrTermNone
	}
	return lang.ErrTermNone
}

// ScriptTerm is lang.Options.Term for a script that is not on stdin (sash
// -c, or a file): the person at in and errOut, when both are terminals.
// Otherwise it is nil, and is_tty() is false. The terminal is put in raw
// mode for one question at a time and restored after each.
func ScriptTerm(in, errOut *os.File) lang.Term {
	if !IsTerm(in) || !IsTerm(errOut) {
		return nil
	}
	return &asker{in: in, out: errOut, raw: enterRaw, flush: func() error { return flushInput(int(in.Fd())) }}
}
