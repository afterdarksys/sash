// Package skills holds the Claude skill for the sash language (the sash
// directory beside this file), embedded so the binary can print it with
// sash --skill. The files sit here, and not under internal, because
// go:embed reads only the package's own directory and below.
//
// Threats: a name that reaches outside the skill (a path, "..", an
// absolute name) or a file read from the working directory instead of the
// binary. Names are one closed set of plain words, checked before any
// lookup, and every byte comes from the embedded copy; nothing here opens
// a host path, runs anything, or grants anything.
package skills

import (
	"embed"
	"errors"
	"io/fs"
	"sort"
)

//go:embed sash
var files embed.FS

const root = "sash"

// entry is the skill's entry file; references are reference/<name>.md.
const entry = "SKILL.md"

// ErrName is a reference name that is not one of References.
var ErrName = errors.New("no such skill file")

// Main returns SKILL.md.
func Main() ([]byte, error) {
	return files.ReadFile(root + "/" + entry)
}

// References are the names Reference takes: each is reference/<name>.md.
func References() []string {
	return []string{"features", "language", "verbs"}
}

// Reference returns reference/<name>.md. The name is a bare word from
// References: no ".md", no "reference/", no path.
func Reference(name string) ([]byte, error) {
	for _, n := range References() {
		if n == name {
			return files.ReadFile(root + "/reference/" + name + ".md")
		}
	}
	return nil, ErrName
}

// List returns every embedded file, relative to the skill directory, in
// sorted order: SKILL.md and reference/<name>.md.
func List() ([]string, error) {
	sub, err := fs.Sub(files, root)
	if err != nil {
		return nil, err
	}
	var out []string
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
