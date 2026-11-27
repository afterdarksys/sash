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

// msgTermNoTerminal refuses term without a terminal to ask.
const msgTermNoTerminal = "term needs a terminal"

// termQuestionMax caps a question after expansion, in bytes.
const termQuestionMax = 1024

// Outcomes of one question, as a recording stores them.
const (
	termOK          = ""
	termEOF         = "eof"
	termInterrupted = "interrupted"
	termTimeout     = "timeout"
	termHangup      = "hangup"
	termLong        = "long"
	termNotText     = "not_text"
	termNone        = "no_terminal"
)

func termCode(err error) string {
	switch {
	case err == nil:
		return termOK
	case errors.Is(err, ErrTermEOF):
		return termEOF
	case errors.Is(err, ErrTermInterrupted):
		return termInterrupted
	case errors.Is(err, ErrTermTimeout):
		return termTimeout
	case errors.Is(err, ErrTermHangup):
		return termHangup
	case errors.Is(err, ErrTermLong):
		return termLong
	case errors.Is(err, ErrTermNotText):
		return termNotText
	}
	// Anything else is a terminal that cannot be asked: fail closed.
	return termNone
}

// runTerm is term prompt "<question>" and term confirm "<question>". It
// asks through Options.Term, or answers from a recording on replay. It
// sets only its own value and status: m.declined, State.confirm, and the
// plan lock are never written here, so a yes is not the answer to confirm
// mode and the next mutating verb still asks its own question.
func (m *machine) runTerm(st expStage) (value, int) {
	fail := func(status int, msg string) (value, int) {
		m.err.WriteString(msg + "\n")
		return value{}, status
	}
	halt := func(status int, msg string) (value, int) {
		m.ctl = ctlHalt
		m.haltStatus = status
		return fail(status, msg)
	}
	op := st.argv[1]
	question := st.argv[len(st.argv)-1]
	secret := len(st.argv) == 4
	if secret {
		op = "prompt-s"
	}
	switch {
	case secret && (!m.secretPipe || !st.solo):
		// The parser allows -s only as the whole value of a declare -s;
		// this refuses to read when it is reached any other way.
		return fail(exitType, msgSecretPrompt)
	case m.pb != nil:
		// A branch has no terminal of its own: the parser refuses a
		// literal term in a par body, and this a call that reaches one.
		return fail(exitType, "term inside par")
	case m.atomic != nil:
		// A person at a prompt would let the agent's transaction deadline
		// restore the block.
		return fail(exitType, "atomic_irreversible: term")
	case m.st.HungUp():
		return halt(exitHangup, "hangup")
	case m.pastDeadline():
		return fail(exitType, "timeout")
	}
	ask := TermAsk{Question: question, Secret: secret, Hung: m.st.HungUp}
	if !m.deadline.IsZero() {
		// Waiting counts against timeout: the read ends at the deadline.
		if ask.Wait = m.deadline.Sub(m.now()); ask.Wait <= 0 {
			return fail(exitType, "timeout")
		}
	}
	live := func() (string, string) {
		if m.term == nil {
			return "", termNone
		}
		if op == "confirm" {
			yes, err := m.term.Confirm(ask)
			if err == nil && yes {
				return "yes", termOK
			}
			return "no", termCode(err)
		}
		answer, err := m.term.Prompt(ask)
		if err != nil {
			return "", termCode(err)
		}
		return answer, termOK
	}
	var answer, code string
	if m.tape != nil {
		var ok bool
		if secret {
			// The answer never reaches the tape: not its text, not its
			// length, not how the question ended.
			answer, code, ok = m.tape.termSecret(live)
		} else {
			answer, code, ok = m.tape.term(op, live)
		}
		if !ok {
			// Run reports the tape's error once, at the end.
			m.ctl = ctlHalt
			return value{}, exitType
		}
	} else {
		answer, code = live()
	}
	switch code {
	case termOK:
	case termEOF:
		// The end of input is not asked again by retry.
		m.termEnded = true
		if op == "confirm" {
			return value{kind: valBytes}, 1
		}
		m.err.WriteString("term prompt: end of input\n")
		return value{kind: valStr}, 1
	case termInterrupted:
		return halt(exitInterrupt, "term: interrupted")
	case termTimeout:
		return fail(exitType, "timeout")
	case termHangup:
		return halt(exitHangup, "hangup")
	case termLong:
		return fail(exitType, "term prompt: answer over 4096 bytes")
	case termNotText:
		return fail(exitType, "term prompt: answer is not text")
	default:
		return halt(exitType, msgTermNoTerminal)
	}
	if op == "confirm" {
		if answer == "yes" {
			return value{kind: valBytes}, 0
		}
		return value{kind: valBytes}, 1
	}
	// The answer is untrusted text a person typed: origin names it.
	id := m.st.addOrigin(origin{src: m.provSrcName(), line: m.provLine(), key: "term prompt", nohash: true})
	m.prov = addProv(m.prov, id)
	if secret {
		return value{kind: valData, d: secretVal{answer}}, 0
	}
	return value{kind: valStr, s: answer}, 0
}
