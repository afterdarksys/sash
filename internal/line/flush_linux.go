//go:build linux && (386 || amd64 || arm || arm64 || riscv64 || s390x || loong64)

package line

import "syscall"

// tcflsh is TCFLSH on these architectures; the syscall package does not
// export it.
const tcflsh = 0x540B

// flushInput throws away input typed but not read (tcflush TCIFLUSH):
// confirm asks only after it, so typeahead never answers.
func flushInput(fd int) error {
	const tciflush = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflsh, tciflush); errno != 0 {
		return errno
	}
	return nil
}
