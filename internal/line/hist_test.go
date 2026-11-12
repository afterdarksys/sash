package line

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// agentHist serves lines as agent-stored history.
type agentHist struct{ lines []string }

func (h *agentHist) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "history" && len(v.Argv) > 0 && v.Argv[0] == "get" {
		var rows []parent.Row
		for _, l := range h.lines {
			rows = append(rows, parent.Row{"line": l})
		}
		return parent.Result{Kind: parent.KindRows, Cols: []string{"line"}, Rows: rows}, nil
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

var badHistory = []string{
	"echo a\x1b[31mred",
	"echo a\x1b]0;pwn\x07",
	"echo a\u009bb",
	"echo \u202eevil",
	"echo a\u200bb",
	"echo a\u2028b",
	"echo a\tb",
	"echo \xffbad",
}

// Agent-stored history with ESC, OSC, C1, RLO, zero-width, a line
// separator, a tab, or a bad byte is dropped: it never reaches the
// suggestion, browsing, search, or the terminal.
func TestHistoryDropsEscapingLines(t *testing.T) {
	h := &agentHist{lines: append(append([]string{}, badHistory...), `echo C:\dir`, "echo good", "echo café 日本")}
	e := New(grant.Grant{}, h)
	if strings.Join(e.history, "|") != `echo C:\dir|echo good|echo café 日本` {
		t.Fatalf("history %q", e.history)
	}
	e.Feed([]byte("echo a"))
	if v := e.View(); v.Suggestion != "" {
		t.Fatalf("suggestion %q", v.Suggestion)
	}
	e.Feed([]byte{0x15})
	for i := 0; i < 12; i++ {
		e.Feed([]byte("\x1b[A"))
		f := Frame(e.View())
		for _, bad := range []string{"\x1b[31m", "\x1b]0", "\x07", "\u009b", "\u202e", "\u200b", "\u2028", "\t", "\xff"} {
			if strings.Contains(e.line, bad) || strings.Contains(f, bad) {
				t.Fatalf("browse shows %q: line %q", bad, e.line)
			}
		}
	}
	e.Feed([]byte{0x15, 0x12})
	e.Feed([]byte("evil"))
	if strings.Contains(e.line, "\u202e") {
		t.Fatalf("search found %q", e.line)
	}
}

// A line typed with such a rune runs but is not kept or sent to the
// agent's history.
func TestHistoryDoesNotKeepEscapingLines(t *testing.T) {
	p := &countParent{}
	e := New(grant.Grant{}, p)
	e.Feed([]byte("echo \u202eevil\r"))
	if e.Runs != 1 || len(e.history) != 0 {
		t.Fatalf("runs %d history %q", e.Runs, e.history)
	}
	e.Feed([]byte("echo fine\r"))
	if len(e.history) != 1 || e.history[0] != "echo fine" {
		t.Fatalf("history %q", e.history)
	}
	for _, l := range badHistory {
		if histSafe(l) {
			t.Errorf("%q kept", l)
		}
	}
	if !histSafe(`echo C:\dir \x1b`) {
		t.Fatal("a backslash dropped a line")
	}
}
