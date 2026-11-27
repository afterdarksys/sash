package lang

// The terminal a script may ask (docs/plans/2026-11-27-sash-platform-verbs.md
// section 1).
//
// Threats: a script that answers the platform's safety question for the
// person, typeahead or a paste that answers a question, an answer or a
// question that carries terminal control sequences, and a prompt that
// hangs an unattended run. Options.Term is set only by a caller that holds
// a real terminal on stdin and stderr and whose stdin is not the script;
// nil is no terminal, and every use then fails closed. Nothing here
// touches set -o confirm, plan mode, or their locks: an answer is a value
// and a status, never the answer to confirm mode. It does not protect a
// secret typed at a plain prompt, and it cannot stop host data in a
// question from telling the person what to type.

import (
	"errors"
	"time"
)

// TermAsk is one question for the person at the terminal.
type TermAsk struct {
	// Question is the text as the script built it. The asker escapes it
	// before it is written.
	Question string
	// Secret asks without echo.
	Secret bool
	// Wait bounds the read; zero is no bound.
	Wait time.Duration
	// Hung, when set, reports a hangup: the read then ends.
	Hung func() bool
}

// Term asks a person. Prompt returns one line without its Enter. Confirm
// is yes only for y or Y then Enter. Both return one of the ErrTerm
// errors when no answer was read.
type Term interface {
	Prompt(TermAsk) (string, error)
	Confirm(TermAsk) (bool, error)
}

var (
	// ErrTermNone is no terminal to ask, or one that could not be made
	// ready (a failed flush).
	ErrTermNone = errors.New("term needs a terminal")
	// ErrTermEOF is Ctrl-D on an empty line, or the end of input.
	ErrTermEOF = errors.New("end of input")
	// ErrTermInterrupted is Ctrl-C.
	ErrTermInterrupted = errors.New("interrupted")
	// ErrTermTimeout is the deadline passing while the person was asked.
	ErrTermTimeout = errors.New("timeout")
	// ErrTermHangup is a hangup while the person was asked.
	ErrTermHangup = errors.New("hangup")
	// ErrTermLong is an answer over TermAnswerMax bytes.
	ErrTermLong = errors.New("answer over 4096 bytes")
	// ErrTermNotText is an answer that is not UTF-8.
	ErrTermNotText = errors.New("answer is not text")
)

// TermAnswerMax is the longest answer term prompt takes, in bytes.
const TermAnswerMax = 4096

// isTTY answers is_tty(): true when a terminal can be asked. A recording
// pins the reading like the clock, and a replay answers from the tape
// without looking at any terminal.
func (m *machine) isTTY() (bool, error) {
	if m.tape == nil {
		return m.term != nil, nil
	}
	v := m.tape.value("tty", func() string {
		if m.term != nil {
			return "true"
		}
		return "false"
	})
	if err := m.tape.Err(); err != nil {
		m.ctl = ctlHalt
		return false, err
	}
	return v == "true", nil
}
