// Command sash runs one script. --session reads the grant and verb pipes.
// A terminal with no script is the live prompt. Non-tty stdin is
// still one script. A hand start, without --session, has an empty grant:
// toolkit verbs are denied and filters still run. --needs reads a script
// and prints what it would ask for; it runs nothing.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/line"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/session"
)

type closedParent struct{}

func (closedParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{}, fmt.Errorf("no parent")
}

func main() {
	args := os.Args[1:]
	if needsMode(args) {
		os.Exit(runNeeds(args))
	}
	g := grant.Grant{}
	var p parent.Parent = closedParent{}
	if len(args) > 0 && args[0] == "--session" {
		args = args[1:]
		var err error
		g, p, err = session.Open(os.NewFile(3, "grant"), os.NewFile(4, "fleet"), os.NewFile(5, "emergency"), os.NewFile(6, "verb"), time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(2)
		}
	}
	// --plan runs a script with mutating verbs stopped, locked on.
	st := lang.NewState()
	plan := len(args) > 0 && args[0] == "--plan"
	if plan {
		args = args[1:]
		st.LockPlan()
	}
	if len(args) == 0 && line.IsTerm(os.Stdin) {
		if plan {
			fmt.Fprintln(os.Stderr, "usage: sash --plan needs a script")
			os.Exit(2)
		}
		os.Exit(line.Loop(os.Stdin, os.Stdout, os.Stderr, g, p))
	}
	src, err := readSource(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(langExit(2))
	}
	got := lang.Run(src, g, p, lang.Options{State: st})
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
	return "", fmt.Errorf(usage)
}

const usage = "usage: sash [--session] [--plan] [-c script | file]; sash --needs [--policy] [-c script | file]"

// needsMode is true when --needs is among the leading flags.
func needsMode(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			return false
		}
		if a == "--needs" {
			return true
		}
	}
	return false
}

// runNeeds prints the script's grant manifest. It calls no parent and
// takes no grant, so --session and --plan are refused.
func runNeeds(args []string) int {
	policy := false
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--needs":
		case "--policy":
			policy = true
		default:
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		args = args[1:]
	}
	if len(args) == 0 && line.IsTerm(os.Stdin) {
		fmt.Fprintln(os.Stderr, "usage: sash --needs needs a script")
		return 2
	}
	src, err := readSource(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	got := lang.Needs(src, lang.NeedsOptions{Policy: policy})
	fmt.Fprint(os.Stdout, got.Stdout)
	fmt.Fprint(os.Stderr, got.Stderr)
	return got.Status
}

func langExit(code int) int {
	if code < 0 {
		return 1
	}
	return code
}
