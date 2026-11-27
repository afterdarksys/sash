package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// is_tty() is true only where a person can be asked: the script is not on
// stdin, and stdin and stderr are both terminals.
func TestBinaryIsTTY(t *testing.T) {
	bin := ptyBin(t)
	const src = "echo tty [ is_tty() ]\n"
	file := filepath.Join(t.TempDir(), "s.sash")
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		run  ptyRun
		want string
	}{
		{"-c at a terminal", ptyRun{Argv: []string{bin, "-c", src}}, "tty true"},
		{"a file at a terminal", ptyRun{Argv: []string{bin, file}}, "tty true"},
		{"stdout piped, still a terminal to ask", ptyRun{Argv: []string{bin, "-c", src}, Stdout: "pipe"}, "tty true"},
		{"stdin is a pipe", ptyRun{Argv: []string{bin, "-c", src}, Stdin: "pipe"}, "tty false"},
		{"stdin is the script", ptyRun{Argv: []string{bin}, Stdin: "pipe", Input: src}, "tty false"},
		{"stderr is not a terminal", ptyRun{Argv: []string{bin, file}, Stderr: "pipe"}, "tty false"},
	} {
		rep := runPTY(t, c.run)
		if got := rep.TTY + rep.Stdout; rep.Code != 0 || !strings.Contains(got, c.want) {
			t.Errorf("%s: code %d, want %q, got tty %q stdout %q stderr %q", c.name, rep.Code, c.want, rep.TTY, rep.Stdout, rep.Stderr)
		}
	}
}
