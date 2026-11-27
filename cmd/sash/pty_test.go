package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ptyRun is one run of the binary on a pseudo terminal (testdata/onpty.py).
type ptyRun struct {
	Argv   []string          `json:"argv"`
	Argv0  string            `json:"argv0,omitempty"`
	Stdin  string            `json:"stdin,omitempty"`
	Stdout string            `json:"stdout,omitempty"`
	Stderr string            `json:"stderr,omitempty"`
	Input  string            `json:"input,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
	Steps  []ptyStep         `json:"steps,omitempty"`
	Wait   float64           `json:"wait,omitempty"`
}

type ptyStep struct {
	Expect string  `json:"expect,omitempty"`
	Send   string  `json:"send,omitempty"`
	Signal string  `json:"signal,omitempty"`
	Sleep  float64 `json:"sleep,omitempty"`
}

// ptyReport is what the run did: what the terminal showed (latin-1, so
// every byte is kept), what a piped stdout or stderr received, the exit
// code (negative for a signal), and whether the terminal mode was put
// back.
type ptyReport struct {
	Code     int    `json:"code"`
	TTY      string `json:"tty"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Restored bool   `json:"restored"`
	Error    string `json:"error"`
}

// latin1 is the bytes of a report string.
func latin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}

var (
	ptyBinOnce sync.Once
	ptyBinPath string
	ptyBinErr  error
)

// ptyBin builds the binary once for every pty test.
func ptyBin(t *testing.T) string {
	t.Helper()
	ptyBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sash-pty")
		if err != nil {
			ptyBinErr = err
			return
		}
		ptyBinPath = filepath.Join(dir, "sash")
		build := exec.Command("go", "build", "-o", ptyBinPath, ".")
		build.Stderr = os.Stderr
		ptyBinErr = build.Run()
	})
	if ptyBinErr != nil {
		t.Fatal(ptyBinErr)
	}
	return ptyBinPath
}

// runPTY runs one scenario. It skips without python3, as the REPL test does.
func runPTY(t *testing.T, r ptyRun) ptyReport {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("PTY integration test requires python3")
	}
	sc, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, python, "testdata/onpty.py", string(sc)).Output()
	if err != nil {
		t.Fatalf("pty.py: %v\n%s", err, out)
	}
	var rep ptyReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("pty.py report: %v\n%s", err, out)
	}
	rep.TTY, rep.Stdout, rep.Stderr = string(latin1(rep.TTY)), string(latin1(rep.Stdout)), string(latin1(rep.Stderr))
	if rep.Error != "" {
		t.Fatalf("pty: %s\ntty %q stdout %q stderr %q", rep.Error, rep.TTY, rep.Stdout, rep.Stderr)
	}
	return rep
}
