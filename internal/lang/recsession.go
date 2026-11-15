package lang

// In-session recording: set -o record <path> (docs/plans/2026-11-12-
// sash-v6-tier3.md section 4, "Where recordings live").
//
// Threats: a recording concentrates host data the actor could already
// read into one file. So it is off until a literal set -o record names a
// path under a file_write root (a file.write need that needs and lint
// report); it is held in memory, redacted against every secret the session
// bound, chained, and capped exactly as an offline recording; and it is
// written only through the agent's file.write with mode 0600, never by
// sash itself, never retried without the mode (an agent too old for the
// mode leaves it unwritten, with a clear error). Over recSegMax it is split
// into <path>.1 .. <path>.N, each written at 0600, each header chaining to
// the previous segment. Plan mode refuses to start one (plan writes
// nothing); confirm asks before each write like any file.write. A session
// killed before Finish writes nothing: the recording is lost, by design. If
// a later segment's write fails, the segments already written stay, and
// the error names how many were written; replay refuses the incomplete set.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// recSessionExpiry is a session recording's lifetime, as --record's
// default.
const recSessionExpiry = 7 * 24 * time.Hour

// sessionRec is a recording in progress: where it goes and its tape.
type sessionRec struct {
	path string
	tape *Tape
}

// runRecord is set -o record <path> and set +o record. The path is a
// literal word (prepare checked it).
func (m *machine) runRecord(argv []string) int {
	fail := func(status int, msg string) int {
		m.err.WriteString(msg + "\n")
		return status
	}
	switch {
	case len(argv) == 4 && argv[1] == "-o" && argv[2] == "record":
	case len(argv) == 3 && argv[1] == "+o" && argv[2] == "record":
		return m.finishRecord()
	default:
		return fail(exitType, "usage set -o record <path>, set +o record")
	}
	if m.pb != nil {
		return fail(exitType, "type record: not in a par branch")
	}
	if m.st.rec == nil && m.tape != nil {
		// An offline --record or --replay already holds this run.
		return 0
	}
	if m.st.rec != nil {
		return fail(exitType, "record: already recording")
	}
	if m.st.plan {
		return fail(exitType, "record: refused in plan mode (plan writes nothing)")
	}
	if !m.g.Has("file.write") {
		return fail(exitDenied, "denied file.write")
	}
	full, err := resolvePath(m.prefix(), argv[3])
	if err != nil {
		return fail(exitType, "prefix_unset")
	}
	if !m.g.AllowsWrite(full) {
		return fail(exitDenied, "denied file.write")
	}
	tape, err := NewRecorder(nil, m.src, nowFn().Add(recSessionExpiry))
	if err != nil {
		return fail(exitType, "record: "+err.Error())
	}
	tape.segMax = recSegMax
	tape.begin(m.text)
	tape.at = m.at
	m.st.rec = &sessionRec{path: full, tape: tape}
	m.tape = tape
	return 0
}

// finishRecord ends the session recording and writes it through
// file.write at 0600: set +o record, the end of a script, or logout.
func (m *machine) finishRecord() int {
	rec := m.st.rec
	if rec == nil {
		return 0
	}
	m.st.rec = nil
	if m.tape == rec.tape {
		m.tape = nil
	}
	fail := func(msg string) int {
		m.err.WriteString("record: " + msg + "\n")
		return exitType
	}
	// Every secret the session bound, up to the last statement, joins the
	// set Finish redacts against.
	rec.tape.see(m.secretTexts())
	if err := rec.tape.Finish(); err != nil {
		return fail("not written: " + err.Error())
	}
	segs := rec.tape.Segments()
	m.recWriting = true
	defer func() { m.recWriting = false }()
	for i, seg := range segs {
		p := rec.path
		if len(segs) > 1 {
			p += "." + strconv.Itoa(i+1)
		}
		// Exactly what a > redirect sends: the agent's file.write takes
		// argv [">", path] and nothing else.
		if _, err := m.do(parent.Verb{Key: "file.write", Argv: []string{">", p}, Path: p, Body: seg, Mode: "0600"}); err != nil {
			st := m.doFailed("file.write", err)
			if st < exitType {
				st = exitType
			}
			m.err.WriteString(fmt.Sprintf("record: not written: %d of %d segments written\n", i, len(segs)))
			return st
		}
		if m.st.plan {
			m.err.WriteString("record: not written (plan)\n")
			return exitType
		}
	}
	if rec.tape.Truncated() {
		m.err.WriteString("record: written truncated (it reached its size cap)\n")
	}
	return 0
}

// FinishRecording writes a session recording still open when a script
// ends or a session logs out. It asks under confirm like any file.write.
// With no recording it does nothing.
func FinishRecording(g grant.Grant, p parent.Parent, opt Options) Outcome {
	if opt.State == nil || opt.State.rec == nil {
		return Outcome{Prefix: opt.Prefix}
	}
	m := newMachine(g, p, opt.State)
	m.confirm = opt.Confirm
	st := m.finishRecord()
	if m.ctl == ctlHalt && m.haltStatus != 0 {
		st = m.haltStatus
	}
	return Outcome{Status: st, Stdout: m.out.String(), Stderr: safeText(m.err.String()), Prefix: opt.Prefix}
}

// Recording reports whether a session recording is open, for the prompt.
func (s *State) Recording() bool { return s != nil && s.rec != nil }
