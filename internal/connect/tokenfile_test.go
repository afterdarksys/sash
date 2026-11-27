//go:build darwin || linux

package connect

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The token file is read only when it is a regular file of this user, with
// no group or other bit, at most 4096 bytes, holding one token; every other
// file is the one fixed refusal, nothing is sent, and neither the path nor
// the content is printed.
func TestTokenFileTable(t *testing.T) {
	const secret = "sashsecret_ABCDEFGHIJKLMNOPQRSTUV"
	write := func(t *testing.T, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct {
		name  string
		path  func(t *testing.T) string
		owner int // the effective user the check compares with; 0 is this one
		want  tokenState
	}{
		{"good", func(t *testing.T) string { return write(t, secret+"\n", 0o600) }, 0, tokenOK},
		{"read-only", func(t *testing.T) string { return write(t, secret, 0o400) }, 0, tokenOK},
		{"surrounded by space", func(t *testing.T) string { return write(t, "  \t"+secret+"\r\n\n", 0o600) }, 0, tokenOK},
		{"mode 0640", func(t *testing.T) string { return write(t, secret, 0o640) }, 0, tokenRejected},
		{"mode 0604", func(t *testing.T) string { return write(t, secret, 0o604) }, 0, tokenRejected},
		{"mode 0610", func(t *testing.T) string { return write(t, secret, 0o610) }, 0, tokenRejected},
		{"another owner", func(t *testing.T) string { return write(t, secret, 0o600) }, 1, tokenRejected},
		{"5000 bytes", func(t *testing.T) string { return write(t, strings.Repeat("a", 5000), 0o600) }, 0, tokenRejected},
		{"4097 bytes of space around a token", func(t *testing.T) string {
			return write(t, secret+strings.Repeat(" ", 4097-len(secret)), 0o600)
		}, 0, tokenRejected},
		{"8 bytes", func(t *testing.T) string { return write(t, "abcdefgh", 0o600) }, 0, tokenRejected},
		{"15 bytes", func(t *testing.T) string { return write(t, strings.Repeat("a", 15), 0o600) }, 0, tokenRejected},
		{"513 bytes", func(t *testing.T) string { return write(t, strings.Repeat("a", 513), 0o600) }, 0, tokenRejected},
		{"empty", func(t *testing.T) string { return write(t, "", 0o600) }, 0, tokenRejected},
		{"two lines", func(t *testing.T) string { return write(t, secret+"\n"+secret+"\n", 0o600) }, 0, tokenRejected},
		{"a space inside", func(t *testing.T) string { return write(t, "sashsecret ABCDEFGHIJKLMNOP", 0o600) }, 0, tokenRejected},
		{"a quote", func(t *testing.T) string { return write(t, `sashsecret"ABCDEFGHIJKLMNOP`, 0o600) }, 0, tokenRejected},
		{"an escape", func(t *testing.T) string { return write(t, "sashsecret\x1b[2JABCDEFGHIJ", 0o600) }, 0, tokenRejected},
		{"not ASCII", func(t *testing.T) string { return write(t, "sashsecret_ABCDEFGHIJKLMé", 0o600) }, 0, tokenRejected},
		{"a symlink to a good file", func(t *testing.T) string {
			target := write(t, secret, 0o600)
			link := filepath.Join(t.TempDir(), "token")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, 0, tokenRejected},
		{"a FIFO", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "token")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, 0, tokenRejected},
		{"a directory", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return path
		}, 0, tokenRejected},
		{"a device", func(t *testing.T) string { return "/dev/null" }, 0, tokenRejected},
		{"unreadable", func(t *testing.T) string { return write(t, secret, 0o200) }, 0, tokenRejected},
		{"absent", func(t *testing.T) string { return filepath.Join(t.TempDir(), "token") }, 0, tokenNone},
		{"relative", func(t *testing.T) string { return "token" }, 0, tokenNone},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root reads a file of any mode")
			}
			path := c.path(t)
			if c.owner != 0 {
				was := euid
				euid = func() int { return was() + c.owner }
				defer func() { euid = was }()
			}
			got, state := readToken(path)
			if state != c.want {
				t.Fatalf("state %d, want %d", state, c.want)
			}
			if c.want == tokenOK && got != secret {
				t.Fatalf("token %q", got)
			}
			if c.want != tokenOK && got != "" {
				t.Fatalf("a rejected file gave a token")
			}

			// Through the whole command: the fixed line, exit 2, nothing
			// sent, and neither the path nor the content printed.
			if c.want == tokenOK || c.name == "relative" {
				return
			}
			if c.owner != 0 {
				was := euid
				euid = func() int { return was() + c.owner }
				defer func() { euid = was }()
			}
			cp := newFakeCP(t)
			con := newFakeCon()
			e := &env{vars: map[string]string{"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_TOKEN_FILE": path}}
			res := begin(con, e, "web1").wait(t)
			want := "connect: token_file_rejected\n"
			if c.want == tokenNone {
				want = "connect: no login; a person logs in with the systemapi CLI\n"
			}
			if res.Code != 2 || con.err.String() != want {
				t.Errorf("code %d stderr %q", res.Code, con.err.String())
			}
			printed := con.err.String() + con.out.String()
			if strings.Contains(printed, secret) || strings.Contains(printed, path) {
				t.Errorf("printed %q", printed)
			}
			if n := len(cp.requests()); n != 0 {
				t.Errorf("%d requests sent", n)
			}
			checkClean(t, cp, con)
		})
	}
}
