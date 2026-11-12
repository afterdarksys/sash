package lang

import (
	"strconv"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
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

// check and log print a script's names and fields: an escape sequence,
// a bidi override, or a zero-width character becomes a space, as a
// newline does.
func TestCheckAndLogEscape(t *testing.T) {
	g := grant.Grant{}
	spaced := []string{"a [31mb", "a ]0;pwn b", "a b", "a b c"}
	for i, p := range escapePayloads {
		out := Run("check '"+p.lit+"' [ true ]", g, &countParent{}, Options{})
		escapeCheck(t, "check", out.Stdout, p.bad, "ok "+spaced[i]+"\n")
		out = Run("log info '"+p.lit+"'", g, &countParent{}, Options{})
		escapeCheck(t, "log", out.Stderr, p.bad, "info "+spaced[i]+"\n")
	}
	out := Run("check 'a\nb' [ true ]", g, &countParent{}, Options{})
	if out.Stdout != "ok a b\n" {
		t.Fatalf("newline %q", out.Stdout)
	}
}

// decodeCell reads needsCell's output back: \\ is a backslash, \xHH,
// \uHHHH, and \UHHHHHHHH are the escaped byte or rune, and any other
// backslash is itself. It exists to prove the output has one reading.
func decodeCell(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		rest := s[i+1:]
		if strings.HasPrefix(rest, `\`) {
			b.WriteByte('\\')
			i += 2
			continue
		}
		n := 0
		if rest != "" {
			n = map[byte]int{'x': 2, 'u': 4, 'U': 8}[rest[0]]
		}
		if n > 0 && len(rest) > n {
			if v, err := strconv.ParseUint(rest[1:1+n], 16, 32); err == nil {
				if n == 2 {
					b.WriteByte(byte(v))
				} else {
					b.WriteRune(rune(v))
				}
				i += 2 + n
				continue
			}
		}
		b.WriteByte('\\')
		i++
	}
	return b.String()
}

// needsCell keeps a lone backslash single and still decodes one way:
// every string over an alphabet of backslashes, escape letters, hex
// digits, ESC, a bad byte, and RLO round-trips.
func TestCellBackslashIsUnambiguous(t *testing.T) {
	for in, want := range map[string]string{
		`C:\dir café`:   `C:\dir café`,
		`/tmp/a\b`:      `/tmp/a\b`,
		`a\x1b`:         `a\\x1b`,
		"a\\\x1b":       `a\\\x1b`,
		`a\\`:           `a\\\`,
		`a\u202e`:       `a\\u202e`,
		`a\x1`:          `a\x1`,
		"日本\\語":         "日本\\語",
		"a\\\u202eb":    `a\\\u202eb`,
		`a\U0001f600z`:  `a\\U0001f600z`,
		`a\U0001f60`:    `a\U0001f60`,
		"tab\\\tx":      `tab\ x`,
		"bad\\\xffbyte": `bad\\\xffbyte`,
	} {
		if got := needsCell(in); got != want {
			t.Errorf("cell %q = %q want %q", in, got, want)
		}
	}
	alpha := []string{`\`, "x", "u", "U", "1", "b", "\x1b", "\xff", "\u202e", "0"}
	var walk func(prefix string, n int)
	walk = func(prefix string, n int) {
		if got := decodeCell(needsCell(prefix)); got != prefix {
			t.Fatalf("%q printed %q reads back %q", prefix, needsCell(prefix), got)
		}
		if n == 0 {
			return
		}
		for _, a := range alpha {
			walk(prefix+a, n-1)
		}
	}
	walk("", 5)
}

// A backslash in a log line or in an error path prints once; accents and
// CJK pass unchanged; ESC, OSC, and RLO in error text are still escaped.
func TestStderrBackslashPrintsOnce(t *testing.T) {
	g := grant.Grant{}
	out := Run(`log info 'C:\dir café 日本'`, g, &countParent{}, Options{})
	if !strings.HasSuffix(out.Stderr, " info C:\\dir café 日本\n") || strings.Count(out.Stderr, "\\") != 1 {
		t.Fatalf("log %q", out.Stderr)
	}
	out = Run(`'/tmp/a\b'`, g, &countParent{}, Options{})
	if out.Status != exitUnknown || out.Stderr != "unknown /tmp/a\\b\n" {
		t.Fatalf("unknown %+v", out)
	}
	out = Run(`'/tmp/日本\é'`, g, &countParent{}, Options{})
	if out.Stderr != "unknown /tmp/日本\\é\n" {
		t.Fatalf("cjk %q", out.Stderr)
	}
	out = Run("'a\\\x1b]0;pwn\x07\u202eb'", g, &countParent{}, Options{})
	if out.Stderr != `unknown a\\\x1b]0;pwn\x07\u202eb`+"\n" {
		t.Fatalf("escapes %q", out.Stderr)
	}
}

// escRowsParent returns a row whose cells carry a CSI, an OSC title
// write, an RLO, and a backslash.
type escRowsParent struct{}

func (escRowsParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{Kind: parent.KindRows, Cols: []string{"user", "cmd"}, Rows: []parent.Row{
		{"user": "a\x1b[31mb", "cmd": "x\x1b]0;pwn\x07\u202ey C:\\dir"},
	}}, nil
}

// Row tables are display: host cells and declare -p values print
// through needsCell. echo stays byte-faithful.
func TestRowTablesEscapeCells(t *testing.T) {
	g := grant.Grant{V: "sash/1", Entitlements: []string{"ps"}}
	out := Run("ps", g, escRowsParent{}, Options{})
	want := `a\x1b[31mb  x\x1b]0;pwn\x07\u202ey C:\dir` + "\n"
	if out.Stdout != want {
		t.Fatalf("ps %q want %q", out.Stdout, want)
	}
	out = Run("ps |> head 1", g, escRowsParent{}, Options{})
	if out.Stdout != want {
		t.Fatalf("ps |> head %q", out.Stdout)
	}
	src := "let x = 'v\x1b]0;pwn\x07w'\ndeclare -p x\necho $x"
	out = Run(src, g, &countParent{}, Options{})
	if !strings.HasSuffix(out.Stdout, "  v\\x1b]0;pwn\\x07w\nv\x1b]0;pwn\x07w\n") || strings.Count(out.Stdout, "\x1b") != 1 {
		t.Fatalf("declare -p %q", out.Stdout)
	}
	for _, src := range []string{"declare", "builtin", "checks", "dirs"} {
		out = Run("let y = 'q\x1bq'\ncheck 'c\x1bc' [ true ]\n"+src, g, &countParent{}, Options{})
		if strings.Contains(out.Stdout[strings.Index(out.Stdout, "\n")+1:], "\x1b") {
			t.Errorf("%s printed %q", src, out.Stdout)
		}
	}
}
