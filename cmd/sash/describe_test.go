package main

import (
	"bytes"
	"crypto/ed25519"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

func buildSash(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sash")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	return bin
}

func runPlainBin(bin string, args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	code := exitCode(cmd.Run())
	return out.String(), errb.String(), code
}

// The --describe document is the stable shape sahinit derives a unit
// from, and the grant request the unsigned sash/2 body: both golden.
func TestBinaryDescribeGolden(t *testing.T) {
	bin := buildSash(t)
	for args, golden := range map[string]string{
		"--describe":                 "testdata/app.describe.json",
		"--describe --grant-request": "testdata/app.grant-request.json",
	} {
		stdout, stderr, code := runPlainBin(bin, append(strings.Fields(args), "testdata/app.sash")...)
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || stderr != "" || stdout != string(want) {
			t.Errorf("%s: code %d stderr %q\n%s", args, code, stderr, stdout)
		}
	}
}

func TestBinaryDescribeExits(t *testing.T) {
	bin := buildSash(t)
	for _, c := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"--describe", "-c", "echo hi"}, 2, "no service block"},
		{[]string{"--describe", "-c", "service {\n name a\n}\necho hi\n"}, 2, "service_top_level"},
		{[]string{"--describe", "-c", "service {\n name a\n}\nfn start() {\n let p = /opt/x\n spawn main $p\n}\n"}, 1, ""},
		{[]string{"--describe", "--session", "testdata/app.sash"}, 2, "usage"},
		{[]string{"--describe", "--plan", "testdata/app.sash"}, 2, "usage"},
		{[]string{"--describe", "testdata/app.sash", "x"}, 2, "usage"},
		{[]string{"--entry", "start"}, 2, "usage"},
	} {
		_, stderr, code := runPlainBin(bin, c.args...)
		if code != c.code || !strings.Contains(stderr, c.err) {
			t.Errorf("%q: code %d stderr %q", c.args, code, stderr)
		}
	}
	// Plan mode prints the spawn line and runs nothing.
	src := "service {\n name app\n user app\n group app\n run /opt/app/bin/server \"-g\" \"daemon off;\"\n}\n"
	_, stderr, code := runPlainBin(bin, "--plan", "--entry", "run", "-c", src)
	// A hand start has an empty grant: the spawn is denied before plan.
	if code != 2 || !strings.Contains(stderr, "denied svc.spawn") {
		t.Errorf("hand plan: %d %q", code, stderr)
	}
}

// --session --entry against the init double over the verb socket: the
// boot grant verifies, start spawns, the secret reaches the init marked,
// and a grant for another service stops before any verb.
func TestBinaryEntryAgainstTheInit(t *testing.T) {
	bin := buildSash(t)
	fleetPub, fleetPriv, _ := ed25519.GenerateKey(nil)
	emergencyPub, _, _ := ed25519.GenerateKey(nil)
	boot := func(service string) (grant.Grant, []byte) {
		g := grant.Grant{V: "sash/2", Actor: "service:" + service, Host: "web1", Purpose: grant.PurposeService, LeaseID: "boot-1",
			Entitlements: []string{"file.read", "file.write", "svc.spawn", "svc.signal", "svc.wait", "svc.status", "svc.probe"},
			FileRead:     []string{"/etc/app"}, FileWrite: []string{"/run/app"},
			Service: service, Exec: []string{"/opt/app/bin/server"}, Caps: []string{}, Mounts: []string{}, MountSources: []string{}, User: "app", Group: "app", Serial: 3}
		doc, err := grant.Sign(g, fleetPriv)
		if err != nil {
			t.Fatal(err)
		}
		return g, doc
	}
	g, doc := boot("app")
	in := prototest.NewInit("app", g)
	in.Status["postgres"] = "ready"
	stdout, stderr, code := runInit(t, bin, doc, fleetPub, emergencyPub, in, "--session", "--entry", "start", "testdata/app.sash")
	if code != 0 {
		t.Fatalf("start: %d %q %q", code, stdout, stderr)
	}
	p := in.Proc("main")
	if p == nil || p.Env["DB_PASS"] != "pw-from-file" || strings.Join(p.SecretEnv, ",") != "DB_PASS" || p.User != "app" {
		t.Fatalf("proc %+v", p)
	}
	if stdout, stderr, code := runInit(t, bin, doc, fleetPub, emergencyPub, in, "--session", "--entry", "stop", "testdata/app.sash"); code != 0 || in.Proc("main").Signal != "TERM" {
		t.Fatalf("stop: %d %q %q", code, stdout, stderr)
	}

	_, other := boot("web")
	in = prototest.NewInit("app", g)
	_, stderr, code = runInit(t, bin, other, fleetPub, emergencyPub, in, "--session", "--entry", "start", "testdata/app.sash")
	if code != 2 || stderr != "grant_service_mismatch\n" || len(in.Calls()) != 0 {
		t.Fatalf("mismatch: %d %q %v", code, stderr, in.Calls())
	}
	_, stderr, code = runInit(t, bin, doc, fleetPub, emergencyPub, in, "--session", "-c", "echo hi")
	if code != 2 || !strings.Contains(stderr, "entry_missing") {
		t.Fatalf("boot grant without entry: %d %q", code, stderr)
	}
}

// runInit runs bin with a grant and the init double on the verb socket.
// The init answers file.read of /etc/app/db.secret for the start entry.
func runInit(t *testing.T, bin string, doc, fleet, emergency []byte, in *prototest.Init, args ...string) (string, string, int) {
	t.Helper()
	pipe := func(b []byte) *os.File {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
		w.Close()
		return r
	}
	sp, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(sp[0])
	syscall.CloseOnExec(sp[1])
	parentF := os.NewFile(uintptr(sp[0]), "verb-parent")
	childF := os.NewFile(uintptr(sp[1]), "verb-child")
	conn, err := net.FileConn(parentF)
	if err != nil {
		t.Fatal(err)
	}
	parentF.Close()
	agent := &prototest.Agent{Init: in, Files: map[string]string{"/etc/app/db.secret": "pw-from-file"}, Handle: nil}
	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.Serve(conn)
	}()
	grantR, fleetR, emergencyR := pipe(doc), pipe(fleet), pipe(emergency)
	cmd := exec.Command(bin, args...)
	cmd.ExtraFiles = []*os.File{grantR, fleetR, emergencyR, childF}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	childF.Close()
	grantR.Close()
	fleetR.Close()
	emergencyR.Close()
	<-done
	conn.Close()
	return stdout.String(), stderr.String(), exitCode(err)
}
