package proto

import (
	"net"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

// The service verbs travel the verb pipe, serial and mux, with spawn's
// options in the body and the file verbs on file.write with their
// operation as argv[0]; the init double answers them by name.
func TestServiceVerbsOverThePipe(t *testing.T) {
	g := grant.Grant{V: "sash/2", Purpose: grant.PurposeService, Service: "app",
		Entitlements: []string{"svc.spawn", "svc.signal", "svc.proc", "file.write"},
		FileWrite:    []string{"/run/app"}, Exec: []string{"/opt/app/bin/server"}, User: "app", Group: "app", EnvNames: []string{"P"}}
	for _, max := range []int{0, 4} {
		in := prototest.NewInit("app", g)
		a, b := net.Pipe()
		agent := &prototest.Agent{Max: max, Init: in, Handle: func(parent.Verb) (parent.Result, error) { return parent.Result{}, nil }}
		done := make(chan struct{})
		go func() { defer close(done); agent.Serve(b) }()
		c := &Client{RW: a}
		if max > 0 {
			if n, err := c.Concurrency(2); err != nil || n != 2 {
				t.Fatalf("mux: %d %v", n, err)
			}
		}
		res, err := c.Do(parent.Verb{Key: "svc.spawn", Argv: []string{"spawn", "main", "/opt/app/bin/server", "-v"}, Body: []byte(`{"env":{"P":{"secret":"pw"}},"user":"app","group":"app"}`)})
		if err != nil || res.Kind != parent.KindRows || res.Rows[0]["name"] != "main" {
			t.Fatalf("max %d spawn: %+v %v", max, res, err)
		}
		if p := in.Proc("main"); p.Env["P"] != "pw" || p.User != "app" || p.Args[0] != "-v" {
			t.Fatalf("max %d proc %+v", max, p)
		}
		if _, err := c.Do(parent.Verb{Key: "file.write", Argv: []string{"mkdir", "/run/app/x"}, Path: "/run/app/x"}); err != nil || in.Files["/run/app/x"] != "dir" {
			t.Fatalf("max %d mkdir: %v %v", max, err, in.Files)
		}
		if _, err := c.Do(parent.Verb{Key: "svc.signal", Argv: []string{"signal", "other", "TERM"}}); err == nil || err.Error() != "unknown_name" {
			t.Fatalf("max %d other name: %v", max, err)
		}
		a.Close()
		<-done
	}
}

// Without the init, an agent that knows only "> path" denies a file verb:
// mkdir on file.write never becomes an empty write.
func TestFileVerbDeniedByAnAgent(t *testing.T) {
	a, b := net.Pipe()
	wrote := false
	agent := &prototest.Agent{Handle: func(v parent.Verb) (parent.Result, error) { wrote = true; return parent.Result{}, nil }}
	done := make(chan struct{})
	go func() { defer close(done); agent.Serve(b) }()
	c := &Client{RW: a}
	if _, err := c.Do(parent.Verb{Key: "file.write", Argv: []string{"mkdir", "/run/app/x"}, Path: "/run/app/x"}); err == nil || wrote {
		t.Fatalf("an agent ran mkdir as a write: %v %v", err, wrote)
	}
	a.Close()
	<-done
}
