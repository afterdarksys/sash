package lang

// defer and finally (v7 contract item 2), in place of bash's trap.
// Threats: cleanup skipped on an error path (a defer runs when its function
// or the script ends, whichever way: success, return, exit, an error, set -e,
// a cap; finally runs after the body and any catch on every way out); a
// cleanup that itself fails hiding the first failure (the defers all run,
// and the statement keeps the first failure's status; a failing finally
// replaces the status, as in Go and Python, which lint reports); a cleanup
// slipping past the grant (each body is ordinary statements, checked,
// planned and denied like any other); unbounded growth (at most deferMax
// pending defers per frame, exit 2 defer max). Does NOT run defers after a
// hangup: a hung-up session stops at its next statement, defer bodies
// included. There are no signal handlers.

const deferMax = 64

// pushDefer records a defer body on the innermost frame.
func (m *machine) pushDefer(body *Program) (int, bool) {
	if len(m.defers) == 0 {
		m.defers = [][]*Program{nil}
	}
	top := len(m.defers) - 1
	if len(m.defers[top]) >= deferMax {
		m.err.WriteString("defer max\n")
		return exitType, true
	}
	m.defers[top] = append(m.defers[top], body)
	return 0, false
}

// runDefers pops the innermost frame and runs its bodies last first. status
// and stop are the frame's own; the frame keeps its first failure.
func (m *machine) runDefers(status int, stop bool) int {
	if len(m.defers) == 0 {
		return status
	}
	top := len(m.defers) - 1
	frame := m.defers[top]
	m.defers = m.defers[:top]
	if len(frame) == 0 {
		return status
	}
	saved := m.ctl
	failed := status != 0 || stop
	for i := len(frame) - 1; i >= 0; i-- {
		if m.st.HungUp() {
			break
		}
		if saved == ctlHalt || m.ctl == ctlHalt {
			// A cap ended the frame: its defers still run, but a defer
			// cannot clear the cap.
			m.ctl = ctlNone
		} else {
			m.ctl = ctlNone
		}
		st, dstop := m.runProg(frame[i])
		if (st != 0 || dstop) && !failed {
			failed = true
			status = st
			if status == 0 {
				status = exitType
			}
		}
	}
	if saved != ctlNone {
		m.ctl = saved
	} else if m.ctl == ctlReturn || m.ctl == ctlBreak || m.ctl == ctlContinue {
		m.ctl = ctlNone
	}
	return status
}

// runFinally runs t.Finally after the body or catch that ended with status
// and stop. A finally that fails replaces them; one that succeeds keeps
// them, the pending return, exit or cap included.
func (m *machine) runFinally(t *Try, status int, stop bool) (int, bool) {
	if t.Finally == nil {
		return status, stop
	}
	if m.st.HungUp() {
		return status, stop
	}
	saved := m.ctl
	m.ctl = ctlNone
	st, fstop := m.runProg(t.Finally)
	if st != 0 || fstop {
		if m.ctl == ctlNone && saved == ctlHalt {
			m.ctl = ctlHalt
		}
		return st, fstop
	}
	m.ctl = saved
	return status, stop
}
