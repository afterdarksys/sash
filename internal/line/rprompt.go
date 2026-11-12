package line

// The right prompt is fixed fields in a fixed order, drawn as literal
// text: the grant purpose, time to expiry, plan, the secret count, and the
// last status. Threats: there is no prompt variable, no format string, and
// no expansion, so no displayed value is ever parsed (CVE-2021-45444, zsh
// PROMPT_SUBST running a branch name). The purpose comes from the signed
// grant but is still host text: it goes through needsCell and is capped.
// Secrets are a count only, never a name or a value. Expiry is display
// only; the agent re-checks the grant. Nothing here calls the parent.

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
)

// purposeMax caps the shown purpose, in runes of its escaped form.
const purposeMax = 32

// nowFn is the clock for expiry; tests replace it.
var nowFn = time.Now

// rightPrompt is the right prompt's text. A field with nothing to say is
// left out: no purpose or expiry on a hand start (no grant), plan only
// when on, secrets only when there are any, !N only on a failure.
func rightPrompt(g grant.Grant, st *lang.State, last int, now time.Time) string {
	var fields []string
	if p := capPurpose(g.Purpose); p != "" {
		fields = append(fields, p)
	}
	if g.Exp != 0 {
		fields = append(fields, expField(g.Exp-now.Unix()))
	}
	if st.Plan() {
		fields = append(fields, "plan")
	}
	if n := st.SecretCount(); n > 0 {
		fields = append(fields, fmt.Sprintf("secrets %d", n))
	}
	if last != 0 {
		fields = append(fields, fmt.Sprintf("!%d", last))
	}
	return strings.Join(fields, "  ")
}

// capPurpose is the purpose through needsCell's grammar (a literal
// backslash before an escape-looking sequence is doubled, so the text
// \x1b never reads as a real ESC), cut at the longest prefix whose
// escaped form fits purposeMax runes, so an escape is never cut in half.
func capPurpose(p string) string {
	out := ""
	for i := 0; i < len(p); {
		_, size := utf8.DecodeRuneInString(p[i:])
		next := lang.Cell(p[:i+size])
		if utf8.RuneCountInString(next) > purposeMax {
			break
		}
		out = next
		i += size
	}
	return out
}

func expField(left int64) string {
	switch {
	case left <= 0:
		return "expired"
	case left < 60:
		return fmt.Sprintf("exp %ds", left)
	case left < 3600:
		return fmt.Sprintf("exp %dm", left/60)
	case left < 48*3600:
		return fmt.Sprintf("exp %dh", left/3600)
	default:
		return fmt.Sprintf("exp %dd", left/86400)
	}
}

// rightAt is where Frame draws the right prompt: the 1-based column, or 0
// to drop it, when the width is unknown or it would touch the line. All
// widths are display widths.
func rightAt(v View) int {
	if v.Right == "" || v.Width <= 0 {
		return 0
	}
	used := paintedWidth(promptText) + paintedWidth(v.Line) + paintedWidth(v.Suggestion)
	col := v.Width - paintedWidth(v.Right) + 1
	if col <= used+1 {
		return 0
	}
	return col
}
