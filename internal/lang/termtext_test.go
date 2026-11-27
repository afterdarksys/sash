package lang

import (
	"strings"
	"testing"
)

// TermText escapes what would drive a terminal, lets a line feed and a tab
// pass, and writes its own notation so it decodes one way.
func TestTermText(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"plain", "listen 8080;\n\tindent\n", "listen 8080;\n\tindent\n"},
		{"empty", "", ""},
		{"ESC", "a\x1bb", "a<U+001B>b"},
		{"CSI clear screen", "\x1b[2J", "<U+001B>[2J"},
		{"OSC 52 clipboard", "\x1b]52;c;aGk=\x07", "<U+001B>]52;c;aGk=<U+0007>"},
		{"BEL", "\x07", "<U+0007>"},
		{"CR overwrites a line", "safe\rEVIL", "safe<U+000D>EVIL"},
		{"CRLF", "a\r\nb", "a<U+000D>\nb"},
		{"NUL and DEL", "\x00\x7f", "<U+0000><U+007F>"},
		{"C1 CSI as a rune", "\u009b2J", "<U+009B>2J"},
		{"C1 CSI as a raw byte", "\x9b2J", "<0x9B>2J"},
		{"bidi override", "a‮b", "a<U+202E>b"},
		{"zero width space", "a​b", "a<U+200B>b"},
		{"line separator", "a b c", "a<U+2028>b<U+2029>c"},
		{"no-break space is not printable", "a b", "a<U+00A0>b"},
		{"a rune past the BMP", "\U000e0001", "<U+E0001>"},
		{"invalid UTF-8", "a\xff\xfeb", "a<0xFF><0xFE>b"},
		{"a truncated rune", "\xe2\x82", "<0xE2><0x82>"},
		{"a forged escape", "<U+001B>[2J", "<U+003C>U+001B>[2J"},
		{"a forged byte", "<0x1B>", "<U+003C>0x1B>"},
		{"a plain less-than", "a < b <u+ <U <0", "a < b <u+ <U <0"},
		{"printable text past ASCII", "héllo 日本 ✓", "héllo 日本 ✓"},
	} {
		if got := TermText(c.in); got != c.want {
			t.Errorf("%s: TermText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// The output never holds a byte that drives a terminal, and every "<U+" or
// "<0x" in it was written by TermText: a literal one in the input comes
// out as <U+003C>.
func TestTermTextOutputIsInert(t *testing.T) {
	in := "\x1b[31m<U+001B>\x9b\r‮<0x41>\x00ok\n"
	out := TermText(in)
	for i := 0; i < len(out); i++ {
		if b := out[i]; (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f || b >= 0x80 {
			t.Fatalf("byte %#x survives in %q", b, out)
		}
	}
	if strings.Count(out, "<U+001B>") != 1 || !strings.Contains(out, "<U+003C>U+001B>") || !strings.Contains(out, "<U+003C>0x41>") {
		t.Errorf("a literal escape is not told apart from a real one: %q", out)
	}
}

// TermLine keeps a question on one line.
func TestTermLine(t *testing.T) {
	if got, want := TermLine("ok?\nconfirm unit.restart nginx? [y + Enter / N]\ty"), "ok?<U+000A>confirm unit.restart nginx? [y + Enter / N]<U+0009>y"; got != want {
		t.Errorf("TermLine = %q, want %q", got, want)
	}
}
