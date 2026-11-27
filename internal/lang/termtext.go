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
	"unicode"
	"unicode/utf8"
)

// termPlain reports a rune TermText writes as it is, whatever comes
// before it.
func termPlain(r rune) bool {
	return r == '\n' || r == '\t' || (!unsafeRune(r) && !termInvisible(r))
}

// termInvisible is a printable rune that draws nothing a reader can see,
// so it can hide text or forge a gap: a Default_Ignorable_Code_Point
// (U+034F, the Hangul fillers U+115F, U+1160, U+3164 and U+FFA0, U+17B4
// and U+17B5, the variation selectors U+180B-U+180F, U+FE00-U+FE0F and
// U+E0100-U+E01EF, and the rest of Other_Default_Ignorable_Code_Point),
// and the blank braille pattern U+2800.
func termInvisible(r rune) bool {
	return r == 0x2800 || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Variation_Selector, r)
}

// termMarksMax is how many stacking combining marks (Mn, Me) one base
// keeps as they are; the rest are escaped, so a flood of marks cannot
// paint over the lines around it. A spacing mark (Mc) is not counted and
// does not end the run; any other rune does.
const termMarksMax = 4

// termStacking reports a nonspacing or enclosing mark, and termMark any
// combining mark.
func termStacking(r rune) bool { return unicode.In(r, unicode.Mn, unicode.Me) }

func termMark(r rune) bool { return unicode.Is(unicode.M, r) }

// TermText makes s safe to write to a terminal. A line feed and a tab pass.
// Every other control character (a carriage return included), a C1
// control, a format character (bidi overrides, zero-width), U+2028,
// U+2029, a default-ignorable rune or U+2800 (termInvisible), a fifth or
// later stacking mark on one base (termMarksMax), and anything else
// unicode.IsPrint rejects becomes <U+XXXX>, upper-case hex, at least four
// digits. A byte that is not UTF-8 becomes <0xHH>. The literal text "<U+"
// or "<0x" becomes "<U+003C>U+" or "<U+003C>0x", so the output decodes
// one way only.
func TermText(s string) string {
	clean := true
	marks := 0
	for i := 0; i < len(s) && clean; {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || !termPlain(r) || (r == '<' && termForged(s[i+1:])) {
			clean = false
		}
		marks = termMarks(marks, r)
		clean = clean && marks <= termMarksMax
		i += size
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	marks = 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !(r == utf8.RuneError && size == 1) {
			marks = termMarks(marks, r)
		} else {
			marks = 0
		}
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, "<0x%02X>", s[i])
		case r == '<' && termForged(s[i+1:]):
			b.WriteString("<U+003C>")
		case termPlain(r) && marks <= termMarksMax:
			b.WriteString(s[i : i+size])
		default:
			fmt.Fprintf(&b, "<U+%04X>", r)
		}
		i += size
	}
	return b.String()
}

// termMarks is the count of stacking marks on the current base after r:
// one more for a stacking mark, the same for a spacing mark, and zero
// for anything else.
func termMarks(n int, r rune) int {
	switch {
	case termStacking(r):
		return n + 1
	case termMark(r):
		return n
	}
	return 0
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
