//go:build darwin || linux

package connect

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// euid is the effective user; a test replaces it.
var euid = os.Geteuid

func openToken(path string) (string, tokenState) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return "", tokenNone
		}
		return "", tokenRejected
	}
	f := os.NewFile(uintptr(fd), "token")
	token, state := readOpenToken(f)
	if err := f.Close(); err != nil {
		return "", tokenRejected
	}
	return token, state
}

func readOpenToken(f *os.File) (string, tokenState) {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > tokenFileMax || st.Mode().Perm()&0o077 != 0 {
		return "", tokenRejected
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != euid() {
		return "", tokenRejected
	}
	b, err := io.ReadAll(io.LimitReader(f, tokenFileMax+1))
	if err != nil || len(b) > tokenFileMax {
		return "", tokenRejected
	}
	token, ok := tokenText(b)
	if !ok {
		return "", tokenRejected
	}
	return token, tokenOK
}
