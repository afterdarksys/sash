package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// --needs runs nothing and takes no grant: --session and --plan are
// refused, a parse error prints nothing on stdout, and the exit says
// whether the manifest is complete.
func TestBinaryNeeds(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdin = bytes.NewReader(nil)
		cmd.Stdout = &out
		cmd.Stderr = &errb
		code := exitCode(cmd.Run())
		return out.String(), errb.String(), code
	}
	cases := []struct {
		args   []string
		code   int
		stdout string
	}{
		{[]string{"--needs", "--session", "-c", "ps"}, 2, ""},
		{[]string{"--session", "--needs", "-c", "ps"}, 2, ""},
		{[]string{"--needs", "--plan", "-c", "ps"}, 2, ""},
		{[]string{"--plan", "--needs", "-c", "ps"}, 2, ""},
		{[]string{"--needs", "--bogus", "-c", "ps"}, 2, ""},
		{[]string{"--needs", "-c", "cat ("}, 2, ""},
		{[]string{"--needs", "-c", "try { ps }"}, 2, ""},
		// Nothing after the source, and --needs or --policy out of
		// place, is a usage error: the script never runs.
		{[]string{"-c", "echo RAN", "--needs"}, 2, ""},
		{[]string{"-c", "echo RAN", "--policy"}, 2, ""},
		{[]string{"-c", "echo RAN", "x"}, 2, ""},
		{[]string{"--needs", "-c", "echo RAN", "--policy"}, 2, ""},
		{[]string{"--policy", "-c", "echo RAN"}, 2, ""},
		{[]string{"--policy"}, 2, ""},
		{[]string{"--plan", "-c", "echo RAN", "--needs"}, 2, ""},
		{[]string{"-c"}, 2, ""},
		{[]string{"--needs", "-c", "unit status nginx"}, 0, "key          target  mutates  lines  note\nunit.status  nginx   no       1      \n"},
		{[]string{"--needs", "--policy", "-c", "unit status nginx; journal"}, 1, "--service-status nginx\n# journal -: no policy flag grants this (lines 1)\n"},
	}
	for _, c := range cases {
		stdout, stderr, code := run(c.args...)
		if code != c.code || stdout != c.stdout {
			t.Errorf("%q: code %d stdout %q stderr %q", c.args, code, stdout, stderr)
		}
		if c.code == 2 && stderr == "" {
			t.Errorf("%q: no usage on stderr", c.args)
		}
	}
	script := filepath.Join(t.TempDir(), "s.sash")
	if err := os.WriteFile(script, []byte("cat /etc/hosts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stdout, _, code := run("--needs", "--policy", script); code != 0 || stdout != "--file-read /etc\n" {
		t.Fatalf("file: code %d stdout %q", code, stdout)
	}
	for _, args := range [][]string{{script, "--needs"}, {"--needs", script, "--policy"}, {script, script}} {
		if stdout, _, code := run(args...); code != 2 || stdout != "" {
			t.Fatalf("%q: code %d stdout %q", args, code, stdout)
		}
	}
	if stdout, _, code := run("-c", "echo RAN"); code != 0 || stdout != "RAN\n" {
		t.Fatalf("plain run: code %d stdout %q", code, stdout)
	}
}
