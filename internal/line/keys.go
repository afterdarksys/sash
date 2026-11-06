package line

import "unicode/utf8"

type keyKind int

const (
	keyNone keyKind = iota
	keyRune
	keyEnter
	keyBackspace
	keyCtrlA
	keyCtrlB
	keyCtrlC
	keyCtrlD
	keyCtrlE
	keyCtrlF
	keyCtrlG
	keyCtrlK
	keyCtrlR
	keyCtrlU
	keyCtrlW
	keyCtrlY
	keyTab
	keyEsc
	keyUp
	keyDown
	keyRight
	keyLeft
	keyAltB
	keyAltF
	keyAltDot
	keySkip
)

type key struct {
	kind keyKind
	r    rune
}

// decode reads one key. ok is false when buf is only a prefix of a sequence
// or an incomplete UTF-8 rune. A following non-sequence byte commits Esc.
func decode(buf []byte) (n int, k key, ok bool) {
	if len(buf) == 0 {
		return 0, key{}, false
	}
	b := buf[0]
	if b == 0x1b {
		return decodeEsc(buf)
	}
	switch b {
	case '\r', '\n':
		return 1, key{kind: keyEnter}, true
	case 0x7f, 0x08:
		return 1, key{kind: keyBackspace}, true
	case 0x01:
		return 1, key{kind: keyCtrlA}, true
	case 0x02:
		return 1, key{kind: keyCtrlB}, true
	case 0x03:
		return 1, key{kind: keyCtrlC}, true
	case 0x04:
		return 1, key{kind: keyCtrlD}, true
	case 0x05:
		return 1, key{kind: keyCtrlE}, true
	case 0x06:
		return 1, key{kind: keyCtrlF}, true
	case 0x07:
		return 1, key{kind: keyCtrlG}, true
	case 0x09:
		return 1, key{kind: keyTab}, true
	case 0x0b:
		return 1, key{kind: keyCtrlK}, true
	case 0x12:
		return 1, key{kind: keyCtrlR}, true
	case 0x15:
		return 1, key{kind: keyCtrlU}, true
	case 0x17:
		return 1, key{kind: keyCtrlW}, true
	case 0x19:
		return 1, key{kind: keyCtrlY}, true
	}
	if b < 0x20 {
		return 1, key{kind: keySkip}, true
	}
	if b < 0x80 {
		return 1, key{kind: keyRune, r: rune(b)}, true
	}
	if !utf8.FullRune(buf) {
		return 0, key{}, false
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError {
		return 1, key{kind: keySkip}, true
	}
	return size, key{kind: keyRune, r: r}, true
}

func decodeEsc(buf []byte) (int, key, bool) {
	if len(buf) == 1 {
		return 0, key{}, false
	}
	switch buf[1] {
	case '[':
		if len(buf) < 3 {
			return 0, key{}, false
		}
		switch buf[2] {
		case 'A':
			return 3, key{kind: keyUp}, true
		case 'B':
			return 3, key{kind: keyDown}, true
		case 'C':
			return 3, key{kind: keyRight}, true
		case 'D':
			return 3, key{kind: keyLeft}, true
		default:
			return 1, key{kind: keyEsc}, true
		}
	case 'O':
		if len(buf) < 3 {
			return 0, key{}, false
		}
		switch buf[2] {
		case 'A':
			return 3, key{kind: keyUp}, true
		case 'B':
			return 3, key{kind: keyDown}, true
		case 'C':
			return 3, key{kind: keyRight}, true
		case 'D':
			return 3, key{kind: keyLeft}, true
		default:
			return 1, key{kind: keyEsc}, true
		}
	case 'b':
		return 2, key{kind: keyAltB}, true
	case 'f':
		return 2, key{kind: keyAltF}, true
	case '.':
		return 2, key{kind: keyAltDot}, true
	default:
		return 1, key{kind: keyEsc}, true
	}
}
