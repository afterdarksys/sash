package skills

import (
	"bytes"
	"errors"
	"os"
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
