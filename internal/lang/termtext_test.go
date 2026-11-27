package lang

import (
	"fmt"
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

// Runes that draw nothing are escaped, and so is a flood of stacking
// marks past four on one base; ordinary text in scripts that stack marks
// passes unchanged.
func TestTermTextInvisibleAndMarks(t *testing.T) {
	for _, r := range []rune{0x034F, 0x115F, 0x1160, 0x3164, 0xFFA0, 0x17B4, 0x17B5, 0x180B, 0x180C, 0x180D, 0x180F, 0xFE00, 0xFE0F, 0xE0100, 0xE01EF, 0x2800} {
		in := "a" + string(r) + "b"
		want := "a" + fmt.Sprintf("<U+%04X>", r) + "b"
		if got := TermText(in); got != want {
			t.Errorf("U+%04X: got %q, want %q", r, got, want)
		}
	}
	// Five stacking marks: the fifth is escaped. A spacing mark (U+0903)
	// is not counted and does not reset the run; a new base does.
	five := "é̂̃̄̅"
	if got := TermText(five); got != "é̂̃̄<U+0305>" {
		t.Errorf("five marks: %q", got)
	}
	if got := TermText("é̂ः̃̄̅x́"); got != "é̂ः̃̄<U+0305>x́" {
		t.Errorf("a spacing mark: %q", got)
	}
	if got := TermText("a" + strings.Repeat("̶", 200)); strings.Count(got, "<U+0336>") != 196 {
		t.Errorf("a flood: %q", got)
	}
	if got := TermText("e⃝⃝⃝⃝⃝"); got != "e⃝⃝⃝⃝<U+20DD>" {
		t.Errorf("enclosing marks: %q", got)
	}
	for name, s := range map[string]string{
		"Hindi":          "नमस्ते दुनिया, क्षत्रिय, हिन्दी",
		"Burmese":        "မင်္ဂလာပါ မြန်မာ",
		"Tibetan":        "བཀྲ་ཤིས་བདེ་ལེགས། སྐྱེས་",
		"Vietnamese NFD": "Tiếng Việt",
		"Hebrew":         "בְּרֵאשִׁ֖ית בָּרָ֣א",
		"Arabic":         "بِسْمِ ٱللَّهِ ٱلرَّحْمَٰنِ",
		"Thai":           "สวัสดีครับ ที่นี่",
	} {
		if got := TermText(s); got != s {
			t.Errorf("%s changed: %q", name, got)
		}
	}
}
