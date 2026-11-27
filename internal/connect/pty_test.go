//go:build darwin || linux

package connect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/wsclient/wstest"
)

// The pty tests run sash connect as a real process (this test binary, by
// way of TestMain) on a pseudo terminal, through the harness the sash
// command's own pty tests use, against the fake control plane in this
// process. They hold that the terminal's mode is put back on every way out.

type ptyStep struct {
	Expect  string  `json:"expect,omitempty"`
	Send    string  `json:"send,omitempty"`
	Signal  string  `json:"signal,omitempty"`
	Sleep   float64 `json:"sleep,omitempty"`
	Winsize []int   `json:"winsize,omitempty"`
	Check   bool    `json:"check,omitempty"`
}

type ptyReport struct {
	Code     int    `json:"code"`
	TTY      string `json:"tty"`
	Restored bool   `json:"restored"`
	Checks   []bool `json:"checks"`
	Error    string `json:"error"`
}

// ptyTerminal is the host's side of a session for the pty tests: READY once
// started; ^C (forwarded as input) ends the session with 130, ^D with 0, q
// closes the socket with no pty_exit; a resize is echoed as RESIZED.
func ptyTerminal(sizes chan<- string) func(ctx context.Context, c *wstest.Conn, r *http.Request) {
	return func(ctx context.Context, c *wstest.Conn, r *http.Request) {
		sizes <- r.URL.Query().Get("rows") + "x" + r.URL.Query().Get("cols")
		start(ctx, c)
		sendBytes(ctx, c, []byte("READY\r\n"))
		for {
			f, ok := readClient(ctx, c)
			if !ok {
				return
			}
			switch f.Type {
			case "pty_resize":
				sendBytes(ctx, c, []byte(fmt.Sprintf("RESIZED %dx%d\r\n", f.Rows, f.Cols)))
			case "pty_input":
				b, err := base64.StdEncoding.DecodeString(f.DataB64)
				if err != nil {
					return
				}
				switch {
				case strings.ContainsRune(string(b), 0x03):
					sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":130,"reason":"exited"}}`)
					return
				case strings.ContainsRune(string(b), 0x04):
					sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0,"reason":"exited"}}`)
					return
				case strings.Contains(string(b), "q"):
					return
				}
			}
		}
	}
}

func runConnectPTY(t *testing.T, cp *fakeCP, steps []ptyStep) ptyReport {
	t.Helper()
	return runConnectPTYEnv(t, cp, steps, nil)
}

// runConnectPTYEnv is runConnectPTY with names added to the child's
// environment, or removed from it when the value is empty.
func runConnectPTYEnv(t *testing.T, cp *fakeCP, steps []ptyStep, extra map[string]string) ptyReport {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("the pty test needs python3")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"SYSTEMAPI_URL":           cp.srv.URL,
		"SYSTEMAPI_TOKEN_FILE":    tokenFile(t),
		"SASH_CONNECT_TEST_ROOTS": cp.pem(t),
	}
	for k, v := range extra {
		if v == "" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	sc, err := json.Marshal(map[string]any{
		"argv":  []string{exe, "web1"},
		"env":   env,
		"steps": steps,
		"wait":  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, python, "../../cmd/sash/testdata/onpty.py", string(sc)).Output()
	if err != nil {
		t.Fatalf("onpty.py: %v\n%s", err, out)
	}
	var rep ptyReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("onpty.py report: %v\n%s", err, out)
	}
	if rep.Error != "" {
		t.Fatalf("pty: %s\ntty %q", rep.Error, rep.TTY)
	}
	if strings.Contains(rep.TTY, testToken) || stkRe.MatchString(rep.TTY) {
		t.Errorf("a credential reached the terminal: %q", rep.TTY)
	}
	return rep
}

func TestRawModeIsRestoredOnAPTY(t *testing.T) {
	ready := ptyStep{Expect: "READY"}
	for _, c := range []struct {
		name  string
		steps []ptyStep
		code  int
		shown string
	}{
		{"the host ends the session after ^D", []ptyStep{ready, {Send: "\x04"}}, 0, ""},
		{"Ctrl-C is the host's input after start", []ptyStep{ready, {Send: "\x03"}}, 130, ""},
		{"SIGTERM", []ptyStep{ready, {Signal: "TERM"}}, -15, ""},
		{"SIGHUP", []ptyStep{ready, {Signal: "HUP"}}, -1, ""},
		{"the server closes", []ptyStep{ready, {Send: "q"}}, 1, "session ended: connection lost"},
		{"~. after Enter", []ptyStep{ready, {Send: "\r~."}}, 1, "connect: closed"},
		{"a window-size change is forwarded", []ptyStep{ready, {Winsize: []int{50, 132}}, {Signal: "WINCH"}, {Expect: "RESIZED 50x132"}, {Send: "\x04"}}, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cp := newFakeCP(t)
			sizes := make(chan string, 1)
			cp.terminal = ptyTerminal(sizes)
			rep := runConnectPTY(t, cp, c.steps)
			if rep.Code != c.code || !rep.Restored {
				t.Errorf("code %d restored %v, want %d true; tty %q", rep.Code, rep.Restored, c.code, rep.TTY)
			}
			if !strings.Contains(rep.TTY, "connect: session on web1") || !strings.Contains(rep.TTY, c.shown) {
				t.Errorf("tty %q", rep.TTY)
			}
			// The modes a host may set are reset, then bracketed paste
			// is turned off by the raw-mode guard.
			if !strings.Contains(rep.TTY, resetTail+"\x1b[?2004l") {
				t.Errorf("no reset tail: tty %q", rep.TTY)
			}
			// A pseudo terminal starts with no size: the client asks for
			// 24 by 80.
			select {
			case got := <-sizes:
				if got != "24x80" {
					t.Errorf("initial size %s", got)
				}
			default:
				t.Errorf("no upgrade")
			}
		})
	}

	t.Run("SIGINT before the session starts", func(t *testing.T) {
		cp := newFakeCP(t)
		var once sync.Once
		reached := make(chan struct{})
		cp.resolve = func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { close(reached) })
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		}
		rep := runConnectPTY(t, cp, []ptyStep{{Sleep: 1.5}, {Signal: "INT"}})
		select {
		case <-reached:
		default:
			t.Fatal("the resolve call was never made")
		}
		if rep.Code != 130 || !rep.Restored || !strings.Contains(rep.TTY, "connect: interrupted") {
			t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
		}
	})
}

// SSL_CERT_FILE and SSL_CERT_DIR naming a CA do not make a server that CA
// signed trusted, even with the GODEBUG setting that makes darwin honour
// them: the roots are the platform's own. The fake control plane's
// certificate is its own CA, so with either name set to it, a client that
// read the environment would reach the control plane.
func TestEnvironmentCannotChangeTheRoots(t *testing.T) {
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		t.Run(name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.terminal = ptyTerminal(make(chan string, 1))
			value := cp.pem(t)
			if name == "SSL_CERT_DIR" {
				value = filepath.Dir(value)
			}
			rep := runConnectPTYEnv(t, cp, []ptyStep{{Expect: "connect: "}, {Sleep: 0.3}}, map[string]string{
				"SASH_CONNECT_TEST_ROOTS":        "",
				"SASH_CONNECT_TEST_SYSTEM_ROOTS": "1",
				name:                             value,
				// go.mod's go line keeps darwin's old default, which
				// ignores both names; the environment can turn the
				// Go 1.27 behaviour on.
				"GODEBUG": "x509sslcertoverrideplatform=1",
			})
			if rep.Code != 1 || !strings.Contains(rep.TTY, "connect: request_failed") || len(cp.requests()) != 0 {
				t.Errorf("code %d requests %d tty %q", rep.Code, len(cp.requests()), rep.TTY)
			}
		})
	}
}

// On a real terminal: SIGTSTP puts the terminal's mode back while the
// process is stopped and raw mode is entered again when it is continued
// (^D then reaches the host as input, not as the end of a line); SIGQUIT
// restores the terminal and ends with 131; a panic while raw restores the
// terminal before the process ends.
func TestStopQuitAndPanicOnAPTY(t *testing.T) {
	ready := ptyStep{Expect: "READY"}
	t.Run("SIGTSTP then SIGCONT", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.terminal = ptyTerminal(make(chan string, 1))
		rep := runConnectPTYEnv(t, cp, []ptyStep{
			ready, {Check: true}, {Signal: "TSTP"}, {Sleep: 0.5}, {Check: true}, {Signal: "CONT"}, {Sleep: 0.5}, {Check: true}, {Send: "\x04"},
		}, nil)
		if rep.Code != 0 || !rep.Restored || len(rep.Checks) != 3 || rep.Checks[0] || !rep.Checks[1] || rep.Checks[2] {
			t.Errorf("code %d restored %v checks %v tty %q", rep.Code, rep.Restored, rep.Checks, rep.TTY)
		}
		if strings.Count(rep.TTY, resetTail+"\x1b[?2004l") != 2 {
			t.Errorf("tty %q", rep.TTY)
		}
	})
	t.Run("SIGQUIT", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.terminal = ptyTerminal(make(chan string, 1))
		rep := runConnectPTYEnv(t, cp, []ptyStep{ready, {Signal: "QUIT"}}, nil)
		if rep.Code != 131 || !rep.Restored || !strings.Contains(rep.TTY, resetTail+"\x1b[?2004l") || strings.Contains(rep.TTY, "goroutine") {
			t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
		}
	})
	t.Run("a panic", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendBytes(ctx, c, []byte("BOOM"))
			<-ctx.Done()
		}
		rep := runConnectPTYEnv(t, cp, []ptyStep{{Expect: "test fault"}}, map[string]string{"SASH_CONNECT_TEST_PANIC": "1"})
		if rep.Code != 2 || !rep.Restored || !strings.Contains(rep.TTY, resetTail+"\x1b[?2004l") {
			t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
		}
	})
}
