//go:build !darwin && !linux

package connect

// openToken reads no token here: this platform has neither the permission
// bits nor a way to refuse a symlink, and sash connect needs a terminal
// it does not have here either.
func openToken(string) (string, tokenState) { return "", tokenRejected }
