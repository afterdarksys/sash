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
		b.WriteString(suggestion)
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func sgr(face lang.Face, syntax bool) string {
	if face == lang.FaceDenied {
		return "\x1b[2m"
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
	col := utf8.RuneCountInString(promptText) + utf8.RuneCountInString(line[:cur])
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
