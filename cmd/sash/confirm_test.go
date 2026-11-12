package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sash file, -c, and stdin have no terminal to ask: set -o confirm is
// exit 2 confirm needs a terminal, and nothing after it runs.
func TestBinaryConfirmNeedsATerminal(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "s.sash")
	src := "set -o confirm\necho RAN\n"
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		stdin string
		args  []string
	}{{"", []string{"-c", src}}, {"", []string{script}}, {src, nil}, {"", []string{"--plan", "-c", src}}} {
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, c.args...)
		cmd.Stdin = strings.NewReader(c.stdin)
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := exitCode(cmd.Run())
		if code != 2 || out.String() != "" || !strings.Contains(errb.String(), "confirm needs a terminal") {
			t.Errorf("%q: code %d stdout %q stderr %q", c.args, code, out.String(), errb.String())
		}
	}
}
