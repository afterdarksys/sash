package skills

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The embedded copy is the directory on disk, file for file and byte for
// byte, and References names exactly the reference files.
func TestEmbedMatchesDisk(t *testing.T) {
	var disk []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			disk = append(disk, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(disk)
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(disk, "\n") {
		t.Fatalf("embedded %q, disk %q", got, disk)
	}
	var refs []string
	for _, f := range disk {
		if name, ok := strings.CutPrefix(f, "reference/"); ok {
			refs = append(refs, strings.TrimSuffix(name, ".md"))
		} else if f != entry {
			t.Errorf("unexpected file %q", f)
		}
	}
	if strings.Join(refs, " ") != strings.Join(References(), " ") {
		t.Fatalf("references %q, disk %q", References(), refs)
	}
	for _, f := range disk {
		want, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		var b []byte
		if f == entry {
			b, err = Main()
		} else {
			b, err = Reference(strings.TrimSuffix(strings.TrimPrefix(f, "reference/"), ".md"))
		}
		if err != nil || !bytes.Equal(b, want) {
			t.Errorf("%s: embedded copy differs (%v)", f, err)
		}
	}
}

// Only the bare names resolve: one spelling, no path.
func TestReferenceRefusesOtherNames(t *testing.T) {
	for _, n := range []string{"", "language.md", "reference/language", "reference/language.md", "../SKILL.md", "/etc/passwd",
		"SKILL", "SKILL.md", ".", "..", "Language", "language/", "./language", "verbs\x00", "reference"} {
		if b, err := Reference(n); !errors.Is(err, ErrName) || b != nil {
			t.Errorf("%q: %v %d bytes", n, err, len(b))
		}
	}
}

// install.sh copies exactly the embedded files, resolves a relative target
// against the caller's directory, overwrites only its own files, and
// refuses a symlink, a hard link, or a directory where a file goes.
func TestInstallScript(t *testing.T) {
	script, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	same := func(dir string) {
		t.Helper()
		for _, f := range names {
			want, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "sash", filepath.FromSlash(f)))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: installed copy differs (%v)", f, err)
			}
		}
	}
	cwd := t.TempDir()
	out, code := run(cwd, "rel")
	if code != 0 || strings.Count(out, "wrote ") != len(names) || !strings.Contains(out, "wrote "+filepath.Join(cwd, "rel", "sash", "SKILL.md")) {
		t.Fatalf("relative target: %d %q", code, out)
	}
	same(filepath.Join(cwd, "rel"))
	// A second run overwrites its own files and leaves another alone.
	extra := filepath.Join(cwd, "rel", "sash", "reference", "mine.md")
	if err := os.WriteFile(extra, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "rel", "sash", "SKILL.md"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := run(cwd, filepath.Join(cwd, "rel")); code != 0 {
		t.Fatalf("rerun: %d %q", code, out)
	}
	same(filepath.Join(cwd, "rel"))
	if b, err := os.ReadFile(extra); err != nil || string(b) != "mine\n" {
		t.Fatalf("another file changed: %q %v", b, err)
	}

	elsewhere := t.TempDir()
	victim := filepath.Join(elsewhere, "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(dest string){
		"symlinked target": func(dest string) { must(t, os.Symlink(elsewhere, dest)) },
		"symlinked sash": func(dest string) {
			must(t, os.Mkdir(dest, 0o700))
			must(t, os.Symlink(elsewhere, filepath.Join(dest, "sash")))
		},
		"symlinked reference": func(dest string) {
			must(t, os.MkdirAll(filepath.Join(dest, "sash"), 0o700))
			must(t, os.Symlink(elsewhere, filepath.Join(dest, "sash", "reference")))
		},
		"symlinked file": func(dest string) {
			must(t, os.MkdirAll(filepath.Join(dest, "sash", "reference"), 0o700))
			must(t, os.Symlink(victim, filepath.Join(dest, "sash", "reference", "verbs.md")))
		},
		"hard-linked file": func(dest string) {
			must(t, os.MkdirAll(filepath.Join(dest, "sash", "reference"), 0o700))
			must(t, os.Link(victim, filepath.Join(dest, "sash", "SKILL.md")))
		},
		"directory for a file": func(dest string) {
			must(t, os.MkdirAll(filepath.Join(dest, "sash", "reference", "language.md"), 0o700))
		},
		"target is a file": func(dest string) { must(t, os.WriteFile(dest, []byte("x"), 0o600)) },
	}
	for name, setup := range cases {
		dir := t.TempDir()
		setup(filepath.Join(dir, "skills"))
		out, code := run(dir, "skills")
		if code != 1 || strings.Contains(out, "wrote ") {
			t.Errorf("%s: %d %q", name, code, out)
		}
		if b, err := os.ReadFile(victim); err != nil || string(b) != "keep\n" {
			t.Fatalf("%s: wrote through to %s: %q %v", name, victim, b, err)
		}
		if ents, err := os.ReadDir(elsewhere); err != nil || len(ents) != 1 {
			t.Fatalf("%s: wrote into the link target: %v %v", name, ents, err)
		}
	}
	if out, code := run(cwd, "a", "b"); code != 2 {
		t.Fatalf("two targets: %d %q", code, out)
	}
	if out, code := run(cwd, ""); code != 2 {
		t.Fatalf("empty target: %d %q", code, out)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
