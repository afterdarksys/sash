package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testFixture = `{
  "grant": {"entitlements": ["ps", "file.read", "unit.restart"], "file_read": ["/var/log"], "file_write": []},
  "results": [
    {"key": "ps", "kind": "rows", "cols": ["user", "cmd"], "rows": [{"user": "root", "cmd": "sshd"}]},
    {"key": "file.read", "bytes": "hello\n"},
    {"key": "unit.restart", "bytes": "ok\n"}
  ]
}`

func TestBinaryRecordReplay(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdin = strings.NewReader("")
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := exitCode(cmd.Run())
		return out.String(), errb.String(), code
	}
	fx := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fx, []byte(testFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	src := "ps\ncat /var/log/a\nunit restart nginx\necho [ uuid() ]"
	rec := filepath.Join(dir, "rec.jsonl")
	out1, errOut, code := run("--record", rec, "--fixture", fx, "-c", src)
	if code != 0 {
		t.Fatalf("record: %d %q", code, errOut)
	}
	if fi, err := os.Stat(rec); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("recording mode %v %v", fi.Mode(), err)
	}
	out2, errOut, code := run("--replay", rec, "--fixture", fx, "-c", src)
	if code != 0 || out1 != out2 || !strings.Contains(out1, "sshd") || !strings.Contains(out1, "hello") {
		t.Fatalf("replay: %d %q\nrecord %q\nreplay %q", code, errOut, out1, out2)
	}
	if _, errOut, code := run("--record", rec, "--fixture", fx, "-c", src); code != 2 || errOut == "" {
		t.Fatalf("overwrote a recording: %d %q", code, errOut)
	}
	if _, errOut, code := run("--replay", rec, "--fixture", fx, "-c", src+"\nps"); code != 2 || !strings.Contains(errOut, "replay_other_script") {
		t.Fatalf("other script: %d %q", code, errOut)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(tampered, bytes.Replace(b, []byte("sshd"), []byte("bash"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := run("--replay", tampered, "--fixture", fx, "-c", src); code != 2 || !strings.Contains(errOut, "replay_bad_chain") {
		t.Fatalf("tampered: %d %q", code, errOut)
	}
	// A different grant changes the requests: the run diverges.
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(strings.Replace(testFixture, `"ps", `, "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := run("--replay", rec, "--fixture", other, "-c", src); code != 2 {
		t.Fatalf("other grant: %d %q", code, errOut)
	}
	for _, args := range [][]string{
		{"--record", filepath.Join(dir, "x1"), "-c", "ps"},
		{"--replay", rec, "-c", "ps"},
		{"--record", filepath.Join(dir, "x2"), "--fixture", fx, "--needs", "-c", "ps"},
		{"--needs", "--record", filepath.Join(dir, "x3"), "--fixture", fx, "-c", "ps"},
		{"--session", "--replay", rec, "--fixture", fx, "-c", src},
		{"--plan", "--record", filepath.Join(dir, "x4"), "--fixture", fx, "-c", "ps"},
		{"--record", filepath.Join(dir, "x5"), "--fixture", fx, "--expires", "1s", "-c", "ps"},
		{"--record", filepath.Join(dir, "x6"), "--fixture", fx, "-c", "ps", "extra"},
		{"--record", filepath.Join(dir, "x7"), "--replay", rec, "--fixture", fx, "-c", "ps"},
		{"--record", filepath.Join(dir, "x8"), "--fixture", fx, "--lint", "-c", "ps"},
		{"--record"},
	} {
		if stdout, stderr, code := run(args...); code != 2 || stdout != "" || stderr == "" {
			t.Errorf("%q: %d %q %q", args, code, stdout, stderr)
		}
	}
	for _, bad := range []string{`{"grant":{"entitlements":["*"]},"results":[]}`, `{"grant":{"file_read":["/"]},"results":[]}`, `{"grant":{},"results":[],"extra":1}`} {
		p := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, code := run("--record", filepath.Join(dir, "bad.jsonl"), "--fixture", p, "-c", "ps"); code != 2 {
			t.Errorf("fixture %s accepted", bad)
		}
	}
}
