//go:build darwin

package line

import (
	"os"
	"syscall"
	"unsafe"
)

func enterRaw(fd int) (func() error, error) {
	var old syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGETA, uintptr(unsafe.Pointer(&old))); errno != 0 {
		return nil, errno
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.INLCR | syscall.IGNCR
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCSETA, uintptr(unsafe.Pointer(&raw))); errno != 0 {
		return nil, errno
	}
	restore := func() error {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCSETA, uintptr(unsafe.Pointer(&old))); errno != 0 {
			return errno
		}
		return nil
	}
	return restore, nil
}

func IsTerm(f *os.File) bool {
	if f == nil {
		return false
	}
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// termWidth is the terminal's column count, or 0 when fd is not a
// terminal or the size is unknown. Frame asks on every paint, so a resize
// (SIGWINCH) is seen at the next key without a cache to invalidate.
func termWidth(fd int) int {
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0
	}
	return int(ws.Col)
}

// flushInput throws away input typed but not read (tcflush TCIFLUSH):
// confirm asks only after it, so typeahead never answers.
func flushInput(fd int) error {
	which := 1 // FREAD
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCFLUSH, uintptr(unsafe.Pointer(&which))); errno != 0 {
		return errno
	}
	return nil
}
