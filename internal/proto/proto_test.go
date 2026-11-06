package proto

import (
	"bytes"
	"encoding/binary"
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
