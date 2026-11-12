package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

// escapePayloads are literals a script can put in an error: a CSI
// sequence, an OSC title write, a bidi override, and zero-width characters.
var escapePayloads = []struct{ lit, bad, want string }{
	{"a\x1b[31mb", "\x1b", `a\x1b[31mb`},
	{"a\x1b]0;pwn\x07b", "\x07", `a\x1b]0;pwn\x07b`},
	{"a\u202eb", "\u202e", `a\u202eb`},
	{"a\u200bb\u200dc", "\u200b", `a\u200bb\u200dc`},
}

func escapeCheck(t *testing.T, where, got, bad, want string) {
	t.Helper()
	if strings.Contains(got, bad) || !strings.Contains(got, want) {
		t.Errorf("%s printed %q", where, got)
	}
}

// Every message that carries script text leaves escaped: a run's stderr,
// a parse error in a run, why, needs, and the preview hint.
func TestErrorsEscapeScriptText(t *testing.T) {
	g := grant.Grant{V: "sash/1", Entitlements: []string{"ps"}}
	for _, p := range escapePayloads {
		unknown := "'" + p.lit + "'"
		out := Run(unknown, g, &countParent{}, Options{})
		escapeCheck(t, "run unknown", out.Stderr, p.bad, "unknown "+p.want)

		withItem := "with '" + p.lit + "' { ps }"
		out = Run(withItem, g, &countParent{}, Options{})
		escapeCheck(t, "run parse", out.Stderr, p.bad, "parse: with item "+p.want)

		out = Why(withItem, g, Options{})
		escapeCheck(t, "why parse", out.Stderr, p.bad, p.want)

		out = Needs(withItem, NeedsOptions{})
		escapeCheck(t, "needs parse", out.Stderr, p.bad, p.want)

		pv := Preview(unknown, g, Options{})
		escapeCheck(t, "preview unknown", pv.Smile, p.bad, "unknown "+p.want)
		pv = Preview(withItem, g, Options{})
		escapeCheck(t, "preview parse", pv.Smile, p.bad, p.want)
	}
}
