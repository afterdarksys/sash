package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connect is an argv mode only: as a script, a -c line, or behind any flag
// it never runs, and run as itself it needs a terminal on all three
// descriptors before it reads anything.
func TestBinaryConnectIsArgvOnly(t *testing.T) {
	bin := ptyBin(t)
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("sashtesttoken_0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "s.sash")
	if err := os.WriteFile(script, []byte("connect web1\necho RAN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An address where nothing listens: a run that sent a request would
	// say request_failed.
	env := map[string]string{"SYSTEMAPI_URL": "https://127.0.0.1:1", "SYSTEMAPI_TOKEN_FILE": token, "HOME": dir}
	for name, c := range map[string]struct {
		run  ptyRun
		code int
		says string
	}{
		"a -c line":             {ptyRun{Argv: []string{bin, "-c", "connect web1"}}, 127, ""},
		"a script":              {ptyRun{Argv: []string{bin, script}}, 127, ""},
		"behind --session":      {ptyRun{Argv: []string{bin, "--session", "connect", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"behind --plan":         {ptyRun{Argv: []string{bin, "--plan", "connect", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"behind -l":             {ptyRun{Argv: []string{bin, "-l", "connect", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"behind --needs":        {ptyRun{Argv: []string{bin, "--needs", "connect", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"behind --lint":         {ptyRun{Argv: []string{bin, "--lint", "connect", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"behind -c":             {ptyRun{Argv: []string{bin, "-c", "connect", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"a further argument":    {ptyRun{Argv: []string{bin, "connect", "web1", "extra"}}, 2, "usage: sash connect [--root] <host>"},
		"a flag after connect":  {ptyRun{Argv: []string{bin, "connect", "--session", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"--script":              {ptyRun{Argv: []string{bin, "connect", "--script", "web1"}}, 2, "usage: sash connect [--root] <host>"},
		"--root and no host":    {ptyRun{Argv: []string{bin, "connect", "--root"}}, 2, "usage: sash connect [--root] <host>"},
		"stdin a pipe":          {ptyRun{Argv: []string{bin, "connect", "web1"}, Stdin: "pipe"}, 2, "connect needs a terminal"},
		"stdout a pipe":         {ptyRun{Argv: []string{bin, "connect", "web1"}, Stdout: "pipe"}, 2, "connect needs a terminal"},
		"stderr not a terminal": {ptyRun{Argv: []string{bin, "connect", "--root", "web1"}, Stderr: "pipe"}, 2, "connect needs a terminal"},
		"an http address":       {ptyRun{Argv: []string{bin, "connect", "web1"}, Env: map[string]string{"SYSTEMAPI_URL": "http://127.0.0.1:1", "SYSTEMAPI_TOKEN_FILE": token}}, 2, "connect: invalid SYSTEMAPI_URL"},
		"a hostile host name":   {ptyRun{Argv: []string{bin, "connect", "web1\x1b[2J"}}, 2, "connect: invalid host name"},
	} {
		t.Run(name, func(t *testing.T) {
			run := c.run
			if run.Env == nil {
				run.Env = env
			}
			// The first run of a new binary can be slow to start (a scan
			// of it on darwin); none of these waits on anything.
			run.Wait = 15
			rep := runPTY(t, run)
			all := rep.TTY + rep.Stdout + rep.Stderr
			if rep.Code != c.code || !strings.Contains(all, c.says) || !rep.Restored {
				t.Errorf("code %d restored %v, want %d; tty %q stdout %q stderr %q", rep.Code, rep.Restored, c.code, rep.TTY, rep.Stdout, rep.Stderr)
			}
			if strings.Contains(all, "request_failed") || strings.Contains(all, "RAN") || strings.Contains(all, "sashtesttoken") {
				t.Errorf("it went further: %q", all)
			}
			if strings.Contains(all, "\x1b[2J") {
				t.Errorf("a host name reached the terminal unescaped: %q", all)
			}
		})
	}
}
