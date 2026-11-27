package line

import (
	"bytes"
	"io"
	"testing"

	"github.com/afterdarksys/sash/internal/lang"
)

// hostile is byte output that would drive a terminal: ESC, a CSI, OSC 52,
// BEL, CR, C1 as a rune and as a raw byte, a bidi override, a zero-width
// space, a line separator, bytes that are not UTF-8, and a literal escape
// in sash's own notation.
const hostile = "a\x1b[2J\x1b]52;c;aGk=\x07\rb\u009b\x9b‮​ \xff<U+001B><0x1B>\tend\n"

const hostileEscaped = "a<U+001B>[2J<U+001B>]52;c;aGk=<U+0007><U+000D>b<U+009B><0x9B><U+202E><U+200B><U+2028><0xFF><U+003C>U+001B><U+003C>0x1B>\tend\n"

// writeSubmit escapes a line's byte output on its way to a terminal, and
// only there: stdout and stderr are judged each on its own, and a pipe or
// a file receives the bytes unchanged.
func TestWriteSubmitEscapesOnlyForATerminal(t *testing.T) {
	old := isTermWriter
	defer func() { isTermWriter = old }()
	var out, errOut bytes.Buffer
	last := lang.Outcome{Stdout: hostile, Stderr: "unknown <U+001B>x\n"}
	for _, c := range []struct {
		name            string
		outTTY, errTTY  bool
		wantOut, wantEr string
	}{
		{"both terminals", true, true, hostileEscaped, "unknown <U+003C>U+001B>x\n"},
		{"stdout a pipe", false, true, hostile, "unknown <U+003C>U+001B>x\n"},
		{"stderr a pipe", true, false, hostileEscaped, "unknown <U+001B>x\n"},
		{"both pipes", false, false, hostile, "unknown <U+001B>x\n"},
	} {
		out.Reset()
		errOut.Reset()
		isTermWriter = func(w io.Writer) bool {
			if w == io.Writer(&out) {
				return c.outTTY
			}
			return c.errTTY
		}
		if err := writeSubmit(&out, &errOut, View{}, last); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != "\r\n"+c.wantOut {
			t.Errorf("%s: stdout %q, want %q", c.name, got, "\r\n"+c.wantOut)
		}
		if got := errOut.String(); got != c.wantEr {
			t.Errorf("%s: stderr %q, want %q", c.name, got, c.wantEr)
		}
	}
}

// A buffer, or any writer that is not a terminal file, is never escaped.
func TestTermBytesLeavesNonTerminalsAlone(t *testing.T) {
	var b bytes.Buffer
	if got := termBytes(&b, hostile); got != hostile {
		t.Errorf("a buffer was escaped: %q", got)
	}
	if isTermWriter(&b) || isTermWriter(nil) {
		t.Errorf("a buffer is a terminal")
	}
}
