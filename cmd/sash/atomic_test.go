package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// The recording appears whole at its final name or not at all.
func TestAtomicRecordingWrite(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "rec.jsonl")
	a := &atomicFile{path: final}
	if n, err := a.Write([]byte("whole\n")); err != nil || n != 6 {
		t.Fatalf("%d %v", n, err)
	}
	b, err := os.ReadFile(final)
	fi, serr := os.Stat(final)
	if err != nil || serr != nil || string(b) != "whole\n" || fi.Mode().Perm() != 0o600 || len(dirNames(t, dir)) != 1 {
		t.Fatalf("final %q %v names %v", b, fi.Mode(), dirNames(t, dir))
	}
	if _, err := a.Write([]byte("again")); err == nil {
		t.Fatal("second write")
	}

	// Killed after the temp is written, before the link: no final file.
	dir = t.TempDir()
	final = filepath.Join(dir, "rec.jsonl")
	a = &atomicFile{path: final, beforeLink: func(tmp string) error {
		if b, err := os.ReadFile(tmp); err != nil || string(b) != "whole\n" {
			t.Errorf("temp %q %v", b, err)
		}
		return errors.New("killed")
	}}
	if _, err := a.Write([]byte("whole\n")); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(final); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a final file after a kill: %v", err)
	}
	for _, n := range dirNames(t, dir) {
		if !strings.HasPrefix(n, ".rec.jsonl.tmp-") {
			t.Fatalf("unexpected %q", n)
		}
	}

	// The final name appears meanwhile: link refuses, the other file is
	// untouched, and the temp is removed.
	dir = t.TempDir()
	final = filepath.Join(dir, "rec.jsonl")
	a = &atomicFile{path: final, beforeLink: func(string) error { return os.WriteFile(final, []byte("theirs"), 0o600) }}
	if _, err := a.Write([]byte("mine\n")); err == nil {
		t.Fatal("overwrote")
	}
	if b, _ := os.ReadFile(final); string(b) != "theirs" || len(dirNames(t, dir)) != 1 {
		t.Fatalf("final %q names %v", b, dirNames(t, dir))
	}
}
