//go:build !darwin && !linux

package line

import (
	"errors"
	"os"
)

func enterRaw(fd int) (func() error, error) {
	return nil, errors.New("sash line failed")
}

func IsTerm(f *os.File) bool {
	return false
}

// termWidth is unknown here, so the right prompt is never drawn.
func termWidth(fd int) int { return 0 }

// flushInput cannot flush here, so confirm always answers no.
func flushInput(fd int) error { return errors.New("no flush") }
