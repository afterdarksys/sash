package lang

// pipeFn captures a function's stdout as pipeline data. An explicit return
// keeps its type; mixing stdout and a return is refused instead of losing data.
func (m *machine) pipeFn(st expStage, in value) (value, int) {
	if !m.bindable("input") {
		return value{}, exitType
	}
	for _, name := range st.call.Params {
		if name == "input" {
			m.err.WriteString("usage fn: input is reserved in a pipeline\n")
			return value{}, exitType
		}
	}
	if containsSecret(toDatum(in)) {
		m.err.WriteString("a secret cannot leave sash\n")
		return value{}, exitType
	}
	atCond := m.polling && m.depth == m.pollDepth
	saved := m.saveVars([]string{"input"})
	m.vars["input"] = in
	defer m.restoreVars(saved)
	// Do not copy a nonzero strings.Builder: save its text instead.
	outer := m.out.String()
	outerTaint := m.outTaint
	m.out.Reset()
	m.outTaint = false
	code, stop := m.callFn(st)
	output := m.out.String()
	redacted := m.outTaint
	m.out.Reset()
	m.out.WriteString(outer)
	m.outTaint = outerTaint
	if stop && m.ctl == ctlNone && code == 0 {
		code = exitType
	}
	if m.retVal != nil {
		v := *m.retVal
		m.retVal = nil
		if output != "" {
			m.err.WriteString("type fn: cannot mix stdout and return in a pipeline\n")
			return value{}, exitType
		}
		// Match the existing restriction on secret function arguments: a return
		// must not smuggle a secret into a later stage or a redirect.
		if containsSecret(toDatum(v)) {
			m.err.WriteString("a secret cannot leave sash\n")
			return value{}, exitType
		}
		if atCond && code == 0 && !isBoolValue(v) {
			m.ctl = ctlHalt
			m.err.WriteString(errAwaitFnBool + "\n")
			return value{}, exitType
		}
		m.typedScalar = typedScalar(v)
		return v, code
	}
	m.typedScalar = false
	return value{kind: valBytes, b: []byte(output), redacted: redacted}, code
}

// typedScalar is a typed return that is not a list of rows: a bool, number,
// string, null, one map, or a list that is not all maps. await refuses it
// anywhere but the end of its condition, so no later stage, function or
// not, can turn a returned false into text that passes.
func typedScalar(v value) bool {
	switch v.kind {
	case valStr:
		return true
	case valData:
		if list, isList := v.d.([]any); isList {
			_, isRows := datumRows(list)
			return !isRows
		}
		return true
	}
	return false
}

// errAwaitFnBool: a function in an await condition answers readiness
// explicitly. Rows or any other value would be judged by their shape (one
// row of {count: 0} looks ready), so only a bool is accepted.
const errAwaitFnBool = "type await: a function in the condition must return a bool: return [ <test> ], for example return [ len($rows) > 0 ]"

func isBoolValue(v value) bool {
	if v.kind != valData {
		return false
	}
	_, ok := v.d.(bool)
	return ok
}
