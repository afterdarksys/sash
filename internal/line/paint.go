package line

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/lang"
)

const promptText = "sash> "

// Paint draws the line. Denial dim stays when syntax colors are off.
// A syntax-off filter emits no trailing reset. The suggestion is gray.
// A control, C1, format (bidi, zero-width), or other unprintable rune,
// and a byte that is not UTF-8, is drawn as its needsCell escape in
// reverse video, in the line and the suggestion; the line keeps the
// bytes, so a pasted U+202E cannot disguise what Enter runs.
func Paint(line, suggestion string, spans []lang.Span, syntax bool) string {
	faces := make([]lang.Face, len(line))
	covered := make([]bool, len(line))
	for _, sp := range spans {
		for i := sp.Start; i < sp.End && i < len(line); i++ {
			if i >= 0 {
				faces[i] = sp.Face
				covered[i] = true
			}
		}
	}
	var b strings.Builder
	open := ""
	for i := 0; i < len(line); {
		if esc, size, ok := lang.EscapeRune(line[i:]); ok {
			if open != "" {
				b.WriteString("\x1b[0m")
				open = ""
			}
			b.WriteString("\x1b[7m" + esc + "\x1b[0m")
			i += size
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		if size < 1 {
			size = 1
		}
		face := lang.FacePlain
		if i < len(covered) && covered[i] {
			face = faces[i]
		}
		want := sgr(face, syntax)
		if want != open {
			if open != "" {
				b.WriteString("\x1b[0m")
			}
			b.WriteString(want)
			open = want
		}
		b.WriteString(line[i : i+size])
		i += size
	}
	if open != "" {
		b.WriteString("\x1b[0m")
	}
	if suggestion != "" {
		b.WriteString("\x1b[90m")
		for i := 0; i < len(suggestion); {
			esc, size, ok := lang.EscapeRune(suggestion[i:])
			if ok {
				b.WriteString("\x1b[7m" + esc + "\x1b[0m\x1b[90m")
			} else {
				b.WriteString(suggestion[i : i+size])
			}
			i += size
		}
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// paintedWidth counts the columns Paint uses for s: an escaped rune is
// its escape's length, any other rune its display width. Two cases look
// at the rune before: VS16 (U+FE0F) after a one-column rune asks for
// emoji presentation, which terminals draw two columns wide, so it adds
// one; and a pair of regional indicators is one two-column flag, so the
// second of a pair adds nothing.
func paintedWidth(s string) int {
	n := 0
	prevW := 0
	riOpen := false
	for i := 0; i < len(s); {
		esc, size, ok := lang.EscapeRune(s[i:])
		if ok {
			n += len(esc)
			prevW, riOpen = 0, false
			i += size
			continue
		}
		r, _ := utf8.DecodeRuneInString(s[i:])
		w := runeWidth(r)
		switch {
		case r == 0xfe0f:
			if prevW == 1 {
				w = 1
			}
		case r >= 0x1f1e6 && r <= 0x1f1ff:
			if riOpen {
				w = 0
			}
			riOpen = !riOpen
		default:
			riOpen = false
		}
		n += w
		if r != 0xfe0f {
			prevW = w
		} else {
			prevW = 0
		}
		i += size
	}
	return n
}

// wideRanges are the East Asian Wide and Fullwidth blocks, the emoji
// blocks, and the default-emoji-presentation symbols in 2600-27BF, which
// a terminal draws two columns wide. It is a compact table, not the full
// Unicode EastAsianWidth data: a rare wide rune outside it counts one
// column and leaves the cursor short. Text-presentation symbols in
// 2600-27BF (U+2600 sun, U+2764 heart) stay one column unless VS16
// follows; see paintedWidth.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec},
	{0x23f0, 0x23f0}, {0x23f3, 0x23f3}, {0x25fd, 0x25fe},
	{0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693},
	{0x26a1, 0x26a1}, {0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5},
	{0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3},
	{0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705},
	{0x270a, 0x270b}, {0x2728, 0x2728}, {0x274c, 0x274c}, {0x274e, 0x274e},
	{0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797}, {0x27b0, 0x27b0},
	{0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff},
	{0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf}, {0xa960, 0xa97f},
	{0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f},
	{0xff00, 0xff60}, {0xffe0, 0xffe6},
	{0x1f1e6, 0x1f1ff}, {0x1f300, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f900, 0x1f9ff},
	{0x1fa70, 0x1faff}, {0x20000, 0x2fffd}, {0x30000, 0x3fffd},
}

// runeWidth is the columns a printable rune takes on its own: 0 for a
// combining mark (VS16 included) or a Hangul medial jamo, 2 for a wide
// one, else 1. Format characters (zero-width) never get here: Paint
// escapes them.
func runeWidth(r rune) int {
	if unicode.In(r, unicode.Mn, unicode.Me) || (r >= 0x1160 && r <= 0x11ff) {
		return 0
	}
	for _, w := range wideRanges {
		if r >= w[0] && r <= w[1] {
			return 2
		}
	}
	return 1
}

// sgr is a face's color. The authorization marks (denied, out of root,
// command from a value) stay when syntax colors are off.
func sgr(face lang.Face, syntax bool) string {
	switch face {
	case lang.FaceDenied:
		return "\x1b[2m"
	case lang.FaceOutOfRoot:
		return "\x1b[2;4m"
	case lang.FaceFromValue:
		return "\x1b[2;3;4m"
	}
	if !syntax {
		return ""
	}
	switch face {
	case lang.FaceFilter:
		return "\x1b[32m"
	case lang.FaceToolkit:
		return "\x1b[36m"
	case lang.FaceUnknown:
		return "\x1b[33m"
	case lang.FaceDanger:
		return "\x1b[31m"
	case lang.FaceTypePipe:
		return "\x1b[35m"
	case lang.FaceDynamic:
		return "\x1b[33;4m"
	default:
		return ""
	}
}

// Frame is one prompt paint: the line, then the smile or the menu, then
// the cursor back on the typed column.
func Frame(v View) string {
	var b strings.Builder
	b.WriteString("\r\x1b[J")
	// The right prompt is drawn first and the line over it, so a width
	// this package counts short (a wide rune missing from wideRanges)
	// can only hide prompt text under the line, never the line.
	if col := rightAt(v); col > 0 {
		fmt.Fprintf(&b, "\x1b[%dG\x1b[90m%s\x1b[0m\r", col, v.Right)
	}
	b.WriteString(promptText)
	b.WriteString(Paint(v.Line, v.Suggestion, v.Spans, v.Syntax))
	extra := 0
	if v.MenuOpen {
		extra = len(v.Menu)
		for i, row := range v.Menu {
			b.WriteByte('\n')
			b.WriteString(menuLine(row, i == v.MenuIndex, v.Syntax))
		}
	} else if v.Smile != "" {
		extra = 1
		b.WriteByte('\n')
		b.WriteString(v.Smile)
	}
	if extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", extra)
	}
	line := v.Line
	cur := v.Cursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(line) {
		cur = len(line)
	}
	col := utf8.RuneCountInString(promptText) + paintedWidth(line[:cur])
	b.WriteByte('\r')
	if col > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", col)
	}
	return b.String()
}

func menuLine(row MenuRow, selected, syntax bool) string {
	mark := "  "
	if selected {
		mark = "> "
	}
	// Every cell is escaped: a seen path is a name from the host.
	body := mark + lang.SafeText(strings.TrimRight(row.Insert, " ")) + "  " + lang.SafeText(row.Desc)
	if row.Key != "" {
		body += "  " + lang.SafeText(row.Key)
	}
	if row.Denied {
		return "\x1b[2m" + body + "\x1b[0m"
	}
	if selected && syntax {
		return "\x1b[7m" + body + "\x1b[0m"
	}
	return body
}
