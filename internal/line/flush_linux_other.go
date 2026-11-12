//go:build linux && !(386 || amd64 || arm || arm64 || riscv64 || s390x || loong64)

package line

import "errors"

// flushInput is not wired for this architecture's TCFLSH, so confirm
// always answers no.
func flushInput(fd int) error { return errors.New("no flush") }
