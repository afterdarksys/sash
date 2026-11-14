package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

func TestRoundTripRowsAndBody(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(b, func(v parent.Verb) (parent.Result, error) {
			if v.Key == "file.write" {
				if string(v.Body) != "hi" || v.Path != "/var/tmp/ops/out" {
					t.Errorf("write verb %+v %q", v, v.Body)
				}
				return parent.Result{Kind: parent.KindBytes}, nil
			}
			return parent.Result{
				Kind: parent.KindRows,
				Cols: []string{"user", "pid", "cmd"},
				Rows: []parent.Row{{"user": "root", "pid": "1", "cmd": "/usr/sbin/sshd"}},
			}, nil
		})
	}()
	c := Client{RW: a}
	got, err := c.Do(parent.Verb{Key: "ps", Argv: []string{"ps", "-ef"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != parent.KindRows || len(got.Rows) != 1 || got.Rows[0]["cmd"] != "/usr/sbin/sshd" {
		t.Fatalf("rows %+v", got)
	}
	if _, err := c.Do(parent.Verb{Key: "file.write", Path: "/var/tmp/ops/out", Body: []byte("hi")}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	<-done
}

func TestRejectsHugeFrame(t *testing.T) {
	var buf bytes.Buffer
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], MaxFrame+1)
	buf.Write(n[:])
	err := Read(&buf, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "frame") {
		t.Fatalf("err %v", err)
	}
	if buf.Len() != 0 {
		t.Fatal("huge frame consumed a body")
	}
}

func TestPipeRunsBytePipeline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		_ = Serve(b, func(v parent.Verb) (parent.Result, error) {
			if v.Key != "ps" {
				t.Errorf("key %s", v.Key)
			}
			return parent.Result{
				Kind: parent.KindRows,
				Cols: []string{"user", "pid", "cmd"},
				Rows: []parent.Row{
					{"user": "root", "pid": "1", "cmd": "/usr/sbin/sshd"},
					{"user": "root", "pid": "2", "cmd": "cron"},
				},
			}, nil
		})
	}()
	got := lang.Run(`ps -ef | egrep sshd | head -n 1`, grant.Grant{
		Entitlements: []string{"ps"},
	}, &Client{RW: a}, lang.Options{})
	if got.Status != 0 || got.Stdout != "root  1  /usr/sbin/sshd\n" {
		t.Fatalf("status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
}

// failWriter reads frames from in and fails every write.
type failWriter struct {
	in *bytes.Buffer
}

func (f failWriter) Read(p []byte) (int, error) { return f.in.Read(p) }
func (failWriter) Write([]byte) (int, error)    { return 0, errors.New("pipe broken") }

// A bad frame body whose error reply cannot be written ends Serve with that
// error instead of reading the next verb on a broken pipe.
func TestServeReturnsBodyErrorWriteFailure(t *testing.T) {
	var in bytes.Buffer
	if err := Write(&in, request{Key: "file.write", Path: "/tmp/x", Body: "!not base64!"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&in, request{Key: "ps"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := Serve(failWriter{in: &in}, func(parent.Verb) (parent.Result, error) {
		calls++
		return parent.Result{}, nil
	})
	if err == nil || err.Error() != "pipe broken" || calls != 0 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

// rawServe answers each frame with reply and hands the raw request to got.
func rawServe(t *testing.T, rw net.Conn, reply response, got chan<- string) {
	for {
		var n [4]byte
		if _, err := io.ReadFull(rw, n[:]); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(n[:]))
		if _, err := io.ReadFull(rw, body); err != nil {
			return
		}
		got <- string(body)
		if err := Write(rw, reply); err != nil {
			t.Error(err)
			return
		}
	}
}

// A file.write mode is on the wire only when the verb has one; an agent
// that refuses it as a bad frame is reported as too old, and only for a
// moded write.
func TestModeOnTheWire(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	got := make(chan string, 4)
	go rawServe(t, b, response{Error: "frame body"}, got)
	c := Client{RW: a}
	_, err := c.Do(parent.Verb{Key: "file.write", Path: "/srv/x", Body: []byte("s"), Mode: "0600"})
	if frame := <-got; !strings.Contains(frame, `"mode":"0600"`) || !errors.Is(err, parent.ErrModeUnsupported) || err.Error() != "agent too old for file.write mode" {
		t.Fatalf("moded: %s %v", frame, err)
	}
	_, err = c.Do(parent.Verb{Key: "file.write", Path: "/srv/x", Body: []byte("s")})
	if frame := <-got; strings.Contains(frame, "mode") || err == nil || err.Error() != "frame body" {
		t.Fatalf("no mode: %s %v", frame, err)
	}
	a.Close()
}

// Serve hands the mode to the handler.
func TestServeMode(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	modes := make(chan string, 2)
	go func() {
		if err := Serve(b, func(v parent.Verb) (parent.Result, error) {
			modes <- v.Mode
			return parent.Result{}, nil
		}); err != nil {
			t.Error(err)
		}
	}()
	c := Client{RW: a}
	for _, want := range []string{"0600", ""} {
		if _, err := c.Do(parent.Verb{Key: "file.write", Path: "/srv/x", Mode: want}); err != nil || <-modes != want {
			t.Fatalf("mode %q: %v", want, err)
		}
	}
	a.Close()
}
