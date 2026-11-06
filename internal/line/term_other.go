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
