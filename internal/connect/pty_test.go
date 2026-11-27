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
}

type ptyReport struct {
	Code     int    `json:"code"`
	TTY      string `json:"tty"`
	Restored bool   `json:"restored"`
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
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("the pty test needs python3")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sc, err := json.Marshal(map[string]any{
		"argv": []string{exe, "web1"},
		"env": map[string]string{
			"SYSTEMAPI_URL":           cp.srv.URL,
			"SYSTEMAPI_TOKEN_FILE":    tokenFile(t),
			"SASH_CONNECT_TEST_ROOTS": cp.pem(t),
		},
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
