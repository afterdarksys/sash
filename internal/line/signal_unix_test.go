//go:build darwin || linux

package line

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"
)

// SIGTERM and SIGHUP restore the terminal (bracketed paste off, mode
// back) once, then end the process by the signal.
func TestSignalRestoresTerminal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		restores := 0
		var out bytes.Buffer
		g := &termGuard{restore: func() error { restores++; return nil }, out: &out}
		got := make(chan os.Signal, 1)
		stop := watchSignals(g.Restore, func(s os.Signal, err error) {
			if err != nil {
				t.Error(err)
			}
			got <- s
		})
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case s := <-got:
			if s != sig {
				t.Fatalf("got %v", s)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%v not handled", sig)
		}
		stop()
		if restores != 1 || out.String() != "\x1b[?2004l" {
			t.Fatalf("%v: not restored before exit: restores %d out %q", sig, restores, out.String())
		}
		// Loop's own restore on the way out is then harmless.
		if err := g.Restore(); err != nil || restores != 1 || out.String() != "\x1b[?2004l" {
			t.Fatalf("%v: second restore: %v %d %q", sig, err, restores, out.String())
		}
	}
}

// Under nohup SIGHUP is ignored, so the re-raised signal does nothing:
// reraise then exits 128+signal itself instead of running on. The test
// makes the kill land nowhere, as an ignored signal does.
func TestReraiseBackstopWhenIgnored(t *testing.T) {
	oldExit, oldKill, oldGrace := exitFn, killFn, signalGrace
	defer func() { exitFn, killFn, signalGrace = oldExit, oldKill, oldGrace }()
	signalGrace = 10 * time.Millisecond
	killed := 0
	killFn = func(int, syscall.Signal) error { killed++; return nil }
	code := -1
	exitFn = func(c int) { code = c }
	reraise(syscall.SIGHUP, nil)
	if killed != 1 || code != 128+int(syscall.SIGHUP) {
		t.Fatalf("killed %d exit %d", killed, code)
	}
}
