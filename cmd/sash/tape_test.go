package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
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

// A session recording over the segment cap is written as <path>.1 ..
// <path>.N through file.write at 0600; --replay names the first and reads
// the rest, and a missing one is refused.
func TestBinaryReplaySegments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	g := grant.Grant{V: "sash/1", Entitlements: []string{"file.read", "file.write"}, FileRead: []string{"/var/log"}, FileWrite: []string{"/tmp"}}
	src := "set -o record /tmp/r.jsonl\nfor i in [ 1..7 ] { cat /var/log/a | wc }"
	big := bytes.Repeat([]byte("x"), 1<<20)
	var writes []parent.Verb
	p := parentFunc(func(v parent.Verb) (parent.Result, error) {
		if v.Key == "file.write" {
			if v.Mode != "0600" {
				t.Errorf("mode %q", v.Mode)
			}
			writes = append(writes, v)
			return parent.Result{Kind: parent.KindBytes}, nil
		}
		return parent.Result{Kind: parent.KindBytes, Bytes: big}, nil
	})
	st := lang.NewState()
	if out := lang.Run(src, g, p, lang.Options{State: st}); out.Status != 0 {
		t.Fatalf("%+v", out.Stderr)
	}
	if fin := lang.FinishRecording(g, p, lang.Options{State: st}); fin.Status != 0 {
		t.Fatalf("%+v", fin.Stderr)
	}
	if len(writes) < 2 {
		t.Fatalf("%d segments", len(writes))
	}
	for i, w := range writes {
		if w.Path != "/tmp/r.jsonl."+strconv.Itoa(i+1) {
			t.Fatalf("segment %d at %s", i+1, w.Path)
		}
		if err := os.WriteFile(filepath.Join(dir, "r.jsonl."+strconv.Itoa(i+1)), w.Body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fx := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fx, []byte(`{"grant": {"entitlements": ["file.read", "file.write"], "file_read": ["/var/log"], "file_write": ["/tmp"]}, "results": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	replay := func() (string, int) {
		var errb bytes.Buffer
		cmd := exec.Command(bin, "--replay", filepath.Join(dir, "r.jsonl.1"), "--fixture", fx, "-c", src)
		cmd.Stdin = strings.NewReader("")
		cmd.Stderr = &errb
		code := exitCode(cmd.Run())
		return errb.String(), code
	}
	if errOut, code := replay(); code != 0 {
		t.Fatalf("replay: %d %q", code, errOut)
	}
	last := filepath.Join(dir, "r.jsonl."+strconv.Itoa(len(writes)))
	if err := os.Remove(last); err != nil {
		t.Fatal(err)
	}
	if errOut, code := replay(); code != 2 || !strings.Contains(errOut, "replay_bad_segment") {
		t.Fatalf("missing segment: %d %q", code, errOut)
	}
}

type parentFunc func(parent.Verb) (parent.Result, error)

func (f parentFunc) Do(v parent.Verb) (parent.Result, error) { return f(v) }
