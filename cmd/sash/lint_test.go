package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --lint runs nothing, takes no grant, and is as strict about its
// arguments as --needs: any other flag, and anything after the source, is
// exit 2 with nothing on stdout.
func TestBinaryLint(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Stdout = &out
		cmd.Stderr = &errb
		code := exitCode(cmd.Run())
		return out.String(), errb.String(), code
	}
	for _, args := range [][]string{
		{"--lint", "--needs", "-c", "echo RAN"},
		{"--needs", "--lint", "-c", "echo RAN"},
		{"--lint", "--session", "-c", "echo RAN"},
		{"--session", "--lint", "-c", "echo RAN"},
		{"--lint", "--plan", "-c", "echo RAN"},
		{"--plan", "--lint", "-c", "echo RAN"},
		{"--lint", "--policy", "-c", "echo RAN"},
		{"--lint", "--bogus", "-c", "echo RAN"},
		{"--lint", "-c", "echo RAN", "x"},
		{"--lint", "-c"},
		{"-c", "echo RAN", "--lint"},
		{"--lint", "-c", "try { ps }"},
	} {
		stdout, stderr, code := run("", args...)
		if code != 2 || stdout != "" || stderr == "" {
			t.Errorf("%q: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	if stdout, _, code := run("", "--lint", "-c", "ps"); code != 0 || stdout != "" {
		t.Fatalf("clean: %d %q", code, stdout)
	}
	if stdout, _, code := run("", "--lint", "-c", "let x = 1"); code != 0 || !strings.Contains(stdout, "unused-binding") {
		t.Fatalf("info: %d %q", code, stdout)
	}
	stdout, _, code := run("", "--lint", "-c", "declare -s t = a\necho $t")
	if code != 1 || !strings.Contains(stdout, "secret-to-sink") || strings.Contains(stdout, "RAN") {
		t.Fatalf("error: %d %q", code, stdout)
	}
	script := filepath.Join(t.TempDir(), "s.sash")
	if err := os.WriteFile(script, []byte("exit 0\necho RAN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		stdin string
		args  []string
	}{{"", []string{"--lint", script}}, {"exit 0\necho RAN\n", []string{"--lint"}}} {
		stdout, _, code := run(c.stdin, c.args...)
		if code != 1 || !strings.Contains(stdout, "unreachable") || strings.Contains(stdout, "RAN\n") {
			t.Fatalf("%q: %d %q", c.args, code, stdout)
		}
	}
}
