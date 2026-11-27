//go:build darwin || linux

package line

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// watchSignals restores the terminal on SIGTERM or SIGHUP, then hands the
// signal to exit, so the process still ends as the signal says. stop
// undoes the watch.
func watchSignals(restore func() error, exit func(os.Signal, error)) (stop func()) {
	return watchSignalsOf(restore, exit, syscall.SIGTERM, syscall.SIGHUP)
}

// watchSignalsOf is watchSignals for the signals named.
func watchSignalsOf(restore func() error, exit func(os.Signal, error), sigs ...os.Signal) (stop func()) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, sigs...)
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
	code := 1
	if s, ok := sig.(syscall.Signal); ok {
		code = 128 + int(s)
		if err := killFn(os.Getpid(), s); err == nil {
			// An ignored signal (SIGHUP under nohup) returns here; give
			// it a moment to land, then end as a shell would report it.
			time.Sleep(signalGrace)
		}
	}
	exitFn(code)
}

// isHangup is SIGHUP: an ssh disconnect.
func isHangup(sig os.Signal) bool { return sig == syscall.SIGHUP }

// signalGrace is how long reraise waits for the re-raised signal before
// its backstop exit; killFn and exitFn are replaced in tests.
var (
	signalGrace = 100 * time.Millisecond
	killFn      = syscall.Kill
	exitFn      = os.Exit
)

// askEnds are the signals that end sash while a script-mode question
// holds raw mode: the terminal is restored first, then the signal's own
// end. In raw mode Ctrl-C and Ctrl-\ are keys, so these come from kill.
var askEnds = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT}

// watchStop handles SIGTSTP while a question holds raw mode: suspend
// restores the terminal, the process stops (SIGSTOP, which a shell
// reports as a stopped job), and when it is continued (SIGCONT) resume
// enters raw mode again before the read goes on. stop undoes the watch.
func watchStop(suspend func() error, resume func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, syscall.SIGTSTP)
	go func() {
		for {
			select {
			case <-ch:
				if err := suspend(); err != nil {
					if _, werr := os.Stderr.WriteString("sash: terminal not restored\n"); werr != nil {
						exitFn(1)
					}
				}
				if err := killFn(os.Getpid(), syscall.SIGSTOP); err != nil {
					exitFn(1)
				}
				// Continued. resume keeps its own failure for the
				// question, which then fails closed.
				resume()
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
