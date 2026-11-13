package lang

import (
	"fmt"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

// checkMax bounds the check rows one session keeps.
const checkMax = 10000

// runCheck evaluates check "name" [ expr ]: a row for checks, and ok or
// FAIL on the output. A failure is status 1 and does not stop the script
// unless set -e is on.
func (m *machine) runCheck(st expStage) (value, int) {
	d, err := m.evalX(st.x)
	if err != nil {
		m.err.WriteString(err.Error() + "\n")
		return value{}, exitType
	}
	pass, derr := decide(d)
	if derr != nil {
		m.err.WriteString(derr.Error() + "\n")
		return value{}, exitType
	}
	name := oneLine(st.argv[1])
	res, line, code := "pass", "ok "+name+"\n", 0
	if !pass {
		res, line, code = "fail", "FAIL "+name+"\n", 1
	}
	if len(m.st.checks) >= checkMax {
		m.err.WriteString("check max\n")
		return value{}, exitType
	}
	m.st.checks = append(m.st.checks, parent.Row{"check": name, "result": res})
	return value{kind: valBytes, b: []byte(line)}, code
}

var logLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// runLog writes one line on stderr: time, level, message, and the fields of
// an optional [ {map} ] in key order. Nothing goes to stdout.
func (m *machine) runLog(st expStage) (value, int) {
	var b strings.Builder
	b.WriteString(m.now().UTC().Format(time.RFC3339))
	b.WriteString(" " + st.argv[1] + " " + oneLine(st.argv[2]))
	if st.x != nil {
		d, err := m.evalX(st.x)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return value{}, exitType
		}
		// Fields computed from a secret log their keys and *** values.
		inner, secret := unsecret(d)
		o, ok := inner.(*omap)
		if !ok {
			m.err.WriteString("usage log: fields must be a map\n")
			return value{}, exitType
		}
		for _, k := range o.keys {
			v := textOf(o.m[k])
			if secret {
				v = redacted
			}
			fmt.Fprintf(&b, " %s=%s", oneLine(k), oneLine(v))
		}
	}
	m.err.WriteString(b.String() + "\n")
	return value{kind: valBytes}, 0
}

// oneLine keeps a check or log line one line and inert: every rune
// needsCell would escape (control, format, line separators, anything not
// printable) becomes a space, and a byte that is not UTF-8 becomes U+FFFD.
// It does not escape, so a log line that later leaves through safeText is
// not escaped twice.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return ' '
		}
		return r
	}, s)
}
