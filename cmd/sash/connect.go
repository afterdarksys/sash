package main

// sash connect [--root] <host>: client mode (package connect).
//
// Threats: a host session, a script, or a login shell reaching the control
// plane with a person's login. connect is an argv mode and nothing else:
// it is recognised only as the first argument of the process, so no line,
// script, fn, or -c text can name it (there it is exit 127 unknown), and
// behind any flag (-l, --session, --plan, --needs, --lint, --record,
// --replay, -c) it is the usage line and exit 2 before anything is read.
// It takes no script and has no non-interactive form. It does not protect
// a workstation whose user runs it on purpose against the wrong address.

import (
	"os"
	"strings"

	"github.com/afterdarksys/sash/internal/connect"
	"github.com/afterdarksys/sash/internal/line"
)

const connectUsage = "usage: sash connect [--root] <host>"

// connectMode is true when the first argument is the word connect and
// something follows. A file named connect that stands alone is still a
// script, as before.
func connectMode(args []string) bool {
	return len(args) >= 2 && args[0] == "connect"
}

// connectMisplaced is true when connect and its arguments stand behind
// flags: that is refused, never run.
func connectMisplaced(args []string) bool {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		i++
	}
	return i > 0 && i < len(args)-1 && args[i] == "connect"
}

func runConnect(args []string) int {
	version, _, _ := buildInfo()
	res := connect.Run(args, os.Getenv, connect.OSConsole(os.Stdin, os.Stdout, os.Stderr), version)
	if res.Signal != nil {
		// SIGTERM or SIGHUP: the terminal is already restored; end as
		// the signal says.
		line.EndBySignal(res.Signal, res.RestoreErr)
	}
	return res.Code
}
