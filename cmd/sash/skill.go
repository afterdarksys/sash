package main

// sash --skill prints the Claude skill for the sash language, embedded in
// the binary (package skills), so a person can install it with
// sash --skill > ~/.claude/skills/sash/SKILL.md.
//
// Threats: a print command that turns into something else. --skill owns
// the whole command line: any other flag, a script, or a file is a usage
// error before anything is read, so it never opens the session
// descriptors (3-6), reads no grant, runs no script, and reads nothing
// from disk or the working directory; every byte is the embedded copy. A
// name is one of three bare words, so a path, "..", or an absolute name
// cannot reach any other file. It writes only to stdout.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/afterdarksys/sash/skills"
)

const skillUsage = "usage: sash --skill [features | language | verbs]; sash --skill --list"

// skillMode is true when --skill is any argument: runSkill then owns the
// command line and refuses everything else.
func skillMode(args []string) bool {
	for _, a := range args {
		if a == "--skill" {
			return true
		}
	}
	return false
}

// runSkill prints SKILL.md, one reference file, or the list of files.
func runSkill(args []string, out io.Writer) int {
	var b []byte
	var err error
	switch {
	case len(args) == 1 && args[0] == "--skill":
		b, err = skills.Main()
	case len(args) == 2 && args[0] == "--skill" && args[1] == "--list":
		var names []string
		if names, err = skills.List(); err == nil {
			b = []byte(strings.Join(names, "\n") + "\n")
		}
	case len(args) == 2 && args[0] == "--skill":
		if b, err = skills.Reference(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, skillUsage)
			return 2
		}
	default:
		fmt.Fprintln(os.Stderr, skillUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "skill: embedded files unreadable")
		return 1
	}
	if _, err := out.Write(b); err != nil {
		fmt.Fprintln(os.Stderr, "skill: write failed")
		return 1
	}
	return 0
}
