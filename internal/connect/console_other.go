//go:build !darwin && !linux

package connect

import "os"

// OSConsole has no terminal here: sash connect answers "connect needs a
// terminal".
func OSConsole(in, out, errOut *os.File) Console {
	return Console{
		In: in, Out: out, Err: errOut,
		IsTerm:  func() bool { return false },
		Size:    func() (int, int) { return 24, 80 },
		Raw:     func() (func() error, error) { return nil, os.ErrInvalid },
		Signals: func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() {} },
		Stop:    func() {},
	}
}
