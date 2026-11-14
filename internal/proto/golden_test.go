package proto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"os"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

// testdata/sash-mux-frames.json is shared with the systemapi agent. Its
// twin is systemapi-agent/testdata/sash-mux-frames.json in systemapi.io
// (branch feat/sash-mux-pipe), whose tests replay these frames against the
// real agent. The two files must stay byte-identical and change together:
// a change to what this Client sends, or to what it accepts, regenerates
// this file with -update and is copied there in the same change.
var update = flag.Bool("update", false, "regenerate testdata/sash-mux-frames.json")

const goldenPath = "testdata/sash-mux-frames.json"

// TestAgentGolden drives the real Client against a scripted agent and
// records every frame, then compares the result byte for byte with the
// golden. The agent replies are the systemapi agent's exact bytes, and each
// is checked to be accepted as intended.
func TestAgentGolden(t *testing.T) {
	type step struct {
		Op    string `json:"op"`
		Frame string `json:"frame,omitempty"`
		Name  string `json:"name,omitempty"`
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var prior struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(golden, &prior); err != nil || prior.Source == "" {
		t.Fatalf("golden source: %v", err)
	}
	var steps []step
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &Client{RW: a}
	r := rawAgent{t, b}
	recv := func() { steps = append(steps, step{Op: "send", Frame: string(r.read())}) }
	reply := func(s string) { steps = append(steps, step{Op: "expect", Frame: s}); r.send(s) }
	release := func(n string) { steps = append(steps, step{Op: "release", Name: n}) }
	type res struct {
		r   parent.Result
		err error
	}
	call := func(ctx context.Context, v parent.Verb, serial bool) chan res {
		ch := make(chan res, 1)
		go func() {
			var x res
			if serial {
				x.r, x.err = c.Do(v)
			} else {
				x.r, x.err = c.DoContext(ctx, v)
			}
			ch <- x
		}()
		return ch
	}
	wait := func(ch chan res) res {
		select {
		case x := <-ch:
			return x
		case <-time.After(5 * time.Second):
			t.Fatal("call hung")
		}
		return res{}
	}
	ps := `"kind":"rows","rows":[{"cmd":"init","pid":"1","user":"root"}],"cols":["user","pid","cmd"],"exit":0}`
	bg := context.Background()

	// Serial, before hello: today's wire.
	ch := call(bg, parent.Verb{Key: "ps", Argv: []string{"ps"}}, true)
	recv()
	reply(`{` + ps)
	if x := wait(ch); x.err != nil || len(x.r.Rows) != 1 {
		t.Fatalf("serial ps %+v", x)
	}
	// Hello.
	nch := make(chan int, 1)
	go func() {
		n, err := c.Concurrency(8)
		if err != nil {
			n = -1
		}
		nch <- n
	}()
	recv()
	reply(`{"mux":{"v":1,"max":4,"have":["mode"]}}`)
	if n := <-nch; n != 4 {
		t.Fatalf("concurrency %d", n)
	}
	ch = call(bg, parent.Verb{Key: "ps", Argv: []string{"ps"}}, false)
	recv()
	reply(`{"id":1,` + ps)
	if x := wait(ch); x.err != nil || len(x.r.Rows) != 1 {
		t.Fatalf("mux ps %+v", x)
	}
	ch = call(bg, parent.Verb{Key: "file.write", Argv: []string{">", "/srv/a"}, Path: "/srv/a", Body: []byte("x"), Mode: "0600"}, false)
	recv()
	reply(`{"id":2,"kind":"bytes","exit":0}`)
	if x := wait(ch); x.err != nil {
		t.Fatalf("write %+v", x)
	}
	// 3 runs; 4 queues behind it on the same unit and is cancelled.
	chA := call(bg, parent.Verb{Key: "unit.restart", Argv: []string{"unit", "restart", "nginx"}}, false)
	recv()
	ctxB, cancelB := context.WithCancel(bg)
	defer cancelB()
	chB := call(ctxB, parent.Verb{Key: "unit.restart", Argv: []string{"unit", "restart", "nginx.service"}}, false)
	recv()
	cancelB()
	recv()
	reply(`{"id":4,"error":"cancelled"}`)
	if x := wait(chB); !errors.Is(x.err, parent.ErrCancelled) {
		t.Fatalf("cancel %+v", x)
	}
	release("nginx")
	reply(`{"id":3,"kind":"bytes","exit":0}`)
	if x := wait(chA); x.err != nil {
		t.Fatalf("restart %+v", x)
	}
	// Past the agent's deadline.
	ch = call(bg, parent.Verb{Key: "package.install", Argv: []string{"package", "install", "curl"}}, false)
	recv()
	reply(`{"id":5,"error":"outcome_unknown"}`)
	release("curl")
	if x := wait(ch); !errors.Is(x.err, parent.ErrOutcomeUnknown) {
		t.Fatalf("outcome %+v", x)
	}
	ch = call(bg, parent.Verb{Key: "journal", Argv: []string{"journal"}}, false)
	recv()
	reply(`{"id":6,"error":"denied"}`)
	if x := wait(ch); x.err == nil || x.err.Error() != "denied" {
		t.Fatalf("denied %+v", x)
	}
	if err := c.brokenErr(); err != nil {
		t.Fatalf("broken after the golden: %v", err)
	}
	raw, err := json.MarshalIndent(struct {
		Source string `json:"source"`
		Steps  []step `json:"steps"`
	}{Source: prior.Source, Steps: steps}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if *update {
		if err := os.WriteFile(goldenPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(raw, golden) {
		t.Fatalf("frames drifted from %s (and its agent twin); regenerate with -update and copy it to systemapi-agent/testdata in the same change:\n%s", goldenPath, raw)
	}
}
