//go:build darwin || linux

package main

import (
	"syscall"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/lang"
)

// SIGHUP during a script marks the State hung up (runs stop at the next
// statement, the recording is then written), and sash ends 129 if that
// takes past grace.
func TestWatchHangup(t *testing.T) {
	st := lang.NewState()
	exited := make(chan int, 1)
	stop := watchHangup(st, 50*time.Millisecond, func(code int) { exited <- code })
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 129 || !st.HungUp() {
			t.Fatalf("code %d hung up %v", code, st.HungUp())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit after grace")
	}
}
