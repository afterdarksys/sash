package lang

// Terminal escaping of byte output (docs/plans/2026-11-27-sash-platform-verbs.md
// section 2).
//
// Threats: a file body, a journal line, a process name, or a person's typed
// answer that repaints the screen, sets the window title, or makes the
// terminal answer a query; bidi and zero-width text that hides part of a
// line; and a writer that forges an escape by writing <U+001B> literally.
// TermText is applied at the last write, and only when that write goes to a
// terminal: a pipe, a file, a value, a > body, and a recording keep their
// bytes. It does not protect a reader of a pipe or a file, and it is not the
// cell escaping (needsCell), which stays as it is for rows and messages.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// termPlain reports a rune TermText writes as it is.
func termPlain(r rune) bool {
	return r == '\n' || r == '\t' || !unsafeRune(r)
}

// TermText makes s safe to write to a terminal. A line feed and a tab pass.
// Every other control character (a carriage return included), a C1
// control, a format character (bidi overrides, zero-width), U+2028,
// U+2029, and anything else unicode.IsPrint rejects becomes <U+XXXX>,
// upper-case hex, at least four digits. A byte that is not UTF-8 becomes
// <0xHH>. The literal text "<U+" or "<0x" becomes "<U+003C>U+" or
// "<U+003C>0x", so the output decodes one way only.
func TermText(s string) string {
	clean := true
	for i := 0; i < len(s) && clean; {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || !termPlain(r) || (r == '<' && termForged(s[i+1:])) {
			clean = false
		}
		i += size
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, "<0x%02X>", s[i])
		case r == '<' && termForged(s[i+1:]):
			b.WriteString("<U+003C>")
		case termPlain(r):
			b.WriteString(s[i : i+size])
		default:
			fmt.Fprintf(&b, "<U+%04X>", r)
		}
		i += size
	}
	return b.String()
}

// termForged reports text after a "<" that would read as one of
// TermText's own escapes.
func termForged(rest string) bool {
	return strings.HasPrefix(rest, "U+") || strings.HasPrefix(rest, "0x")
}

// TermLine is TermText for text that must stay on one line (a question a
// script asks): a line feed and a tab are escaped too, so the text cannot
// draw a second line that reads as sash's own.
func TermLine(s string) string {
	return strings.NewReplacer("\n", "<U+000A>", "\t", "<U+0009>").Replace(TermText(s))
}
