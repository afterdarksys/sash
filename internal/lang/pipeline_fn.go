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
	if isSecret(toDatum(in)) {
		m.err.WriteString("a secret cannot leave sash\n")
		return value{}, exitType
	}
	saved := m.saveVars([]string{"input"})
	m.vars["input"] = in
	defer m.restoreVars(saved)
	// Do not copy a nonzero strings.Builder: save its text instead.
	outer := m.out.String()
	m.out.Reset()
	code, stop := m.callFn(st)
	output := m.out.String()
	m.out.Reset()
	m.out.WriteString(outer)
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
		if isSecret(toDatum(v)) {
			m.err.WriteString("a secret cannot leave sash\n")
			return value{}, exitType
		}
		m.typedScalar = typedScalar(v)
		return v, code
	}
	m.typedScalar = false
	return value{kind: valBytes, b: []byte(output)}, code
}

// typedScalar is a typed return that is not rows: a bool, number, string or
// null (await refuses it anywhere but the end of its condition).
func typedScalar(v value) bool {
	switch v.kind {
	case valStr:
		return true
	case valData:
		if _, isRows := datumRows(v.d); isRows {
			return false
		}
		_, isList := v.d.([]any)
		return !isList
	}
	return false
}
