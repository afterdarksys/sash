package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostile is byte output that would drive a terminal: ESC, a CSI, OSC 52,
// BEL, CR, C1 as a rune and as a raw byte, a bidi override, a zero-width
// space, a line separator, a byte that is not UTF-8, and a literal escape
// in sash's own notation.
const hostile = "a\x1b[2J\x1b]52;c;aGk=\x07\rb\u009b\x9b‮​ \xff<U+001B><0x1B>\tend"

const hostileEscaped = "a<U+001B>[2J<U+001B>]52;c;aGk=<U+0007><U+000D>b<U+009B><0x9B><U+202E><U+200B><U+2028><0xFF><U+003C>U+001B><U+003C>0x1B>\tend"

// hostileFixture is an offline host whose ps row holds an escape sequence.
func hostileFixture(t *testing.T) (fixture string) {
	t.Helper()
	fixture = filepath.Join(t.TempDir(), "fx.json")
	fx := `{"grant":{"entitlements":["ps"],"file_read":[],"file_write":[]},"results":[` +
		`{"key":"ps","kind":"rows","cols":["user","cmd"],"rows":[{"user":"root","cmd":"a\u001b[2Jb"}]}]}`
	if err := os.WriteFile(fixture, []byte(fx), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// hostileSrc is a script that prints the hostile bytes: it decodes them
// itself, since bytes that are not UTF-8 cannot ride in a JSON fixture.
func hostileSrc() string {
	return "echo [ unbase64(\"" + base64.StdEncoding.EncodeToString([]byte(hostile)) + "\") ]\n"
}

// Byte output to a terminal is escaped at the last write; the same bytes
// to a pipe arrive unchanged, byte for byte. stdout and stderr are judged
// separately.
func TestBinaryByteOutputIsEscapedOnlyForATerminal(t *testing.T) {
	bin := ptyBin(t)
	src := hostileSrc() + "\"<U+001B>nosuch\"\n"
	const failure = "unknown <U+001B>nosuch"
	const failureEscaped = "unknown <U+003C>U+001B>nosuch"

	rep := runPTY(t, ptyRun{Argv: []string{bin, "-c", src}})
	if rep.Code != 127 || rep.TTY != hostileEscaped+"\r\n"+failureEscaped+"\r\n" {
		t.Errorf("a terminal: code %d tty %q", rep.Code, rep.TTY)
	}
	// stdout a pipe: the bytes as they came; stderr, still a terminal, escaped.
	rep = runPTY(t, ptyRun{Argv: []string{bin, "-c", src}, Stdout: "pipe"})
	if rep.Stdout != hostile+"\n" || rep.TTY != failureEscaped+"\r\n" {
		t.Errorf("stdout piped: stdout %q tty %q", rep.Stdout, rep.TTY)
	}
	// stderr a pipe: unchanged there; stdout, still a terminal, escaped.
	rep = runPTY(t, ptyRun{Argv: []string{bin, "-c", src}, Stderr: "pipe"})
	if rep.Stderr != failure+"\n" || rep.TTY != hostileEscaped+"\r\n" {
		t.Errorf("stderr piped: stderr %q tty %q", rep.Stderr, rep.TTY)
	}
	// Both pipes, with a terminal on stdin: nothing is escaped.
	rep = runPTY(t, ptyRun{Argv: []string{bin, "-c", src}, Stdout: "pipe", Stderr: "pipe"})
	if rep.Stdout != hostile+"\n" || rep.Stderr != failure+"\n" || rep.TTY != "" {
		t.Errorf("both piped: stdout %q stderr %q tty %q", rep.Stdout, rep.Stderr, rep.TTY)
	}
	// A file gets the bytes too.
	out := filepath.Join(t.TempDir(), "out")
	rep = runPTY(t, ptyRun{Argv: []string{"/bin/sh", "-c", "exec \"$0\" -c \"$1\" > \"$2\"", bin, hostileSrc(), out}})
	b, err := os.ReadFile(out)
	if err != nil || string(b) != hostile+"\n" || rep.Code != 0 {
		t.Errorf("a file: %q err %v code %d tty %q", b, err, rep.Code, rep.TTY)
	}
}

// The prompt escapes a line's byte output the same way, and a log line
// and a row keep their own escaping.
func TestBinaryPromptOutputIsEscaped(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{
		Argv: []string{bin},
		Steps: []ptyStep{
			{Expect: "sash> "}, {Send: strings.TrimSuffix(hostileSrc(), "\n") + "\r"}, {Expect: "end\r\n"},
			{Send: "log info \"<U+001B>x\"\r"}, {Expect: "info <U+003C>U+001B>x"},
			{Send: "\x04"},
		},
	})
	if rep.Code != 0 || !rep.Restored || !strings.Contains(rep.TTY, "\r\n"+hostileEscaped+"\r\n") {
		t.Errorf("code %d tty %q", rep.Code, rep.TTY)
	}
	for _, raw := range []string{"\x1b[2J", "\x1b]52", "\x07", "‮", "​", " ", "\xff", "\x9b"} {
		if strings.Contains(rep.TTY, raw) {
			t.Errorf("%q reached the terminal: %q", raw, rep.TTY)
		}
	}
}

// At the prompt too a pipe gets the bytes as they came: sash | tee keeps
// what the host sent.
func TestBinaryPromptOutputToAPipeIsUnchanged(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{
		Argv: []string{bin}, Stdout: "pipe",
		Steps: []ptyStep{{Sleep: 0.5}, {Send: strings.TrimSuffix(hostileSrc(), "\n") + "\r"}, {Sleep: 0.5}, {Send: "\x04"}},
	})
	if rep.Code != 0 || !strings.Contains(rep.Stdout, "\r\n"+hostile+"\n") || strings.Contains(rep.Stdout, "<U+001B>[2J") {
		t.Errorf("code %d stdout %q tty %q", rep.Code, rep.Stdout, rep.TTY)
	}
}

// Rows are unchanged: their cells keep the cell escaping (\x1b), at a
// terminal and in a pipe alike. A recording run and its replay escape
// byte output at a terminal as any run does.
func TestBinaryRowsKeepTheirEscaping(t *testing.T) {
	bin := ptyBin(t)
	fixture := hostileFixture(t)
	rec := filepath.Join(t.TempDir(), "rec.jsonl")
	const src = "ps\nps | head -n 1\n"
	rep := runPTY(t, ptyRun{Argv: []string{bin, "--record", rec, "--fixture", fixture, "-c", src}})
	// The table's cell is escaped as a cell; the same rows as bytes (ps |
	// head) are byte output, escaped at the terminal.
	want := "root  a\\x1b[2Jb\r\nroot  a<U+001B>[2Jb\r\n"
	if rep.Code != 0 || rep.TTY != want {
		t.Errorf("record: code %d tty %q, want %q", rep.Code, rep.TTY, want)
	}
	rep = runPTY(t, ptyRun{Argv: []string{bin, "--replay", rec, "--fixture", fixture, "-c", src}})
	if rep.Code != 0 || rep.TTY != want {
		t.Errorf("replay: code %d tty %q, want %q", rep.Code, rep.TTY, want)
	}
	rep = runPTY(t, ptyRun{Argv: []string{bin, "--replay", rec, "--fixture", fixture, "-c", src}, Stdout: "pipe"})
	if rep.Code != 0 || rep.Stdout != "root  a\\x1b[2Jb\nroot  a\x1b[2Jb\n" {
		t.Errorf("replay piped: code %d stdout %q", rep.Code, rep.Stdout)
	}
}
