// Package connect is sash connect: the client half of the terminal
// protocol, run by a person at a workstation (contract
// docs/plans/2026-11-27-sash-platform-verbs.md section 5).
package connect

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Filter is what stands between the remote terminal stream and the local
// terminal. It is always on; nothing turns it off.
//
// Threats: a hostile host, or data a host prints, driving the workstation
// terminal through PTY bytes: setting the window title, writing the
// clipboard (OSC 52), opening hyperlinks, loading fonts or keys (DCS), or
// making the terminal answer a query (DSR, DA, DECRQM, window reports,
// DECID, mouse and focus reporting) so that the answer is typed back as
// input. The filter passes printable text, LF, CR, TAB, BS, and an
// allow-list of complete CSI and two-byte escape sequences for cursor
// movement, colour, and erasing. Everything else is dropped: every OSC,
// DCS, APC, PM and SOS string whole, every CSI sequence not on the list,
// every other escape, every other control byte, C1 controls (as bytes or
// as U+0080 to U+009F), format characters, and bytes that are not UTF-8.
// A sequence reaches the terminal only when all of it has arrived and
// passed, so a sequence split over two reads is judged as one. Memory is
// bounded: at most csiMax bytes of one sequence and three bytes of one
// rune are held, and a dropped string holds nothing. It does not protect
// what the screen shows: the text is the host's, as with any remote shell.
type Filter struct {
	state   int
	seq     [csiMax]byte // the CSI sequence so far, after ESC [
	n       int
	rune    [utf8.UTFMax]byte // a rune split over two writes
	rn      int
	oscLike bool // the string being dropped also ends at BEL
	// Dropped counts bytes the filter did not pass.
	Dropped uint64
	// AltScreen is true while the host has the alternate screen on, so
	// the client can leave it when the session ends.
	AltScreen bool
}

// csiMax is the longest CSI sequence, after ESC [, the filter considers.
// A longer one is dropped whole.
const csiMax = 64

const (
	stGround    = iota
	stEsc       // after ESC
	stEscInter  // after ESC and an intermediate byte: one more byte follows
	stCSI       // after ESC [
	stCSIDrop   // a CSI sequence over csiMax: dropped through its final byte
	stString    // inside OSC, DCS, APC, PM or SOS
	stStringEsc // ESC inside such a string
)

// Write filters p and returns what may be written to the terminal. The
// result is at most len(p) plus the bytes held from earlier writes.
func (f *Filter) Write(p []byte) []byte {
	out := make([]byte, 0, len(p)+f.n+f.rn+2)
	for _, b := range p {
		out = f.step(out, b)
	}
	return out
}

func (f *Filter) drop(n int) { f.Dropped += uint64(n) }

func (f *Filter) step(out []byte, b byte) []byte {
	switch f.state {
	case stEsc:
		return f.afterEsc(out, b)
	case stEscInter:
		// ESC ( B and ESC ( 0: the character set the line drawing of a
		// full-screen program uses. Nothing else with an intermediate.
		f.state = stGround
		if f.seq[0] == '(' && (b == 'B' || b == '0') {
			return append(out, 0x1b, '(', b)
		}
		f.drop(2)
		return f.reprocess(out, b)
	case stCSI:
		return f.inCSI(out, b)
	case stCSIDrop:
		switch {
		case b >= 0x40 && b <= 0x7e:
			f.drop(1)
			f.state = stGround
		case b >= 0x20 && b <= 0x3f:
			f.drop(1)
		default:
			f.state = stGround
			return f.ground(out, b)
		}
		return out
	case stString:
		switch {
		case b == 0x1b:
			f.state = stStringEsc
		case b == 0x18 || b == 0x1a, b == 0x07 && f.oscLike:
			f.state = stGround
		}
		f.drop(1)
		return out
	case stStringEsc:
		if b == '\\' {
			f.drop(1)
			f.state = stGround
			return out
		}
		// As a terminal does: an ESC that is not the string's end starts
		// a new sequence, judged like any other.
		return f.afterEsc(out, b)
	}
	return f.ground(out, b)
}

// reprocess judges a byte that ended a dropped sequence without being
// part of it.
func (f *Filter) reprocess(out []byte, b byte) []byte {
	if b >= 0x20 && b < 0x7f {
		// A printable byte that only completed the dropped sequence.
		f.drop(1)
		return out
	}
	return f.ground(out, b)
}

func (f *Filter) afterEsc(out []byte, b byte) []byte {
	f.state = stGround
	switch b {
	case '[':
		f.state, f.n = stCSI, 0
		return out
	case ']':
		f.state, f.oscLike = stString, true
		f.drop(2)
		return out
	case 'P', '_', '^', 'X':
		f.state, f.oscLike = stString, false
		f.drop(2)
		return out
	case '7', '8', 'M', 'D', 'E', '=', '>':
		// Save and restore the cursor, index, reverse index, next line,
		// keypad modes.
		return append(out, 0x1b, b)
	case 0x1b:
		f.drop(1)
		f.state = stEsc
		return out
	}
	if b >= 0x20 && b <= 0x2f {
		f.seq[0] = b
		f.state = stEscInter
		return out
	}
	f.drop(1)
	return f.reprocess(out, b)
}

func (f *Filter) inCSI(out []byte, b byte) []byte {
	switch {
	case b >= 0x40 && b <= 0x7e:
		f.state = stGround
		if csiAllowed(f.seq[:f.n], b) {
			if f.n > 0 && f.seq[0] == '?' {
				for _, m := range strings.Split(string(f.seq[1:f.n]), ";") {
					if m == "47" || m == "1047" || m == "1049" {
						f.AltScreen = b == 'h'
					}
				}
			}
			out = append(out, 0x1b, '[')
			out = append(out, f.seq[:f.n]...)
			return append(out, b)
		}
		f.drop(f.n + 3)
		return out
	case b >= 0x20 && b <= 0x3f:
		if f.n == csiMax {
			f.drop(f.n + 3)
			f.state = stCSIDrop
			return out
		}
		f.seq[f.n] = b
		f.n++
		return out
	}
	// A control byte, or a byte past 0x7e, inside the sequence: the
	// sequence is dropped and the byte is judged on its own.
	f.drop(f.n + 2)
	f.state = stGround
	return f.ground(out, b)
}

// privateModes are the DEC private modes a host may set and reset: cursor
// keys, autowrap, cursor blink and visibility, the alternate screen, and
// bracketed paste. Mouse and focus reporting (1000 to 1016, 1004) make the
// terminal write, and are not here.
var privateModes = map[string]bool{"1": true, "7": true, "12": true, "25": true, "47": true, "1047": true, "1048": true, "1049": true, "2004": true}

// csiAllowed is the allow-list: body is what stands between ESC [ and the
// final byte.
func csiAllowed(body []byte, final byte) bool {
	if len(body) > 0 && body[0] == '?' {
		if final != 'h' && final != 'l' {
			return false
		}
		start := 1
		for i := 1; i <= len(body); i++ {
			if i < len(body) && body[i] != ';' {
				if body[i] < '0' || body[i] > '9' {
					return false
				}
				continue
			}
			if !privateModes[string(body[start:i])] {
				return false
			}
			start = i + 1
		}
		return true
	}
	params := 1
	for _, c := range body {
		switch {
		case c >= '0' && c <= '9':
		case c == ';':
			params++
		case c == ':' && final == 'm':
			// The colon form of an SGR colour.
		default:
			// A private marker (< = > ?) or an intermediate: a query, a
			// report, or a mode this list does not hold.
			return false
		}
	}
	switch final {
	case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'f', 'd', // cursor movement
		'J', 'K', 'X', // erase
		'L', 'M', 'P', '@', 'S', // insert, delete, scroll up
		'm',      // colour and style
		'r',      // scroll region
		's', 'u': // save and restore the cursor
		return true
	case 'T':
		// Scroll down; with more parameters it starts mouse tracking.
		return params == 1
	}
	// n (status report), c (device attributes), t (window operations),
	// p (mode request), x, q, i, b, h and l without ?, and the rest.
	return false
}

// ground judges one byte of text.
func (f *Filter) ground(out []byte, b byte) []byte {
	if f.rn > 0 {
		if b&0xc0 == 0x80 {
			f.rune[f.rn] = b
			f.rn++
			if !utf8.FullRune(f.rune[:f.rn]) {
				return out
			}
			r, size := utf8.DecodeRune(f.rune[:f.rn])
			n := f.rn
			f.rn = 0
			if r == utf8.RuneError && size <= 1 || !textRune(r) {
				f.drop(n)
				return out
			}
			return append(out, f.rune[:n]...)
		}
		// The rune was cut short: its bytes are dropped.
		f.drop(f.rn)
		f.rn = 0
	}
	switch {
	case b == 0x1b:
		f.state = stEsc
		return out
	case b == '\n' || b == '\r' || b == '\t' || b == '\b':
		return append(out, b)
	case b >= 0x20 && b < 0x7f:
		return append(out, b)
	case b >= 0xc2 && b <= 0xf4:
		f.rune[0] = b
		f.rn = 1
		return out
	}
	// Another control byte, 0x7f, a C1 byte, or a byte no UTF-8 text holds.
	f.drop(1)
	return out
}

// textRune reports a rune beyond ASCII that is printable text: no C1
// control, no format character (bidi overrides, zero-width), no separator
// but the space.
func textRune(r rune) bool {
	return r >= 0xa0 && unicode.IsPrint(r)
}
