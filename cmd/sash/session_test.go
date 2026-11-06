package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto"
)

func TestBinarySessionAndHandStart(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	fleetPub, fleetPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	emergencyPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeOpen,
		Exp: time.Now().Add(time.Hour).Unix(), LeaseID: "lease-1", Entitlements: []string{"ps"},
	}, fleetPriv)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSession(t, bin, doc, fleetPub, emergencyPub, []string{"--session", "-c", "ps -ef | egrep sshd | head -n 1"})
	if code != 0 || stdout != "root  1  /usr/sbin/sshd\n" || stderr != "" {
		t.Fatalf("pipeline code %d stdout %q stderr %q", code, stdout, stderr)
	}
	_, stderr, code = runSession(t, bin, nil, fleetPub, emergencyPub, []string{"--session", "-c", "echo hi"})
	if code != 2 || stderr != "grant_missing\n" {
		t.Fatalf("missing code %d stderr %q", code, stderr)
	}
	star, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeOpen,
		Exp: time.Now().Add(time.Hour).Unix(), LeaseID: "lease-1", Entitlements: []string{"*"},
	}, fleetPriv)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runSession(t, bin, star, fleetPub, emergencyPub, []string{"--session", "-c", "echo hi"})
	if code != 2 || stderr != "grant_star\n" || stdout != "" {
		t.Fatalf("star code %d stdout %q stderr %q", code, stdout, stderr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, heldW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	defer heldW.Close()
	hand := exec.CommandContext(ctx, bin, "-c", "echo hi")
	hand.ExtraFiles = []*os.File{held, held, held, held}
	out, err := hand.Output()
	if err != nil || string(out) != "hi\n" {
		t.Fatalf("hand start stdout %q err %v", out, err)
	}
}

func runSession(t *testing.T, bin string, doc, fleet, emergency []byte, args []string) (string, string, int) {
	t.Helper()
	grantR, grantW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fleetR, fleetW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	emergencyR, emergencyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sp, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(sp[0])
	syscall.CloseOnExec(sp[1])
	parentVerb := os.NewFile(uintptr(sp[0]), "verb-parent")
	childVerb := os.NewFile(uintptr(sp[1]), "verb-child")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = proto.Serve(parentVerb, func(v parent.Verb) (parent.Result, error) {
			if v.Key != "ps" {
				return parent.Result{}, errors.New("unexpected")
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
	if doc != nil {
		if _, err := grantW.Write(doc); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fleetW.Write(fleet); err != nil {
		t.Fatal(err)
	}
	if _, err := emergencyW.Write(emergency); err != nil {
		t.Fatal(err)
	}
	grantW.Close()
	fleetW.Close()
	emergencyW.Close()

	cmd := exec.Command(bin, args...)
	cmd.ExtraFiles = []*os.File{grantR, fleetR, emergencyR, childVerb}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	grantR.Close()
	fleetR.Close()
	emergencyR.Close()
	childVerb.Close()
	parentVerb.Close()
	<-done
	return stdout.String(), stderr.String(), exitCode(err)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
