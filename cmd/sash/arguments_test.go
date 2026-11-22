package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryScriptArguments(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sash")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	src := `args { service string required; wait duration default 30s }; echo $service $wait`
	script := filepath.Join(t.TempDir(), "inspect.sash")
	if err := os.WriteFile(script, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
		fail bool
	}{
		{[]string{script, "--", "--service", "--skill"}, "--skill 30s\n", false},
		{[]string{script, "--", "--service", "nginx"}, "nginx 30s\n", false},
		{[]string{"-c", src, "--", "--service", "sshd", "--wait", "1m"}, "sshd 1m\n", false},
		{[]string{script, "--", "--help"}, "--service <string> (required)", false},
		{[]string{script}, "args: missing required --service", true},
		{[]string{script, "--", "--service", "nginx", "--wait", "no"}, "args: --wait invalid duration", true},
		{[]string{"-c", "echo hi", "--", "--unknown", "value"}, "args: script has no argument declaration", true},
	} {
		out, err := exec.Command(bin, tc.args...).CombinedOutput()
		if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
			t.Fatalf("args %q: err %v output %q", tc.args, err, out)
		}
	}
	cmd := exec.Command(bin, "--", "--service", "nginx")
	cmd.Stdin = strings.NewReader(src)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "nginx 30s\n" {
		t.Fatalf("stdin: %v %q", err, out)
	}

}
