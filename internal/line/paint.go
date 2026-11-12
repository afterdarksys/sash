package line

import (
	"fmt"
	"strings"
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
// its escape's length, any other rune one column.
func paintedWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		esc, size, ok := lang.EscapeRune(s[i:])
		if ok {
			n += len(esc)
		} else {
			n++
		}
		i += size
	}
	return n
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
	body := mark + strings.TrimRight(row.Insert, " ") + "  " + row.Desc
	if row.Key != "" {
		body += "  " + row.Key
	}
	if row.Denied {
		return "\x1b[2m" + body + "\x1b[0m"
	}
	if selected && syntax {
		return "\x1b[7m" + body + "\x1b[0m"
	}
	return body
}
