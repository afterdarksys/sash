package proto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

func muxPair(t *testing.T, max, slots int, h func(parent.Verb) (parent.Result, error)) (*Client, *fakeAgent, func()) {
	t.Helper()
	a, b := net.Pipe()
	agent := newFakeAgent(b, max, slots, h)
	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.serve()
	}()
	return &Client{RW: a}, agent, func() {
		a.Close()
		<-done
		b.Close()
		agent.mu.Lock()
		defer agent.mu.Unlock()
		if agent.err != nil {
			t.Errorf("agent saw a protocol violation: %v", agent.err)
		}
	}
}

// helloed is a raw agent after a good hello with limit max.
func helloed(t *testing.T, max int) (*Client, rawAgent, func()) {
	t.Helper()
	a, b := net.Pipe()
	c := &Client{RW: a}
	r := rawAgent{t, b}
	got := make(chan error, 1)
	go func() {
		_, err := c.Concurrency(8)
		got <- err
	}()
	if h := string(r.read()); h != `{"mux":{"v":1,"max":16,"need":["mode"]}}` {
		t.Fatalf("hello %s", h)
	}
	r.send(fmt.Sprintf(`{"mux":{"v":1,"max":%d,"have":["mode"]}}`, max))
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	return c, r, func() {
		a.Close()
		b.Close()
	}
}

// Against the fake agent: hello once, effective N = min(N, M), results
// routed by id, and the agent never sees more than M outstanding.
func TestMuxConcurrentDo(t *testing.T) {
	c, agent, done := muxPair(t, 4, 4, func(v parent.Verb) (parent.Result, error) {
		time.Sleep(time.Duration(len(v.Path)%3) * time.Millisecond)
		return echoPath(v)
	})
	defer done()
	if n, err := c.Concurrency(16); err != nil || n != 4 {
		t.Fatalf("n %d %v", n, err)
	}
	if n, err := c.Concurrency(2); err != nil || n != 2 {
		t.Fatalf("n %d %v", n, err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				p := fmt.Sprintf("/srv/g%d/%d", g, i)
				var res parent.Result
				var err error
				if i%2 == 0 {
					res, err = c.Do(parent.Verb{Key: "file.read", Path: p})
				} else {
					res, err = c.DoContext(context.Background(), parent.Verb{Key: "file.read", Path: p})
				}
				if err != nil || string(res.Bytes) != p {
					t.Errorf("%s: %q %v", p, res.Bytes, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	agent.mu.Lock()
	defer agent.mu.Unlock()
	hellos := 0
	for _, f := range agent.frames {
		if strings.Contains(string(f), `"mux"`) {
			hellos++
		}
	}
	if hellos != 1 || agent.maxSeen > 4 || agent.lastID != 320 {
		t.Fatalf("hellos %d maxSeen %d lastID %d", hellos, agent.maxSeen, agent.lastID)
	}
}

// An old agent answers hello with exactly {"error":"frame body"}: the
// client stays serial, N is 1, and never errors. A session that never asks
// for concurrency sends exactly today's frames.
func TestMuxSerialFallback(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	var mu sync.Mutex
	var frames [][]byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		oldAgent(b, &frames, &mu, echoPath)
	}()
	c := &Client{RW: a}
	v := parent.Verb{Key: "file.write", Argv: []string{">", "/srv/x"}, Path: "/srv/x", Body: []byte("hi")}
	if res, err := c.Do(v); err != nil || string(res.Bytes) != "/srv/x" {
		t.Fatalf("%q %v", res.Bytes, err)
	}
	if n, err := c.Concurrency(8); err != nil || n != 1 {
		t.Fatalf("n %d %v", n, err)
	}
	if n, err := c.Concurrency(8); err != nil || n != 1 {
		t.Fatalf("second n %d %v", n, err)
	}
	if res, err := c.DoContext(context.Background(), v); err != nil || string(res.Bytes) != "/srv/x" {
		t.Fatalf("%q %v", res.Bytes, err)
	}
	if _, err := c.Do(parent.Verb{Key: "file.write", Path: "/srv/x", Mode: "0600"}); !errors.Is(err, parent.ErrModeUnsupported) {
		t.Fatalf("mode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.DoContext(ctx, v); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled serial: %v", err)
	}
	a.Close()
	<-done
	want, err := json.Marshal(request{Key: v.Key, Argv: v.Argv, Path: v.Path, Body: base64.StdEncoding.EncodeToString(v.Body)})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 4 || string(frames[0]) != string(want) || string(frames[2]) != string(want) || !strings.Contains(string(frames[1]), `"mux"`) {
		t.Fatalf("frames %q", frames)
	}
}

// A session that never calls Concurrency never sends hello, even to a mux
// agent.
func TestMuxNoHelloWithoutConcurrency(t *testing.T) {
	c, agent, done := muxPair(t, 4, 4, echoPath)
	for i := 0; i < 3; i++ {
		if _, err := c.Do(parent.Verb{Key: "ps"}); err != nil {
			t.Fatal(err)
		}
	}
	done()
	for _, f := range agent.frames {
		if strings.Contains(string(f), "mux") || strings.Contains(string(f), `"id"`) {
			t.Fatalf("frame %s", f)
		}
	}
}

// Any hello answer but the two exact shapes breaks the client.
func TestMuxMalformedHello(t *testing.T) {
	for _, reply := range []string{
		`{"mux":{"v":2,"max":4,"have":["mode"]}}`,
		`{"mux":{"v":1,"max":0,"have":["mode"]}}`,
		`{"mux":{"v":1,"max":17,"have":["mode"]}}`,
		`{"mux":{"v":1,"max":-1,"have":["mode"]}}`,
		`{"mux":{"v":1,"max":4}}`,
		`{"mux":{"v":1,"max":4,"have":["mode"],"x":1}}`,
		`{"mux":{"v":1,"max":4,"have":["mode"]},"error":""}`,
		`{"mux":{"v":1,"max":4,"have":["mode"]}} {}`,
		`{"error":"frame body "}`,
		`{"error": "frame body"}`,
		`{"error":"denied"}`,
		`{"mux":null}`,
		`{}`,
		`[]`,
		`not json`,
	} {
		a, b := net.Pipe()
		c := &Client{RW: a}
		r := rawAgent{t, b}
		got := make(chan error, 1)
		go func() {
			_, err := c.Concurrency(4)
			got <- err
		}()
		r.read()
		r.send(reply)
		if err := <-got; !errors.Is(err, ErrBroken) {
			t.Errorf("%s: %v", reply, err)
		}
		if _, err := c.Do(parent.Verb{Key: "ps"}); !errors.Is(err, ErrBroken) {
			t.Errorf("%s: later Do %v", reply, err)
		}
		a.Close()
		b.Close()
	}
}

// A response for an id not outstanding breaks the client: it is never
// routed to another waiter, and later calls fail at once.
func TestMuxUnknownIDBreaks(t *testing.T) {
	for _, bad := range []string{`{"id":99,"kind":"bytes","exit":0}`, `{"id":0,"kind":"bytes","exit":0}`, `{"kind":"bytes","exit":0}`, `{"id":1,"kind":"bytes","exit":0}`} {
		c, r, done := helloed(t, 4)
		got := make(chan error, 1)
		go func() {
			_, err := c.Do(parent.Verb{Key: "ps"})
			got <- err
		}()
		r.read()
		if bad == `{"id":1,"kind":"bytes","exit":0}` {
			// Answered twice: the second is for an id no longer
			// outstanding.
			r.send(bad)
			if err := <-got; err != nil {
				t.Fatal(err)
			}
			go func() {
				_, err := c.Do(parent.Verb{Key: "ps"})
				got <- err
			}()
			r.read()
		}
		r.send(bad)
		if err := <-got; !errors.Is(err, ErrBroken) {
			t.Errorf("%s: %v", bad, err)
		}
		if _, err := c.Do(parent.Verb{Key: "ps"}); !errors.Is(err, ErrBroken) {
			t.Errorf("%s: later %v", bad, err)
		}
		done()
	}
}

// A framing error fails every outstanding call; so do over-limit and
// connection-level protocol replies.
func TestMuxFramingErrorFailsAll(t *testing.T) {
	for _, bad := range []func(r rawAgent){
		func(r rawAgent) { r.send(strings.Repeat("x", 3)) },
		func(r rawAgent) {
			if _, err := r.conn.Write([]byte{0xff, 0xff, 0xff, 0xff}); err != nil {
				r.t.Fatal(err)
			}
		},
		func(r rawAgent) { r.conn.Close() },
		func(r rawAgent) { r.send(`{"id":2,"error":"mux_over_limit"}`) },
		func(r rawAgent) { r.send(`{"id":0,"error":"mux_protocol"}`) },
	} {
		c, r, done := helloed(t, 4)
		got := make(chan error, 3)
		for i := 0; i < 3; i++ {
			go func() {
				_, err := c.Do(parent.Verb{Key: "ps"})
				got <- err
			}()
			r.read()
		}
		bad(r)
		for i := 0; i < 3; i++ {
			if err := <-got; !errors.Is(err, ErrBroken) {
				t.Errorf("call %d: %v", i, err)
			}
		}
		done()
	}
}

// Ids are strictly increasing; at the last id the client breaks rather
// than wrap.
func TestMuxIDExhaustion(t *testing.T) {
	c, agent, done := muxPair(t, 4, 4, echoPath)
	defer done()
	if _, err := c.Concurrency(4); err != nil {
		t.Fatal(err)
	}
	c.idLimit = 3
	for i := 1; i <= 3; i++ {
		if _, err := c.Do(parent.Verb{Key: "ps", Path: "p"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := c.Do(parent.Verb{Key: "ps"}); !errors.Is(err, ErrBroken) || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("4th: %v", err)
	}
	if _, err := c.DoContext(context.Background(), parent.Verb{Key: "ps"}); !errors.Is(err, ErrBroken) {
		t.Fatalf("later: %v", err)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.lastID != 3 {
		t.Fatalf("lastID %d", agent.lastID)
	}
}

// Cancel: a queued request is answered cancelled (nothing ran); a cancel
// that crosses a response returns the verb's real result, since it ran.
func TestMuxCancel(t *testing.T) {
	release := make(chan struct{})
	c, agent, done := muxPair(t, 4, 1, func(v parent.Verb) (parent.Result, error) {
		if v.Path == "block" {
			<-release
		}
		return echoPath(v)
	})
	defer done()
	if _, err := c.Concurrency(4); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := c.Do(parent.Verb{Key: "unit.restart", Path: "block"})
		first <- err
	}()
	waitOpen(t, agent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := c.DoContext(ctx, parent.Verb{Key: "unit.restart", Path: "queued"})
		second <- err
	}()
	waitOpen(t, agent, 2)
	cancel()
	if err := <-second; !errors.Is(err, parent.ErrCancelled) {
		t.Fatalf("queued: %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}

	// Crossed: the agent answers after the cancel went out; the call
	// returns that answer.
	c2, r, done2 := helloed(t, 4)
	defer done2()
	ctx, cancel = context.WithCancel(context.Background())
	type out struct {
		res parent.Result
		err error
	}
	got := make(chan out, 1)
	go func() {
		res, err := c2.DoContext(ctx, parent.Verb{Key: "unit.restart", Path: "nginx"})
		got <- out{res, err}
	}()
	r.read()
	cancel()
	if f := string(r.read()); f != `{"cancel":1}` {
		t.Fatalf("cancel frame %s", f)
	}
	r.send(`{"id":1,"kind":"bytes","exit":0,"bytes":"` + base64.StdEncoding.EncodeToString([]byte("done")) + `"}`)
	if o := <-got; o.err != nil || string(o.res.Bytes) != "done" {
		t.Fatalf("crossed: %+v", o)
	}
	// outcome_unknown is its own error, and the pipe stays up.
	go func() {
		res, err := c2.Do(parent.Verb{Key: "package.install", Path: "x"})
		got <- out{res, err}
	}()
	r.read()
	r.send(`{"id":2,"error":"outcome_unknown"}`)
	if o := <-got; !errors.Is(o.err, parent.ErrOutcomeUnknown) {
		t.Fatalf("outcome_unknown: %+v", o)
	}
	if err := c2.brokenErr(); err != nil {
		t.Fatal(err)
	}
}

func waitOpen(t *testing.T, a *fakeAgent, n int) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		a.mu.Lock()
		got := len(a.open)
		a.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("agent never had %d outstanding", n)
}
