//go:build !darwin && !linux

package line

import "os"

// watchSignals is a no-op here: Loop never enters raw mode on these
// platforms.
func watchSignals(restore func() error, exit func(os.Signal, error)) (stop func()) {
	return func() {}
}

func reraise(os.Signal, error) { os.Exit(1) }
