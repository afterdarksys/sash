package line

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

// v5 faces in the editor: out of root, a command from a value (Block),
// and a dynamic subcommand. The authorization marks stay with syntax off.
func TestLiveGrantFaces(t *testing.T) {
	g := grant.Grant{Entitlements: []string{"file.read", "unit.status"}, FileRead: []string{"/var/log"}, AllowPrivate: true}
	for _, syntax := range []bool{true, false} {
		p := &countParent{}
		e := New(g, p)
		e.Syntax = syntax
		e.Feed([]byte("cat /var/log/a /etc/shadow"))
		v := e.View()
		painted := Paint(v.Line, v.Suggestion, v.Spans, v.Syntax)
		if !v.Block || !v.Denied || !strings.Contains(painted, "\x1b[2;4m/etc/shadow\x1b[0m") || strings.Contains(painted, "\x1b[2m") {
			t.Fatalf("syntax %v out of root %q %+v", syntax, painted, v)
		}
		e.Feed([]byte{0x15})
		e.Feed([]byte("$c x"))
		v = e.View()
		painted = Paint(v.Line, v.Suggestion, v.Spans, v.Syntax)
		if !v.Block || v.Smile != "command_from_value" || !strings.Contains(painted, "\x1b[2;3;4m$c\x1b[0m") {
			t.Fatalf("syntax %v from value %q %+v", syntax, painted, v)
		}
		e.Feed([]byte("\r"))
		if e.Runs != 0 || p.n != 0 {
			t.Fatalf("syntax %v from value ran: runs %d calls %d", syntax, e.Runs, p.n)
		}
		e.Feed([]byte{0x15})
	}
	e := New(g, nil)
	e.Feed([]byte("unit $op nginx"))
	v := e.View()
	painted := Paint(v.Line, v.Suggestion, v.Spans, v.Syntax)
	if v.Block || !strings.Contains(painted, "\x1b[33;4m$op") || strings.Contains(painted, "\x1b[32m") {
		t.Fatalf("dynamic %q %+v", painted, v)
	}
}

// A format, C1, or bad byte on the line paints as its escape in reverse
// video; the line keeps the bytes, and the cursor column counts the
// painted form.
func TestPaintEscapesFormatAndC1(t *testing.T) {
	line := "echo a\u202eb\u0085c\u200bd"
	painted := Paint(line+"\xff", "x\u202ey", nil, true)
	for _, bad := range []string{"\u202e", "\u0085", "\u200b", "\xff"} {
		if strings.Contains(painted, bad) {
			t.Fatalf("raw %q in %q", bad, painted)
		}
	}
	for _, esc := range []string{`\u202e`, `\u0085`, `\u200b`, `\xff`} {
		if !strings.Contains(painted, "\x1b[7m"+esc+"\x1b[0m") {
			t.Fatalf("no reverse %s in %q", esc, painted)
		}
	}
	e := New(grant.Grant{AllowPrivate: true}, nil)
	e.Feed([]byte(line))
	if e.line != line {
		t.Fatalf("line changed %q", e.line)
	}
	f := Frame(e.View())
	want := len(promptText) + len("echo a") + len(`\u202e`) + 1 + len(`\u0085`) + 1 + len(`\u200b`) + 1
	if !strings.HasSuffix(f, "\r\x1b["+strconv.Itoa(want)+"C") {
		t.Fatalf("cursor column, want %d: %q", want, f[len(f)-12:])
	}
}

// The cursor column is the display width: a wide rune is two columns, a
// combining mark none.
func TestFrameCountsDisplayWidth(t *testing.T) {
	for line, cols := range map[string]int{
		"echo 日本":                             5 + 4,
		"echo ｱ":                              5 + 1,
		"echo Ａ":                              5 + 2,
		"echo e\u0301":                        5 + 1,
		"echo 한국":                             5 + 4,
		"echo \U0001f600":                     5 + 2,
		"echo café":                           5 + 4,
		"echo \U0001f680":                     5 + 2,
		"echo \U0001f6eb":                     5 + 2,
		"echo \U0001fa70":                     5 + 2,
		"echo \U0001fa90":                     5 + 2,
		"echo \U0001f1fa\U0001f1f8":           5 + 2,
		"echo \U0001f1fa":                     5 + 2,
		"echo \U0001f1fa\U0001f1f8\U0001f1eb": 5 + 4,
		"echo \u2614":                         5 + 2,
		"echo \u26a1":                         5 + 2,
		"echo \u2705":                         5 + 2,
		"echo \u27bf":                         5 + 2,
		"echo \u2600":                         5 + 1,
		"echo \u2600\ufe0f":                   5 + 2,
		"echo \u2764\ufe0f":                   5 + 2,
		"echo \u2614\ufe0f":                   5 + 2,
		"echo a\ufe0f":                        5 + 2,
		"echo \u65e5\ufe0f":                   5 + 2,
	} {
		e := New(grant.Grant{AllowPrivate: true}, nil)
		e.Feed([]byte(line))
		f := Frame(e.View())
		want := "\r\x1b[" + strconv.Itoa(len(promptText)+cols) + "C"
		if !strings.HasSuffix(f, want) {
			t.Errorf("%q: want column %d, frame ends %q", line, len(promptText)+cols, f[len(f)-10:])
		}
	}
}

// The memo is keyed by line, prefix, and State generation: a cursor move
// reuses it, and a write to State, made anywhere, refreshes it.
func TestPreviewMemo(t *testing.T) {
	e := New(grant.Grant{AllowPrivate: true}, nil)
	e.Feed([]byte("zzq"))
	v := e.View()
	if v.Smile != "unknown zzq" {
		t.Fatalf("before fn %q", v.Smile)
	}
	gen := e.opt.State.Generation()
	e.Feed([]byte("\x1b[D"))
	if e.memo.gen != gen || e.memo.line != "zzq" {
		t.Fatalf("cursor move missed the memo %+v", e.memo)
	}
	for _, src := range []string{"fn zzq() { echo hi }", "let x = 1", "set -e", "declare -i n = 2"} {
		before := e.opt.State.Generation()
		lang.Run(src, e.g, closedParent{}, lang.Options{State: e.opt.State})
		if e.opt.State.Generation() == before {
			t.Fatalf("%q did not bump the generation", src)
		}
	}
	if v := e.View(); v.Smile == "unknown zzq" {
		t.Fatalf("stale preview after fn: %q", v.Smile)
	}
	before := e.opt.State.Generation()
	e.opt.State.SetXtrace(true)
	if e.opt.State.Generation() == before {
		t.Fatal("SetXtrace did not bump the generation")
	}
}

// framesWriter counts painted frames.
type framesWriter struct {
	frames int
	buf    bytes.Buffer
}

func (w *framesWriter) Write(b []byte) (int, error) {
	if bytes.HasPrefix(b, []byte("\r\x1b[J")) {
		w.frames++
	}
	return w.buf.Write(b)
}

// A paste is one preview per submission, not one per byte, and each
// submission is painted whole before its output.
func TestLoopCoalescesPaste(t *testing.T) {
	line := "echo " + strings.Repeat("a", 4000)
	in := newTTYSource(strings.NewReader(line + "\recho two\r"))
	e := New(grant.Grant{AllowPrivate: true}, nil)
	e.Syntax = false
	var out, errOut framesWriter
	if st := e.loop(in, &out, &errOut); st != 0 {
		t.Fatalf("status %d", st)
	}
	if out.frames > 6 {
		t.Fatalf("%d frames for two pasted lines", out.frames)
	}
	got := out.buf.String()
	first := strings.Index(got, promptText+"echo "+strings.Repeat("a", 4000))
	output := strings.Index(got, strings.Repeat("a", 4000)+"\n")
	second := strings.Index(got, promptText+"echo two")
	outTwo := strings.Index(got, "two\n")
	if first < 0 || output < first || second < output || outTwo < second {
		t.Fatalf("order %d %d %d %d", first, output, second, outTwo)
	}
	if e.Runs != 2 {
		t.Fatalf("runs %d", e.Runs)
	}
}

// failParent fails the test on any call.
type failParent struct{ t *testing.T }

func (f failParent) Do(v parent.Verb) (parent.Result, error) {
	f.t.Fatalf("parent called while typing: key %q", v.Key)
	return parent.Result{}, nil
}

// typedCorpus are lines typed key by key: verbs, denials, globs, history
// and brace designators, use, redirects, values, escapes, and deep ones.
var typedCorpus = []string{
	"ps | egrep sshd",
	"unit restart nginx",
	"cat /var/log/*.log /etc/sh*",
	"ls **/x; ls *(/)",
	"echo {a,b,c}{1..3} !! !$ !-1",
	"use /var/log/lib.sash; use $p",
	"cat < /etc/shadow > /tmp/out",
	"template_file /var/log/s.yaml /etc/t /tmp/o --check",
	"$cmd x | $f",
	"unit $op nginx; package $p vim; config $c /etc/hosts",
	"let x = [ len(\"a\") ]; fn f(a) { cat $a }; f /etc/x",
	"try { cat /etc/x } catch e { echo $e.message }",
	"try { cat /etc/x }",
	"declare -s tok = abc; echo $tok",
	"echo a\u202eb\u0085c\x1b[31m",
	"retry 3 delay 1s { timeout 1m { with set -e { journal } } }",
	"if [ " + strings.Repeat("(", 70) + "1" + strings.Repeat(")", 70) + " ] { ps }",
	"cd /var/log; pushd /etc; popd; dirs",
	"why unit restart x",
	"\x1b[A\x1b[B\x12cat\x07\x09\x01\x05\x0b\x19\x17\x15",
	"echo 'unterminated",
}

// Typing never calls the parent: Preview uses denyParent and does not
// Expand; globs and history expand only at Enter. Enter is left out, so
// nothing is submitted.
func FuzzTypingNeverCallsParent(f *testing.F) {
	for _, s := range typedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		g := grant.Grant{
			Entitlements: []string{"ps", "file.read", "file.write", "unit.status", "config.read"},
			FileRead:     []string{"/var/log"},
			FileWrite:    []string{"/tmp"},
			AllowPrivate: true,
		}
		e := New(g, failParent{t})
		e.opt.State = lang.NewState()
		lang.Run("let p = /etc/x; let c = ps; fn f(a) { cat $a }", g, failParent{t}, lang.Options{State: e.opt.State})
		for i := 0; i < len(s) && i < maxLine && !e.done; i++ {
			if isEnter(s[i]) || s[i] == 0x04 {
				continue
			}
			e.Feed([]byte{s[i]})
			Frame(e.View())
		}
	})
}

// maxLineSource is an 8192-byte line nested 64 blocks deep, with a [ ]
// expression 63 parentheses deep and pipelines that read in and out of
// the grant's roots.
func maxLineSource() string {
	var b strings.Builder
	for i := 0; i < 64; i++ {
		b.WriteString("if [ true ] { ")
	}
	b.WriteString("echo [ " + strings.Repeat("(", 62) + "1" + strings.Repeat(")", 62) + " ]; ")
	tail := strings.Repeat(" }", 64)
	stmts := []string{"cat /var/log/a | egrep x; ", "unit status nginx; ", "ls /etc; ", "echo $x > /tmp/o; "}
	for i := 0; ; i++ {
		s := stmts[i%len(stmts)]
		if b.Len()+len(s)+len(tail) > maxLine {
			break
		}
		b.WriteString(s)
	}
	b.WriteString(strings.Repeat(" ", maxLine-b.Len()-len(tail)))
	b.WriteString(tail)
	return b.String()
}

// BenchmarkPreviewMaxLine guards the section 1 latency budget: Preview of
// a maxLine (8192-byte) line at depth 64 has p99 <= 2 ms. It reports
// p99-ns and fails when the budget is missed. Run it with
// go test -run x -bench PreviewMaxLine ./internal/line.
func BenchmarkPreviewMaxLine(b *testing.B) {
	src := maxLineSource()
	if len(src) != maxLine {
		b.Fatalf("line is %d bytes", len(src))
	}
	g := grant.Grant{Entitlements: []string{"ps", "file.read", "file.write", "unit.status"}, FileRead: []string{"/var/log"}, FileWrite: []string{"/tmp"}}
	opt := lang.Options{State: lang.NewState()}
	if pv := lang.Preview(src, g, opt); !pv.Block || pv.Smile != "denied file.read" {
		b.Fatalf("preview %q block %v", pv.Smile, pv.Block)
	}
	times := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		lang.Preview(src, g, opt)
		times = append(times, time.Since(start))
	}
	b.StopTimer()
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p99 := times[len(times)*99/100]
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns")
	if len(times) >= 100 && p99 > 2*time.Millisecond {
		b.Errorf("p99 %v over the 2 ms budget", p99)
	}
}
