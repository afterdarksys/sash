package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// --skill prints the embedded skill files byte for byte, needs no grant,
// reads nothing from the working directory or the session descriptors,
// and refuses every other flag, a script, a file, and any name but the
// three bare reference names.
func TestBinarySkill(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "skills", "sash"))
	if err != nil {
		t.Fatal(err)
	}
	// A working directory with its own, different skill: the embedded
	// one must still be printed.
	cwd := t.TempDir()
	decoy := filepath.Join(cwd, "skills", "sash", "reference")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"SKILL.md", "reference/verbs.md", "reference/language.md", "reference/features.md", "reference/extra.md"} {
		if err := os.WriteFile(filepath.Join(cwd, "skills", "sash", f), []byte("DECOY\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, string, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var out, errb bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = cwd
		cmd.Stdin = strings.NewReader("echo RAN\n")
		cmd.Stdout = &out
		cmd.Stderr = &errb
		// fds 3-6 are pipes whose writers stay open: reading any of them
		// (as --session does) would block until the timeout.
		var keep []*os.File
		for i := 0; i < 4; i++ {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.ExtraFiles = append(cmd.ExtraFiles, r)
			keep = append(keep, r, w)
		}
		defer func() {
			for _, f := range keep {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			}
		}()
		code := exitCode(cmd.Run())
		if ctx.Err() != nil {
			t.Fatalf("%q did not finish: it read a descriptor or stdin", args)
		}
		return out.String(), errb.String(), code
	}

	var files []string
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if stdout, stderr, code := run("--skill", "--list"); code != 0 || stdout != strings.Join(files, "\n")+"\n" || stderr != "" {
		t.Fatalf("--list: %d %q %q, want %q", code, stdout, stderr, files)
	}
	for _, f := range files {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--skill"}
		if f != "SKILL.md" {
			args = append(args, strings.TrimSuffix(strings.TrimPrefix(f, "reference/"), ".md"))
		}
		stdout, stderr, code := run(args...)
		if code != 0 || stdout != string(want) || stderr != "" {
			t.Errorf("%q: code %d stderr %q, stdout equal to %s: %v", args, code, stderr, f, stdout == string(want))
		}
	}
	script := filepath.Join(t.TempDir(), "s.sash")
	if err := os.WriteFile(script, []byte("echo RAN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fx := filepath.Join(t.TempDir(), "fx.json")
	if err := os.WriteFile(fx, []byte(`{"grant":{"entitlements":["ps"]},"results":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(t.TempDir(), "rec.jsonl")
	for _, args := range [][]string{
		{"--skill", "--session"}, {"--session", "--skill"},
		{"--skill", "--plan"}, {"--plan", "--skill"},
		{"--skill", "-c", "echo RAN"}, {"-c", "echo RAN", "--skill"}, {"-c", "--skill"},
		{"--skill", "--lint"}, {"--lint", "--skill"}, {"--lint", "--skill", "-c", "echo RAN"},
		{"--skill", "--needs"}, {"--needs", "--skill", "-c", "echo RAN"}, {"--needs", "--policy", "--skill"},
		{"--skill", "--record", rec, "--fixture", fx, "-c", "echo RAN"},
		{"--record", rec, "--fixture", fx, "--skill", "-c", "echo RAN"},
		{"--skill", "--replay", rec, "--fixture", fx, "-c", "echo RAN"},
		{"--skill", script}, {script, "--skill"},
		{"--skill", "--list", "verbs"}, {"--skill", "verbs", "language"}, {"--skill", "--skill"},
		{"--skill", "--bogus"}, {"--list", "--skill"},
		// Hostile and other-spelling names.
		{"--skill", ""}, {"--skill", ".."}, {"--skill", "../SKILL.md"}, {"--skill", "../../go.mod"},
		{"--skill", "/etc/passwd"}, {"--skill", "/"}, {"--skill", "reference/verbs"}, {"--skill", "reference/verbs.md"},
		{"--skill", "verbs.md"}, {"--skill", "SKILL.md"}, {"--skill", "SKILL"}, {"--skill", "extra"},
		{"--skill", "Verbs"}, {"--skill", "verbs/"}, {"--skill", "./verbs"}, {"--skill", "reference"}, {"--skill", "-"},
	} {
		stdout, stderr, code := run(args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "usage") || strings.Contains(stdout+stderr, "RAN") || strings.Contains(stdout+stderr, "DECOY") {
			t.Errorf("%q: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	if _, err := os.Lstat(rec); !os.IsNotExist(err) {
		t.Fatalf("a refused --skill wrote a recording: %v", err)
	}
	// The usage line names --skill.
	if _, stderr, code := run("--bogus"); code != 2 || !strings.Contains(stderr, "sash --skill") {
		t.Fatalf("usage: %d %q", code, stderr)
	}
}
