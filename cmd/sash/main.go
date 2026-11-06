// Command sash is the non-interactive entry for commit 1.
// The live prompt arrives with the line editor. A hand start has an empty
// grant, so toolkit verbs are denied and filters still run.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

type closedParent struct{}

func (closedParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{}, fmt.Errorf("no parent")
}

func main() {
	src, err := readSource(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(langExit(2))
	}
	got := lang.Run(src, grant.Grant{}, closedParent{}, lang.Options{})
	fmt.Fprint(os.Stdout, got.Stdout)
	fmt.Fprint(os.Stderr, got.Stderr)
	os.Exit(langExit(got.Status))
}

func readSource(args []string) (string, error) {
	if len(args) >= 2 && args[0] == "-c" {
		return args[1], nil
	}
	if len(args) == 1 {
		b, err := os.ReadFile(args[0])
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if len(args) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", fmt.Errorf("usage: sash [-c script | file]")
}

func langExit(code int) int {
	if code < 0 {
		return 1
	}
	return code
}
