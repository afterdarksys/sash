package connect

import (
	"bytes"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func filterAll(in string) string {
	var f Filter
	return string(f.Write([]byte(in)))
}

// The allow and deny table: what a host may send to the local terminal,
// and what never arrives.
func TestFilterTable(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		// Passed.
		{"text", "hello, world", "hello, world"},
		{"line controls", "a\r\nb\tc\bd", "a\r\nb\tc\bd"},
		{"utf-8 text", "naïve 日本語 ✓", "naïve 日本語 ✓"},
		{"cursor up", "\x1b[3A", "\x1b[3A"},
		{"cursor position", "\x1b[12;40H", "\x1b[12;40H"},
		{"column", "\x1b[7G", "\x1b[7G"},
		{"erase display", "\x1b[2J\x1b[J\x1b[K", "\x1b[2J\x1b[J\x1b[K"},
		{"colour", "\x1b[1;31mred\x1b[0m", "\x1b[1;31mred\x1b[0m"},
		{"256 and true colour", "\x1b[38;5;196m\x1b[38;2;1;2;3m\x1b[38:2::1:2:3m", "\x1b[38;5;196m\x1b[38;2;1;2;3m\x1b[38:2::1:2:3m"},
		{"insert and delete", "\x1b[2L\x1b[M\x1b[3P\x1b[@\x1b[4X", "\x1b[2L\x1b[M\x1b[3P\x1b[@\x1b[4X"},
		{"scroll", "\x1b[2S\x1b[1T\x1b[2;20r", "\x1b[2S\x1b[1T\x1b[2;20r"},
		{"save and restore", "\x1b[s\x1b[u\x1b7\x1b8", "\x1b[s\x1b[u\x1b7\x1b8"},
		{"cursor visibility", "\x1b[?25l\x1b[?25h", "\x1b[?25l\x1b[?25h"},
		{"alternate screen", "\x1b[?1049h\x1b[?1049l", "\x1b[?1049h\x1b[?1049l"},
		{"bracketed paste", "\x1b[?2004h\x1b[?2004l", "\x1b[?2004h\x1b[?2004l"},
		{"two modes", "\x1b[?1;25h", "\x1b[?1;25h"},
		{"reverse index and keypad", "\x1bM\x1b=\x1b>\x1bD\x1bE", "\x1bM\x1b=\x1b>\x1bD\x1bE"},
		{"charset", "\x1b(0qqq\x1b(B", "\x1b(0qqq\x1b(B"},
		{"four-digit parameters", "\x1b[9999S\x1b[9999;9999H\x1b[0009C", "\x1b[9999S\x1b[9999;9999H\x1b[0009C"},

		// A numeric parameter of five digits or more: the sequence is
		// dropped whole, never clamped.
		{"five-digit scroll", "a\x1b[10000Sb", "ab"},
		{"huge insert", "a\x1b[" + strings.Repeat("9", 60) + "Lb", "ab"},
		{"huge cursor move", "a\x1b[99999999999Cb", "ab"},
		{"huge second parameter", "a\x1b[1;00000Hb", "ab"},
		{"huge colour", "a\x1b[38;5;123456mb", "ab"},
		{"huge colon colour", "a\x1b[38:2::1:2:30000mb", "ab"},
		{"huge scroll region", "a\x1b[1;12345rb", "ab"},
		{"huge private mode", "a\x1b[?10000hb", "ab"},

		// OSC: dropped whole, whichever way it ends.
		{"title, BEL", "a\x1b]0;evil title\x07b", "ab"},
		{"title, ST", "a\x1b]2;evil\x1b\\b", "ab"},
		{"clipboard OSC 52", "a\x1b]52;c;ZXZpbA==\x07b", "ab"},
		{"hyperlink OSC 8", "a\x1b]8;;http://evil/\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		{"OSC colour query", "\x1b]11;?\x07x", "x"},
		{"OSC never ended", "a\x1b]0;rest of the stream", "a"},
		{"OSC cut by a new sequence", "\x1b]0;t\x1b[31mred", "\x1b[31mred"},
		{"OSC cut by CAN", "\x1b]0;t\x18ok", "ok"},

		// DCS, APC, PM, SOS.
		{"DCS", "a\x1bP+q544e\x1b\\b", "ab"},
		{"DCS does not end at BEL", "a\x1bPdata\x07still\x1b\\b", "ab"},
		{"DECRQSS", "\x1bP$qm\x1b\\x", "x"},
		{"APC", "a\x1b_Gf=100;payload\x1b\\b", "ab"},
		{"PM", "a\x1b^private\x1b\\b", "ab"},
		{"SOS", "a\x1bXstring\x1b\\b", "ab"},

		// Queries and reports: anything that makes the terminal write.
		{"cursor position report", "a\x1b[6nb", "ab"},
		{"status report", "a\x1b[5nb", "ab"},
		{"DEC status report", "a\x1b[?6nb", "ab"},
		{"device attributes", "a\x1b[cb\x1b[0cc", "abc"},
		{"secondary device attributes", "a\x1b[>cb\x1b[>0cc", "abc"},
		{"tertiary device attributes", "a\x1b[=cb", "ab"},
		{"DECID", "a\x1bZb", "ab"},
		{"window report", "a\x1b[18tb\x1b[21tc", "abc"},
		{"window resize", "a\x1b[8;10;10tb", "ab"},
		{"mode request", "a\x1b[?25$pb\x1b[4$pc", "abc"},
		{"terminal parameters", "a\x1b[xb", "ab"},
		{"XTVERSION", "a\x1b[>qb\x1b[>0qc", "abc"},
		{"kitty keyboard query", "a\x1b[?ub", "ab"},
		{"mouse tracking", "a\x1b[?1000hb\x1b[?1003hc\x1b[?1006hd", "abcd"},
		{"focus reporting", "a\x1b[?1004hb", "ab"},
		{"an allowed mode beside a refused one", "a\x1b[?25;1000hb", "ab"},
		{"mouse highlight tracking", "a\x1b[1;2;3;4;5Tb", "ab"},
		{"print screen", "a\x1b[ib\x1b[5ic", "abc"},
		{"repeat", "a\x1b[9999bb", "ab"},
		{"cursor style", "a\x1b[2 qb", "ab"},
		{"insert mode", "a\x1b[4hb", "ab"},
		{"full reset", "a\x1bcb", "ab"},
		{"other charset", "a\x1b(Ab\x1b%Gc", "abc"},
		{"soft reset", "a\x1b[!pb", "ab"},

		// Controls and bytes.
		{"BEL, NUL, DEL", "a\x07b\x00c\x7fd", "abcd"},
		{"shift out, ENQ", "a\x0eb\x05c\x0fd", "abcd"},
		{"vertical tab and form feed", "a\x0bb\x0cc", "abc"},
		{"C1 CSI byte", "a\x9b6nb", "a6nb"},
		{"C1 CSI as U+009B", "a\u009b6nb", "a6nb"},
		{"C1 OSC as U+009D", "a\u009d0;t\u009cb", "a0;tb"},
		{"bidi override", "a\u202eb", "ab"},
		{"zero width", "a\u200bb\ufeffc", "abc"},
		{"line separators", "a\u2028b\u2029c", "abc"},
		{"invalid UTF-8", "a\xffb\xc3(c\xe2\x82d", "ab(cd"},
		{"overlong", "a\xc0\x80b\xe0\x80\x80c", "abc"},
		{"surrogate", "a\xed\xa0\x80b", "ab"},
		{"control inside CSI", "a\x1b[3\nAb", "a\nAb"},
		{"ESC inside CSI", "a\x1b[3\x1b[2Jb", "a\x1b[2Jb"},
		{"CSI over the cap", "a\x1b[" + strings.Repeat("1;", 100) + "mb", "ab"},
		{"lone ESC at the end", "a\x1b", "a"},
		{"ESC ESC", "a\x1b\x1b[1mb", "a\x1b[1mb"},
	} {
		if got := filterAll(c.in); got != c.want {
			t.Errorf("%s: %q gave %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// A sequence split over two reads, at every byte, is judged as one: the
// output is the same wherever the frames were cut.
func TestFilterStatefulAcrossSplits(t *testing.T) {
	for _, in := range []string{
		"a\x1b]52;c;ZXZpbA==\x07b",
		"a\x1b]0;title\x1b\\b",
		"a\x1bP+q544e\x1b\\b",
		"a\x1b[6nb",
		"a\x1b[?1000hb",
		"a\x1b[1;31mred\x1b[0mb",
		"a\x1b[?25lb",
		"日本語\u202e✓",
		"a\x1b(0x\x1b(Bb",
		"a\x1b_apc\x1b\\\x1b^pm\x1b\\b",
	} {
		whole := filterAll(in)
		for cut := 0; cut <= len(in); cut++ {
			var f Filter
			got := string(f.Write([]byte(in[:cut]))) + string(f.Write([]byte(in[cut:])))
			if got != whole {
				t.Errorf("%q cut at %d: %q, whole %q", in, cut, got, whole)
			}
		}
		// And one byte at a time.
		var f Filter
		var b strings.Builder
		for i := 0; i < len(in); i++ {
			b.Write(f.Write([]byte{in[i]}))
		}
		if b.String() != whole {
			t.Errorf("%q byte by byte: %q, whole %q", in, b.String(), whole)
		}
	}
	// The first half of an OSC 52 alone prints nothing of it, and the
	// second half alone is not taken for text of a new stream.
	var f Filter
	if got := string(f.Write([]byte("x\x1b]52;c;"))); got != "x" {
		t.Errorf("first half: %q", got)
	}
	if got := string(f.Write([]byte("ZXZpbA==\x07y"))); got != "y" {
		t.Errorf("second half: %q", got)
	}
}

// The filter counts what it dropped and knows when the alternate screen
// is on.
func TestFilterCountsAndAltScreen(t *testing.T) {
	var f Filter
	f.Write([]byte("a\x1b]0;t\x07b"))
	if f.Dropped == 0 {
		t.Errorf("nothing counted as dropped")
	}
	f.Write([]byte("\x1b[?1049h"))
	if !f.AltScreen {
		t.Errorf("alternate screen on was not seen")
	}
	f.Write([]byte("\x1b[?1049l"))
	if f.AltScreen {
		t.Errorf("alternate screen off was not seen")
	}
}

// checkFiltered fails when out holds anything the filter must never
// write: it is an independent reading of the output.
func checkFiltered(t *testing.T, in, out []byte) {
	t.Helper()
	if !utf8.Valid(out) {
		t.Fatalf("output is not UTF-8: %q from %q", out, in)
	}
	for i := 0; i < len(out); {
		b := out[i]
		if b == 0x1b {
			n := allowedSeqLen(out[i:])
			if n == 0 {
				t.Fatalf("an escape not on the list at %d: %q from %q", i, out, in)
			}
			i += n
			continue
		}
		r, size := utf8.DecodeRune(out[i:])
		ok := r == '\n' || r == '\r' || r == '\t' || r == '\b' || (r >= 0x20 && r < 0x7f) || (r >= 0xa0 && unicode.IsPrint(r))
		if !ok {
			t.Fatalf("rune %U at %d: %q from %q", r, i, out, in)
		}
		i += size
	}
}

// allowedSeqLen is the length of the allowed sequence at the head of b,
// or 0. It is written apart from the filter's own table.
func allowedSeqLen(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	if strings.IndexByte("78MDE=>", b[1]) >= 0 {
		return 2
	}
	if b[1] == '(' && len(b) >= 3 && (b[2] == 'B' || b[2] == '0') {
		return 3
	}
	if b[1] != '[' {
		return 0
	}
	i := 2
	private := i < len(b) && b[i] == '?'
	if private {
		i++
	}
	for i < len(b) && (b[i] >= '0' && b[i] <= '9' || b[i] == ';' || b[i] == ':') {
		i++
	}
	if i >= len(b) || i-2 > csiMax {
		return 0
	}
	final := b[i]
	if private {
		if final != 'h' && final != 'l' {
			return 0
		}
		for _, m := range strings.Split(string(b[3:i]), ";") {
			switch m {
			case "1", "7", "12", "25", "47", "1047", "1048", "1049", "2004":
			default:
				return 0
			}
		}
		return i + 1
	}
	if strings.IndexByte("ABCDEFGHfdJKXLMP@SmrsuT", final) < 0 {
		return 0
	}
	if final != 'm' && bytes.IndexByte(b[2:i], ':') >= 0 {
		return 0
	}
	if final == 'T' && bytes.IndexByte(b[2:i], ';') >= 0 {
		return 0
	}
	return i + 1
}

// FuzzFilter: whatever the host sends, and wherever the frames are cut,
// the output holds only text and allowed sequences, is never longer than
// the input, is the same for every cut, and passes through the filter
// again unchanged.
func FuzzFilter(f *testing.F) {
	for _, s := range []string{
		"hello\r\n", "\x1b[1;31mred\x1b[0m", "\x1b]52;c;ZXZpbA==\x07", "\x1b]0;t\x1b\\", "\x1bP+q\x1b\\", "\x1b[6n", "\x1b[c", "\x1b[>c",
		"\x1b[?1000h", "\x1b[?25l", "\x1b[18t", "\x1b[?25$p", "\x1b_a\x1b\\", "\x1b^a\x1b\\", "\x1bXa\x1b\\", "\x9b6n", "\u009b6n", "\u202e",
		"\xff\xfe", "\xe2\x82", "\x1b", "\x1b[", "\x1b[?", "\x1b]", "\x1b(0", "\x1b(", "\x1b[" + strings.Repeat("9", 80) + "m", "\x1b]\x1b]\x1b[2J",
		"\x1b[38:2::1:2:3m", "\x1b[1;2;3;4;5T", "a\x1b[3\x1b[2Jb", "\x1bZ", "\x1bc", "\x18\x1a\x07", "\x1b[?1049h",
	} {
		f.Add([]byte(s), uint16(len(s)/2))
	}
	f.Fuzz(func(t *testing.T, in []byte, cut uint16) {
		var whole Filter
		out := whole.Write(in)
		if len(out) > len(in) {
			t.Fatalf("output longer than input: %d > %d", len(out), len(in))
		}
		if whole.n > csiMax || whole.rn > utf8.UTFMax {
			t.Fatalf("held %d and %d bytes", whole.n, whole.rn)
		}
		checkFiltered(t, in, out)
		at := int(cut)
		if at > len(in) {
			at = len(in)
		}
		var split Filter
		got := append(split.Write(in[:at:at]), split.Write(in[at:])...)
		if !bytes.Equal(got, out) {
			t.Fatalf("cut at %d: %q, whole %q, from %q", at, got, out, in)
		}
		var again Filter
		if twice := again.Write(out); !bytes.Equal(twice, out) {
			t.Fatalf("filtered twice: %q, once %q, from %q", twice, out, in)
		}
	})
}
