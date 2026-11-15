package line

import (
	"sync"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// hupParent blocks ps until released and keeps file.writes.
type hupParent struct {
	mu      sync.Mutex
	release chan struct{}
	writes  []parent.Verb
}

func (p *hupParent) Do(v parent.Verb) (parent.Result, error) {
	switch v.Key {
	case "ps":
		<-p.release
		return parent.Result{Kind: parent.KindRows, Cols: []string{"user"}, Rows: []parent.Row{{"user": "root"}}}, nil
	case "file.write":
		p.mu.Lock()
		p.writes = append(p.writes, v)
		p.mu.Unlock()
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

// A hangup during a run stops it at its next statement and then writes
// the session recording, in that order, at 0600.
func TestHangupWritesRecording(t *testing.T) {
	g := grant.Grant{V: "sash/1", Entitlements: []string{"ps", "file.write"}, FileWrite: []string{"/tmp"}}
	p := &hupParent{release: make(chan struct{})}
	e := New(g, p)
	close(p.release)
	if out := e.run("set -o record /tmp/r.jsonl"); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	p.release = make(chan struct{})
	ran := make(chan int, 1)
	go func() { ran <- e.run("ps\nps\nps").Status }()
	time.Sleep(50 * time.Millisecond)
	type res struct {
		status int
		ok     bool
	}
	hung := make(chan res, 1)
	go func() {
		out, ok := e.hangup(5 * time.Second)
		hung <- res{out.Status, ok}
	}()
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	early := len(p.writes)
	p.mu.Unlock()
	if early != 0 {
		t.Fatal("the recording was written while a run held the State")
	}
	close(p.release)
	if st := <-ran; st != 129 {
		t.Fatalf("run status %d", st)
	}
	r := <-hung
	p.mu.Lock()
	defer p.mu.Unlock()
	if !r.ok || r.status != 0 || len(p.writes) != 1 || p.writes[0].Mode != "0600" || p.writes[0].Argv[0] != ">" {
		t.Fatalf("%+v %+v", r, p.writes)
	}
}
