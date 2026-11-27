//go:build darwin || linux

package connect

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/afterdarksys/sash/internal/line"
)

func init() { sigResize, sigStop, sigQuit = syscall.SIGWINCH, syscall.SIGTSTP, syscall.SIGQUIT }

// OSConsole is the person's terminal on the process's own descriptors.
func OSConsole(in, out, errOut *os.File) Console {
	return Console{
		In: in, Out: out, Err: errOut,
		IsTerm: func() bool { return line.IsTerm(in) && line.IsTerm(out) && line.IsTerm(errOut) },
		Size:   func() (int, int) { return line.TermSize(in) },
		Raw:    func() (func() error, error) { return line.RawMode(in, out) },
		Signals: func() (<-chan os.Signal, func()) {
			ch := make(chan os.Signal, 8)
			signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGWINCH)
			return ch, func() { signal.Stop(ch) }
		},
		// SIGSTOP, as a shell's job control expects; the call returns
		// when the process is continued.
		Stop: func() {
			if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
				return
			}
		},
	}
}
