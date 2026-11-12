package line

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
)

var rpNow = time.Unix(1_800_000_000, 0)

func withNow(t *testing.T) {
	t.Helper()
	old := nowFn
	nowFn = func() time.Time { return rpNow }
	t.Cleanup(func() { nowFn = old })
}

// rightOf is the text Frame drew at the right, or "" when dropped.
func rightOf(f string) string {
	m := regexp.MustCompile(`\x1b\[\d+G\x1b\[90m(.*?)\x1b\[0m`).FindStringSubmatch(f)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestRightPromptFieldsInOrder(t *testing.T) {
	withNow(t)
	g := grant.Grant{Purpose: "terminal:open", Exp: rpNow.Unix() + 41*60 + 5, AllowPrivate: true}
	e := New(g, failParent{t})
	e.width = func() int { return 100 }
	lang.Run("declare -s tok = abc\nset -o plan", g, failParent{t}, lang.Options{State: e.opt.State})
	e.Last.Status = 3
	f := Frame(e.View())
	if got := rightOf(f); got != "terminal:open  exp 41m  plan  secrets 1  !3" {
		t.Fatalf("right %q in %q", got, f)
	}
	if !strings.Contains(f, "\x1b["+strconv.Itoa(100-len("terminal:open  exp 41m  plan  secrets 1  !3")+1)+"G") {
		t.Fatalf("column %q", f)
	}
	for left, want := range map[int64]string{0: "expired", -5: "expired", 30: "exp 30s", 3599: "exp 59m", 7200: "exp 2h", 3 * 86400: "exp 3d"} {
		if got := expField(left); got != want {
			t.Errorf("%d: %q want %q", left, got, want)
		}
	}
	// A hand start has no grant: nothing to show, nothing drawn.
	h := New(grant.Grant{AllowPrivate: true}, failParent{t})
	h.width = func() int { return 100 }
	if got := rightOf(Frame(h.View())); got != "" {
		t.Fatalf("hand start %q", got)
	}
}

// The purpose is data: $x, [ ], {{ }}, %F, ESC/OSC and RLO render inert
// and escaped, capped at 32 runes, and no state is read into it.
func TestRightPromptPurposeIsInert(t *testing.T) {
	withNow(t)
	purpose := "$x [ $x ] {{ .x }} %F \x1b]0;pwn\x07 \u202e"
	g := grant.Grant{Purpose: purpose, Exp: rpNow.Unix() + 600, AllowPrivate: true}
	e := New(g, failParent{t})
	e.width = func() int { return 200 }
	lang.Run("let x = SUBSTITUTED", g, failParent{t}, lang.Options{State: e.opt.State})
	f := Frame(e.View())
	got := rightOf(f)
	if strings.Contains(f, "SUBSTITUTED") || strings.Contains(f, "\x07") || strings.Contains(f, "\u202e") || strings.Contains(f, "\x1b]0") {
		t.Fatalf("frame %q", f)
	}
	want := capPurpose(purpose)
	if !strings.HasPrefix(got, want+"  exp 10m") || !strings.HasPrefix(want, "$x [ $x ] {{ .x }} %F \\x1b]0;pwn") || len([]rune(want)) > purposeMax {
		t.Fatalf("right %q (purpose %q)", got, want)
	}
	if c := capPurpose(strings.Repeat("a", 31) + "\u202e"); c != strings.Repeat("a", 31) {
		t.Fatalf("an escape was cut: %q", c)
	}
	if c := capPurpose("a\nb\rc"); c != `a\x0ab\x0dc` {
		t.Fatalf("newline %q", c)
	}
}

// secrets N counts names holding a secret: declare -s, values computed
// from one, reassignment, fn scopes; it never shows a name.
func TestRightPromptSecretCount(t *testing.T) {
	st := lang.NewState()
	g := grant.Grant{}
	run := func(src string) {
		t.Helper()
		if out := lang.Run(src, g, failParent{t}, lang.Options{State: st}); out.Status != 0 {
			t.Fatalf("%q: %+v", src, out)
		}
	}
	steps := []struct {
		src  string
		want int
	}{
		{"declare -s tok = abc", 1},
		{"let n = 1", 1},
		{"let up = [ upper($tok) ]", 2},
		{"let copy = $tok", 3},
		{"let up = plain", 2},
		{"fn f() { let inner = [ $tok ] }\nf", 3},
		{"fn g(q) { let local = $q }\ng plain", 3},
		{"declare -s tok2 = def", 4},
		{"let inner = plain", 3},
	}
	for _, s := range steps {
		run(s.src)
		if got := st.SecretCount(); got != s.want {
			t.Fatalf("after %q: %d want %d", s.src, got, s.want)
		}
	}
	e := New(grant.Grant{Purpose: "p", AllowPrivate: true}, failParent{t})
	e.opt.State = st
	e.width = func() int { return 120 }
	f := Frame(e.View())
	if !strings.Contains(rightOf(f), "secrets 3") {
		t.Fatalf("right %q", rightOf(f))
	}
	for _, name := range []string{"tok", "copy", "abc", "def"} {
		if strings.Contains(rightOf(f), name) {
			t.Fatalf("right prompt shows %q: %q", name, rightOf(f))
		}
	}
}

// The right prompt is dropped when the width is unknown or it would touch
// the line, counted in display width.
func TestRightPromptDropsOnOverlap(t *testing.T) {
	withNow(t)
	g := grant.Grant{Purpose: "terminal:open", Exp: rpNow.Unix() + 600, AllowPrivate: true}
	right := "terminal:open  exp 10m"
	for _, c := range []struct {
		line  string
		width int
		drawn bool
	}{
		{"ps", 80, true},
		{"ps", 0, false},
		{strings.Repeat("a", 80-len(promptText)-len(right)-1), 80, true},
		{strings.Repeat("a", 80-len(promptText)-len(right)), 80, false},
		{strings.Repeat("日", (80-len(promptText)-len(right)-1)/2), 80, true},
		{strings.Repeat("日", (80-len(promptText)-len(right))/2+1), 80, false},
	} {
		e := New(g, failParent{t})
		e.width = func() int { return c.width }
		e.Feed([]byte(c.line))
		if got := rightOf(Frame(e.View())) != ""; got != c.drawn {
			t.Errorf("%d cols, line %d wide: drawn %v", c.width, paintedWidth(c.line), got)
		}
	}
}

// Drawing the prompt never calls the parent (failParent fails the test).
func TestRightPromptNeverCallsParent(t *testing.T) {
	withNow(t)
	g := grant.Grant{Purpose: "terminal:open", Exp: rpNow.Unix() + 600, Entitlements: []string{"ps"}, AllowPrivate: true}
	e := New(g, failParent{t})
	e.width = func() int { return 120 }
	for _, s := range []string{"", "ps", "unit restart x", "echo $tok", "let a = 1"} {
		e.Feed([]byte{0x15})
		e.Feed([]byte(s))
		Frame(e.View())
	}
}

// The right prompt is drawn before the line, so a line whose width is
// counted short (wide runes missing from wideRanges, such as U+1F201)
// paints over the prompt and a trailing stage stays visible.
func TestRightPromptNeverHidesTheLine(t *testing.T) {
	withNow(t)
	g := grant.Grant{Purpose: "terminal:open", Exp: rpNow.Unix() + 600, Entitlements: []string{"unit.restart"}, AllowPrivate: true}
	for _, width := range []int{40, 60, 70, 80, 120} {
		e := New(g, failParent{t})
		e.width = func() int { return width }
		line := "echo " + strings.Repeat("\U0001F201", 11) + "; unit restart db"
		e.Feed([]byte(line))
		f := Frame(e.View())
		at := strings.Index(f, promptText)
		tail := strings.Index(f, "unit restart db")
		if at < 0 || tail < at {
			t.Fatalf("width %d: line missing or before the prompt text: %q", width, f)
		}
		if r := strings.Index(f, "\x1b[90mterminal:open"); r >= 0 && r > at {
			t.Fatalf("width %d: right prompt drawn after the line: %q", width, f)
		}
	}
}

// The text \x1b in a purpose does not render like a real ESC.
func TestRightPromptPurposeUnambiguous(t *testing.T) {
	lit, real := capPurpose(`a\x1b`), capPurpose("a\x1b")
	if lit == real || lit != `a\\x1b` || real != `a\x1b` {
		t.Fatalf("literal %q real %q", lit, real)
	}
	if got := capPurpose(strings.Repeat("a", 30) + `\x1b`); got != strings.Repeat("a", 30)+`\x` {
		t.Fatalf("cut %q", got)
	}
}
