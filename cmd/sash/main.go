// Command sash runs one script. --session reads the grant and verb pipes.
// A terminal with no script is the live prompt. Non-tty stdin is
// still one script. A hand start, without --session, has an empty grant:
// toolkit verbs are denied and filters still run. --needs reads a script
// and prints what it would ask for; it runs nothing. --lint reads a script
// and prints advisory findings; it runs nothing either. --record and
// --replay run a script offline against a fixture or a recording (tape.go).
// --skill prints the embedded Claude skill for the language (skill.go).
// version --json and codes --json print what the build is and the catalog
// of what it prints (version.go).
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	if skillMode(args) {
		os.Exit(runSkill(args, os.Stdout))
	}
	if infoMode(args) {
		os.Exit(runInfo(args, os.Stdout, os.Stderr))
	}
	if flagMode(args, "--lint") {
		os.Exit(runLint(args))
	}
	if tapeMode(args) {
		os.Exit(runTape(args))
	}
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
			fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
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
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		os.Exit(langExit(2))
	}
	stopHup := watchHangup(st, hupGrace, os.Exit)
	got := lang.Run(src, g, p, lang.Options{State: st, Interrupts: sigInterrupts, Term: scriptTerm(args)})
	emit(os.Stdout, got.Stdout)
	emit(os.Stderr, got.Stderr)
	// A recording set -o record started is written when the script ends,
	// and after a hangup, which stops the run at its next statement.
	fin := lang.FinishRecording(g, p, lang.Options{State: st, Prefix: got.Prefix})
	stopHup()
	emit(os.Stdout, fin.Stdout)
	emit(os.Stderr, fin.Stderr)
	if got.Status == 0 && fin.Status != 0 {
		got.Status = fin.Status
	}
	if st.HungUp() {
		got.Status = 129
	}
	os.Exit(langExit(got.Status))
}

// sigInterrupts is lang.Options.Interrupts for a script: SIGINT is caught
// only while a par waits for its branches (the first cancels them, a
// second stops the wait); outside par it ends sash as before.
func sigInterrupts() (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt)
	ch := make(chan struct{}, 2)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-sig:
				select {
				case ch <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return ch, func() {
		signal.Stop(sig)
		close(done)
		<-stopped
	}
}

// hupGrace bounds the write of a session recording after a hangup.
const hupGrace = 5 * time.Second

// watchHangup catches SIGHUP (most ssh disconnects) while a script runs:
// the run stops at its next statement or verb, so the recording can still
// be written through the verb socket. If that takes past grace, sash ends
// (129) without it: best effort, never a hang. stop ends the watch.
func watchHangup(st *lang.State, grace time.Duration, exit func(int)) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			st.HangUp()
			time.AfterFunc(grace, func() { exit(129) })
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}

// readSource takes exactly one source: -c script, one file, or stdin.
// Anything after the source, or a flag where the source goes (--needs or
// --policy out of place), is a usage error and nothing runs.
func readSource(args []string) (string, error) {
	switch {
	case len(args) == 2 && args[0] == "-c":
		return args[1], nil
	case len(args) == 1 && !strings.HasPrefix(args[0], "-"):
		b, err := os.ReadFile(args[0])
		if err != nil {
			return "", err
		}
		return string(b), nil
	case len(args) == 0:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", errors.New(usage)
}

// emit writes a run's output to f. Threats: a file body, a journal line,
// or a process name that repaints the screen, sets the title, or makes
// the terminal answer a query. When f is a terminal the text goes through
// lang.TermText, at this last write; stdout and stderr are judged each on
// its own. A pipe or a file receives the bytes unchanged, so this does
// not protect whoever reads those.
func emit(f *os.File, s string) {
	if line.IsTerm(f) {
		s = lang.TermText(s)
	}
	fmt.Fprint(f, s)
}

// scriptTerm is the terminal a script may ask (is_tty, term): only when
// the script came from -c or a file, so stdin is not the script, and stdin
// and stderr are both terminals. sash < script and a pipe have none.
func scriptTerm(args []string) lang.Term {
	if len(args) == 0 {
		return nil
	}
	return line.ScriptTerm(os.Stdin, os.Stderr)
}

const usage = "usage: sash [--session] [--plan] [-c script | file]; sash --needs [--policy] [-c script | file]; sash --lint [-c script | file]; sash --skill [features | language | verbs]; sash --skill --list; sash version --json; sash codes --json"

// needsMode is true when --needs is among the leading flags.
func needsMode(args []string) bool { return flagMode(args, "--needs") }

// flagMode is true when flag is among the leading flags.
func flagMode(args []string, flag string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			return false
		}
		if a == flag {
			return true
		}
	}
	return false
}

// runLint prints the script's lint findings. It calls no parent and takes
// no grant, so every other flag (--needs, --session, --plan, --policy) is
// refused, and so is anything after the source.
func runLint(args []string) int {
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		if args[0] != "--lint" {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		args = args[1:]
	}
	if len(args) == 0 && line.IsTerm(os.Stdin) {
		fmt.Fprintln(os.Stderr, "usage: sash --lint needs a script")
		return 2
	}
	src, err := readSource(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	got := lang.Lint(src)
	fmt.Fprint(os.Stdout, got.Stdout)
	fmt.Fprint(os.Stderr, got.Stderr)
	return got.Status
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
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
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
