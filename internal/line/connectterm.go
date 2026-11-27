package line

import (
	"io"
	"os"
)

// What sash connect needs of the terminal: its size, raw mode behind the
// same once-only guard Loop uses, and the way a process ends by a signal.

// TermSize is the terminal's rows and columns; an unknown size is 24 by
// 80.
func TermSize(f *os.File) (rows, cols int) {
	if f == nil {
		return 24, 80
	}
	rows, cols = termSize(int(f.Fd()))
	if rows <= 0 || cols <= 0 {
		return 24, 80
	}
	return rows, cols
}

// RawMode puts in into raw mode. restore puts the mode back and turns
// bracketed paste off on out; it acts once, and every later call returns
// the first call's result.
func RawMode(in *os.File, out io.Writer) (restore func() error, err error) {
	old, err := enterRaw(int(in.Fd()))
	if err != nil {
		return nil, err
	}
	guard := &termGuard{restore: old, out: out}
	return guard.Restore, nil
}

// EndBySignal ends the process as sig says (128 plus its number when the
// signal itself does not end it), after saying on stderr when the
// terminal could not be restored.
func EndBySignal(sig os.Signal, restoreErr error) { reraise(sig, restoreErr) }
