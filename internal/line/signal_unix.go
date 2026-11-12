//go:build darwin || linux

package line

import (
	"os"
	"os/signal"
	"syscall"
)

// watchSignals restores the terminal on SIGTERM or SIGHUP, then hands the
// signal to exit, so the process still ends as the signal says. stop
// undoes the watch.
func watchSignals(restore func() error, exit func(os.Signal, error)) (stop func()) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case sig := <-ch:
			exit(sig, restore())
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// reraise ends the process by sig's default action, after saying on
// stderr when the terminal could not be restored.
func reraise(sig os.Signal, restoreErr error) {
	if restoreErr != nil {
		if _, err := os.Stderr.WriteString("sash: terminal not restored\n"); err != nil {
			os.Exit(1)
		}
	}
	signal.Reset(sig)
	if s, ok := sig.(syscall.Signal); ok {
		if err := syscall.Kill(os.Getpid(), s); err == nil {
			return
		}
	}
	os.Exit(1)
}
