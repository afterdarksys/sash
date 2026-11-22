package lang

import "strings"

// $last and capture (v7 contract item 3). sash has no file descriptors: a
// verb answers with output, an exit status and an error message, so the
// "stderr" of a statement is its error message and the per-stage statuses
// replace ${PIPESTATUS[@]}.
// Threats: a secret reaching $last or a capture map (messages are the same
// redacted text $e.message carries; a capture of a secret value is secret);
// capture hiding a refusal (a grant denial, a decline, a secret refusal, a
// cap, an unknown name or a hangup is not captured: it fails as it would
// without capture); a script rebinding last (it is read-only). Does NOT
// capture a function body's own statements separately, only the pipeline.

// noteStages records a pipeline's per-stage statuses and verb keys.
func (m *machine) noteStages(exp []expStage, exits []int) {
	m.stages = append(m.stages[:0], exits...)
	m.stageKeys = m.stageKeys[:0]
	for _, st := range exp {
		key := st.key
		if key == "" && len(st.argv) > 0 {
			key = st.argv[0]
		}
		m.stageKeys = append(m.stageKeys, key)
	}
}

// lastMap is the statement summary bound as $last and returned by capture.
func (m *machine) lastMap(status int) *omap {
	o := newOmap()
	o.set("status", int64(status))
	stages := make([]any, 0, len(m.stages))
	failed := any(nil)
	for i, st := range m.stages {
		stages = append(stages, int64(st))
		if st != 0 && failed == nil {
			failed = int64(i)
		}
	}
	keys := make([]any, 0, len(m.stageKeys))
	for _, k := range m.stageKeys {
		keys = append(keys, k)
	}
	o.set("stages", stages)
	o.set("keys", keys)
	o.set("failed_stage", failed)
	code, message := "", ""
	if status != 0 && m.failure != nil {
		code, message = m.failure.Code, m.failure.Message
	}
	o.set("code", code)
	o.set("message", message)
	return o
}

// setLast binds $last after a statement.
func (m *machine) setLast(status int) {
	m.vars["last"] = value{kind: valData, d: m.lastMap(status)}
}

// capturable is whether capture may hold a failure of this code as data. A
// refusal fails as it would without capture.
func capturable(code string) bool {
	switch code {
	case "command_failed", "type_error", "timeout", "invalid_argument", "parent_error", "runtime_error":
		return true
	}
	return false
}

// runCapture runs let x = capture { pipeline }: x is a map of out, err,
// status, stages, keys, code and failed_stage, and an ordinary failure
// does not fail the statement.
func (m *machine) runCapture(s *Let) (int, bool) {
	mark := m.err.Len()
	m.stages, m.stageKeys = nil, nil
	m.failure = nil
	st, stop, v := m.runPipe(*s.Pipe)
	errText := m.err.String()[mark:]
	if st != 0 && m.failure == nil {
		m.noteFailure(st, errText)
	}
	// A failure of the data (bad json, a type error, a failed command) is
	// held; a halt (a cap, a hangup, await refusals) or a refusal is not.
	if st != 0 && (m.failure == nil || !capturable(m.failure.Code)) {
		return st, stop
	}
	if stop && m.ctl != ctlNone {
		return st, stop
	}
	// The error text belongs to the map now, not to stderr.
	whole := m.err.String()
	m.err.Reset()
	m.err.WriteString(whole[:mark])
	o := m.lastMap(st)
	out := any(nil)
	switch v.kind {
	case valBytes:
		out = string(v.b)
	default:
		out = toDatum(v)
	}
	o.set("out", out)
	o.set("err", safeText(strings.TrimSpace(errText)))
	var d any = o
	if isSecret(toDatum(v)) {
		d = secretVal{d: o}
	}
	return m.bindLet(s.Name, value{kind: valData, d: d})
}
