package lang

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

// The section 2 rules: each its exact parse error, exit 2, no door call.
func TestChanParseErrors(t *testing.T) {
	ok := "for r in recv c { echo $r }"
	for src, want := range map[string]string{
		"par 4 as r for x in [ [1] ] chan c(4) { echo $x }":                "parse: chan needs par N { }",
		"par 4 for x in [ [1] ] chan c(4) { echo $x }":                     "parse: chan needs par N { }",
		"par 4 as r chan c(4) { send c 1; " + ok + " }":                    "parse: chan needs par N { }",
		"par 4 chan c(4) for x in [ [1] ] { echo $x }":                     "parse: chan needs par N { }",
		"par 2 chan c(0) { send c 1; " + ok + " }":                         "parse: chan buffer is 1 to 64",
		"par 2 chan c(65) { send c 1; " + ok + " }":                        "parse: chan buffer is 1 to 64",
		"par 2 chan c($n) { send c 1; " + ok + " }":                        "parse: chan buffer is 1 to 64",
		"par 2 chan c(1), d(1), e(1), f(1), g(1) { send c 1; " + ok + " }": "parse: par has at most 4 chans",
		"par 2 chan c(1), c(2) { send c 1; " + ok + " }":                   "parse: chan c declared twice",
		"par 2 chan c(1) { echo a; " + ok + " }":                           "parse: chan c: no sender",
		"par 2 chan c(1) { send c 1; echo a }":                             "parse: chan c: no receiver",
		"par 2 chan c(1) { branch { send c 1; " + ok + " }; echo a }":      "parse: chan c: one branch sends and receives",
		"par 2 chan c(1) { send d 1; " + ok + " }":                         "parse: send outside its par",
		"par 2 chan c(1) { send c 1; for r in recv d { echo $r } }":        "parse: recv outside its par",
		"send c 1":                               "parse: send outside its par",
		"for r in recv c { echo $r }":            "parse: recv outside its par",
		"par 2 { send c 1; echo a }":             "parse: send outside its par",
		"fn f() { send c 1 }":                    "parse: send inside fn",
		"fn f() { for r in recv c { echo $r } }": "parse: recv inside fn",
		"par 2 chan c(1) { branch { fn f() { send c 1 }; send c 2 }; " + ok + " }": "parse: send inside fn",
		"par 2 chan c(1) { send c 1; let x = (recv c) }":                           "parse: recv only as for x in recv c",
		"par 2 chan c(1) { send c 1; recv c }":                                     "parse: recv only as for x in recv c",
		"par 2 chan c(1) { send c 1; for k v in recv c { echo $k } }":              "parse: recv only as for x in recv c",
		"par 2 chan c(1) { retry 2 { send c 1 }; " + ok + " }":                     "parse: send inside retry",
		"par 2 chan c(1) { send c 1; retry 2 { " + ok + " } }":                     "parse: recv inside retry",
		// A par inside a fn or a retry: an op directly in a fn or a retry
		// within it is still refused.
		"fn f() { par 2 chan c(1) { branch { fn g() { send c 1 }; send c 2 }; " + ok + " } }": "parse: send inside fn",
		"retry 2 { par 2 chan c(1) { retry 2 { send c 1 }; " + ok + " } }":                    "parse: send inside retry",
		"fn f() { par 2 chan c(1) { send c 1; retry 2 { " + ok + " } } }":                     "parse: recv inside retry",
		"fn send() { echo a }": "",
		"fn chan() { echo a }": "",
		"fn recv() { echo a }": "",
	} {
		f := &fake{}
		out := Run(src, allow(), f, Options{})
		if out.Status != exitType || (want != "" && !strings.Contains(out.Stderr, want)) || len(f.calls) != 0 {
			t.Errorf("%q: status %d err %q", src, out.Status, out.Stderr)
		}
	}
	// Legal: retry inside the loop body, per item; try around an op; a
	// whole channel par inside a retry (each attempt a fresh par) or a fn.
	for _, src := range []string{
		"par 2 chan c(1) { send c 1; for j in recv c { retry 2 { echo $j } } }",
		"par 2 chan c(1) { try { send c 1 } catch { echo no }; try { for j in recv c { echo $j } } catch { echo no } }",
		"retry 2 { par 2 chan c(1) { send c 1; for x in recv c { echo $x } } }",
		"fn f() { par 2 chan c(1) { send c 1; for x in recv c { echo $x } } }\nf",
	} {
		if out := Run(src, allow(), &fake{}, Options{}); out.Status != 0 || out.Stdout != "1\n" {
			t.Errorf("%q: %+v", src, out)
		}
	}
}

// pipeline is a producer, three workers, and an aggregator over n items.
func pipeline(n int) string {
	w := "for j in recv jobs { cat /srv/ops/$j; send done $j }"
	return fmt.Sprintf(`par 5 chan jobs(4), done(2) as r {
  for i in [ range(1, %d) ] { send jobs $i }
  %s
  %s
  %s
  branch { let n = 0; for d in recv done { let n = [ $n + 1 ] }; echo "seen $n" }
}`, n, w, w, w)
}

// catCounter answers cat with the base name and counts each path.
type catCounter struct {
	mu    sync.Mutex
	seen  map[string]int
	delay time.Duration
}

func (c *catCounter) Do(v parent.Verb) (parent.Result, error) {
	c.mu.Lock()
	c.seen[v.Path]++
	c.mu.Unlock()
	time.Sleep(c.delay)
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte("x\n")}, nil
}

// Producer, three workers, aggregator over 100 items: every item acted on
// once, the aggregator sees 100, output in branch order; under a 1-slot
// parent (the baton) and a mux agent alike.
func TestChanProducerWorkersAggregator(t *testing.T) {
	g := parGrant()
	g.FileRead = []string{"/srv/ops"}
	c := &catCounter{seen: map[string]int{}}
	out := Run(pipeline(100), g, c, Options{})
	if out.Status != 0 || !strings.HasSuffix(out.Stdout, "seen 100\n") || len(c.seen) != 100 {
		t.Fatalf("baton: %d %q %q %d", out.Status, out.Stdout[max(0, len(out.Stdout)-40):], out.Stderr, len(c.seen))
	}
	for p, n := range c.seen {
		if n != 1 {
			t.Fatalf("%s read %d times", p, n)
		}
	}
	a := &prototest.Agent{Max: 3, Handle: func(v parent.Verb) (parent.Result, error) {
		time.Sleep(time.Millisecond)
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("x\n")}, nil
	}}
	mc, done := muxClient(t, a)
	out2 := Run(pipeline(100), g, mc, Options{})
	done()
	st := a.Stats()
	if out2.Status != 0 || !strings.HasSuffix(out2.Stdout, "seen 100\n") || st.Requests != 100 || st.MaxRunning > 3 || st.MaxRunning < 2 {
		t.Fatalf("mux: %d %q %+v", out2.Status, out2.Stderr, st)
	}
}

// Two producers: the channel closes when the last one ends, not the
// first, so no item is lost.
func TestChanCloseOnLastSender(t *testing.T) {
	src := `par 3 chan c(1) {
  for i in [ [1, 2, 3] ] { send c $i }
  for i in [ [4, 5, 6, 7, 8] ] { send c $i }
  branch { let n = 0; for x in recv c { let n = [ $n + 1 ] }; echo $n }
}`
	if out := Run(src, allow(), &fake{}, Options{}); out.Status != 0 || out.Stdout != "8\n" {
		t.Fatalf("%+v", out)
	}
}

func TestChanValueRefusals(t *testing.T) {
	g := parGrant()
	big := strings.Repeat("a", 3<<20)
	for src, want := range map[string]string{
		"let b = (cat /srv/ops/f)\npar 2 chan c(1) { send c $b; for x in recv c { echo $x } }":                                                   "type send: bytes cannot cross a channel; lift with lines or rows",
		"let b = (cat /srv/ops/f)\npar 2 chan c(1) { send c [ $b ]; for x in recv c { echo $x } }":                                               "type send: bytes cannot cross a channel; lift with lines or rows",
		"let b = (cat /srv/ops/f)\npar 2 chan c(1) { send c [ [ {\"k\": $b} ] ]; for x in recv c { echo $x } }":                                  "type send: bytes cannot cross a channel; lift with lines or rows",
		"let b = (cat /srv/ops/f)\npar 2 chan c(1) { send c ${b}; for x in recv c { echo $x } }":                                                 "type send: bytes cannot cross a channel; lift with lines or rows",
		"let s = (cat /srv/ops/g | lines)\npar 2 chan c(1) { send c $s; for x in recv c { echo $x } }":                                           "send c: value over 8 MiB",
		"let s = [ repeat(\"a\", 3145728) ]\npar 2 chan c(64) { for i in [ range(1, 20) ] { send c $s }; for x in recv c { echo [ len($x) ] } }": "send c: buffers over 32 MiB",
	} {
		p := &tplParent{files: map[string]string{"/srv/ops/f": big, "/srv/ops/g": strings.Repeat(strings.Repeat("a", 1000)+"\n", 9000)}}
		out := Run(src, g, p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, want) {
			t.Errorf("%q: %d %q", src[:30], out.Status, firstLine(out.Stderr))
		}
	}
	// Text made from bytes is text, as echo makes it: it crosses.
	for src, want := range map[string]string{
		"let b = (cat /srv/ops/f)\npar 2 chan c(1) { send c [ len($b) ]; for x in recv c { echo $x } }":         "4\n",
		"let b = (cat /srv/ops/f)\npar 2 chan c(1) { send c \"x$b\"; for x in recv c { echo $x } }":             "xabc\n\n",
		"let b = (cat /srv/ops/f)\nlet s = \"x$b\"\npar 2 chan c(1) { send c $s; for x in recv c { echo $x } }": "xabc\n\n",
	} {
		out := Run(src, g, &tplParent{files: map[string]string{"/srv/ops/f": "abc\n"}}, Options{})
		if out.Status != 0 || out.Stdout != want {
			t.Errorf("%q: %+v", src, out)
		}
	}
}

// A row set sent as a lone $r arrives as rows, with its provenance: the
// receiver's echo $x fails as echo $r does, for walks its rows, and origin
// names the sender's read.
func TestChanRowsKeepKind(t *testing.T) {
	alone := Run("let r = (ps)\necho $r", recGrant(), &hostParent{}, Options{})
	out := Run("let r = (ps)\npar 2 chan c(1) { send c $r; for x in recv c { echo $x } }", recGrant(), &hostParent{}, Options{})
	if alone.Status != exitType || alone.Stderr != "type value\n" || out.Status != exitType || out.Stdout != "" || !strings.HasPrefix(out.Stderr, alone.Stderr) {
		t.Fatalf("echo: alone %+v, received %+v", alone, out)
	}
	out = Run("let r = (ps)\npar 2 chan c(1) { send c $r; for x in recv c { for w in $x { echo $w.user }; origin x |> select key } }", recGrant(), &hostParent{}, Options{})
	if out.Status != 0 || out.Stdout != "root\nryan\nps\n" {
		t.Fatalf("for: %+v", out)
	}
}

// Every node of a value counts, as jsonValue charges it: a list of empty
// strings sent again and again fills the par's 32 MiB, never counted as
// nothing, and so do empty lists and rows of empty cells.
func TestChanSizeCountsNodes(t *testing.T) {
	empty := make([]any, 1000)
	for i := range empty {
		empty[i] = ""
	}
	_, n, why := chanCopy(value{kind: valData, d: empty})
	if why != "" || n < 1000*chanNodeBytes {
		t.Fatalf("empty strings: %d %q", n, why)
	}
	_, n, _ = chanCopy(value{kind: valData, d: []any{[]any{}, &omap{m: map[string]any{}}}})
	if n < 3*chanNodeBytes {
		t.Fatalf("empty list and map: %d", n)
	}
	rows := make([]parent.Row, 1000)
	for i := range rows {
		rows[i] = parent.Row{"a": ""}
	}
	if _, n, _ = chanCopy(value{kind: valRows, rows: rows}); n < 2000*chanNodeBytes {
		t.Fatalf("rows: %d", n)
	}
	// 300000 empty strings are about 7 MiB: four fit the par, a fifth
	// does not.
	body := "[" + strings.Repeat(`"",`, 299999) + `""]`
	p := &tplParent{files: map[string]string{"/srv/ops/g": body}}
	src := "let s = (cat /srv/ops/g | from json)\npar 2 chan c(64) { for i in [ range(1, 16) ] { send c $s }; for x in recv c { break } }"
	out := Run(src, parGrant(), p, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "send c: buffers over 32 MiB") {
		t.Fatalf("%d %q", out.Status, firstLine(out.Stderr))
	}
}

// A value nested past 128, or of a datum type that cannot cross, says so,
// not that it is over 8 MiB.
func TestChanCopyRefusals(t *testing.T) {
	var deep any = "x"
	for i := 0; i < 200; i++ {
		deep = []any{deep}
	}
	if _, _, why := chanCopy(value{kind: valData, d: deep}); why != "value nested over 128 deep" {
		t.Errorf("deep: %q", why)
	}
	if _, _, why := chanCopy(value{kind: valData, d: []any{struct{}{}}}); why != "value of a kind that cannot cross" {
		t.Errorf("kind: %q", why)
	}
	if _, _, why := chanCopy(value{kind: valBytes, b: []byte("x")}); why != "value of a kind that cannot cross" {
		t.Errorf("bytes: %q", why)
	}
	if _, _, why := chanCopy(value{kind: valStr, s: strings.Repeat("a", dataMaxBytes)}); why != "value over 8 MiB" {
		t.Errorf("big: %q", why)
	}
}

// Receivers break early: a producer with room-less buffer gets no
// receiver left (exit 1); values left queued fail the par as unread.
func TestChanNoReceiverAndUnread(t *testing.T) {
	src := "par 2 chan c(1) { for i in [ range(1, 5) ] { send c $i }; for x in recv c { break } }"
	out := Run(src, allow(), &fake{}, Options{})
	if out.Status != 1 || !strings.Contains(out.Stderr, "send c: no receiver left") {
		t.Fatalf("no receiver: %+v", out)
	}
	src = "par 2 chan c(4) { for i in [ range(1, 3) ] { send c $i }; branch { for x in recv c { break } } }"
	out = Run(src+"\necho after", allow(), &fake{}, Options{})
	if out.Status != 1 || !strings.Contains(out.Stderr, "par: chan c: 2 unread") || strings.Contains(out.Stdout, "after") {
		t.Fatalf("unread: %+v", out)
	}
	// A failed par still names its unread values, after the failure line.
	src = "par 2 chan c(4) { branch { for i in [ range(1, 3) ] { send c $i }; egrep zz <<< a }; for x in recv c { break } }"
	out = Run(src, allow(), &fake{}, Options{})
	if i, j := strings.Index(out.Stderr, "par: branch 1 failed"), strings.Index(out.Stderr, "par: chan c: 3 unread"); out.Status == 0 || i < 0 || j < i {
		t.Fatalf("failed and unread: %+v", out)
	}
}

// Two branches each waiting on the other's channel: a deadlock, exit 2,
// a halt try does not catch, at once.
func TestChanDeadlock(t *testing.T) {
	src := `try {
  par 2 chan a(1), b(1) {
    for x in recv a { send b $x }
    for y in recv b { send a $y }
  }
} catch { echo caught }
echo after`
	start := time.Now()
	out := Run(src, allow(), &fake{}, Options{})
	if time.Since(start) > time.Second || out.Status != exitType || strings.Contains(out.Stdout, "caught") || strings.Contains(out.Stdout, "after") ||
		!strings.Contains(out.Stderr, "par: deadlock: branch 1 recv a; branch 2 recv b") {
		t.Fatalf("%+v", out)
	}
	// A full buffer and a receiver waiting elsewhere.
	src = "par 2 chan a(1), b(1) { branch { send a 1; send a 2; send b 3 }; branch { for y in recv b { echo $y }; for x in recv a { echo $x } } }"
	out = Run(src, allow(), &fake{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "par: deadlock: branch 1 send a; branch 2 recv b") {
		t.Fatalf("full: %+v", out)
	}
}

// A producer failing mid-stream closes its channel: the workers drain
// and end, and the par names the producer.
func TestChanProducerFails(t *testing.T) {
	src := "par 2 chan c(8) as r { branch { send c 1; send c 2; egrep zz <<< a }; for x in recv c { echo $x } }"
	out := Run(src, allow(), &fake{}, Options{})
	if out.Status == 0 || !strings.Contains(out.Stderr, "par: branch 1 failed") || strings.Contains(out.Stderr, "deadlock") {
		t.Fatalf("%+v", out)
	}
}

// A worker failing cancels the par: the producer blocked on a full
// buffer wakes as cancelled.
func TestChanWorkerFailsWakesProducer(t *testing.T) {
	src := "par 2 chan c(1) as r { for i in [ range(1, 10) ] { send c $i }; for x in recv c { echo $nope } }\necho done"
	out := Run("try { "+src+" } catch e { echo $e.status }\necho [ $r[0].message ]", allow(), &fake{}, Options{})
	if !strings.Contains(out.Stdout, "cancelled") || strings.Contains(out.Stderr, "deadlock") {
		t.Fatalf("%+v", out)
	}
}

// The first Ctrl-C while every branch waits on a channel (a reader
// blocks the only sender in a door call) ends the par at once, and no
// goroutine is left parked.
func TestChanInterruptWakesWaiters(t *testing.T) {
	base := runtime.NumGoroutine()
	release := make(chan struct{})
	a := &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		<-release
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("x\n")}, nil
	}}
	c, done := muxClient(t, a)
	intr := make(chan struct{}, 1)
	opt := Options{Interrupts: func() (<-chan struct{}, func()) { return intr, func() {} }}
	var ct *chanTable
	var hookMu sync.Mutex
	chanTableHook = func(t *chanTable) { hookMu.Lock(); ct = t; hookMu.Unlock() }
	defer func() { chanTableHook = nil }()
	parked := func() int {
		hookMu.Lock()
		defer hookMu.Unlock()
		if ct == nil {
			return -1
		}
		ct.mu.Lock()
		defer ct.mu.Unlock()
		return len(ct.waiters)
	}
	go func() {
		waitFor(t, "read running", func() bool { return a.Stats().Running == 1 && parked() == 2 })
		intr <- struct{}{}
		// The first cancel wakes every waiter while the door call still
		// runs: nothing stays parked on a channel.
		waitFor(t, "waiters woken", func() bool { return parked() == 0 })
		close(release)
	}()
	src := "par 3 chan c(1) { branch { cat /srv/ops/a; send c 1 }; for x in recv c { echo $x }; for x in recv c { echo $x } }"
	out := Run(src, parGrant(), c, opt)
	done()
	if out.Status != exitInterrupted || !strings.Contains(out.Stderr, "par: interrupted") {
		t.Fatalf("%+v", out)
	}
	waitFor(t, "goroutines back to baseline", func() bool { return runtime.NumGoroutine() <= base+1 })
}

// gateParent is a parent without the multiplexed pipe (the baton): each
// call says it started, then waits for release.
type gateParent struct {
	in      chan struct{}
	release chan struct{}
}

func (g *gateParent) Do(v parent.Verb) (parent.Result, error) {
	g.in <- struct{}{}
	<-g.release
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte("x\n")}, nil
}

// Under the baton, the first Ctrl-C while the holder is in a door call
// leaves no branch waiting for the baton behind it: the two receivers,
// one woken from its channel and one never started, end at once, before
// the door call returns.
func TestChanInterruptBaton(t *testing.T) {
	g := &gateParent{in: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(g.release) }) }
	defer release()
	intr := make(chan struct{}, 1)
	opt := Options{Interrupts: func() (<-chan struct{}, func()) { return intr, func() {} }}
	var ct *chanTable
	var hookMu sync.Mutex
	chanTableHook = func(t *chanTable) { hookMu.Lock(); ct = t; hookMu.Unlock() }
	defer func() { chanTableHook = nil }()
	state := func() (running, waiters, want int) {
		hookMu.Lock()
		defer hookMu.Unlock()
		ct.mu.Lock()
		defer ct.mu.Unlock()
		return ct.running, len(ct.waiters), len(ct.want)
	}
	src := "par 3 chan c(1) { branch { send c 1; send c 2; cat /srv/ops/a; send c 3 }; for x in recv c { echo $x }; for x in recv c { echo $x } }"
	done := make(chan Outcome, 1)
	go func() { done <- Run(src, parGrant(), g, opt) }()
	<-g.in
	// Branch 1 holds the baton in its door call; the other two are
	// runnable and wait for it.
	if r, w, b := state(); r != 3 || w != 0 || b != 2 {
		t.Fatalf("before: running %d waiters %d want %d", r, w, b)
	}
	intr <- struct{}{}
	waitFor(t, "receivers ended", func() bool { r, w, b := state(); return r == 1 && w == 0 && b == 0 })
	release()
	out := <-done
	if out.Status != exitInterrupted || !strings.Contains(out.Stderr, "par: interrupted") {
		t.Fatalf("%+v", out)
	}
}

// A deadline wakes a blocked op: timeout, which try catches.
func TestChanTimeout(t *testing.T) {
	a := &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		time.Sleep(time.Second)
		return parent.Result{Kind: parent.KindBytes}, nil
	}}
	c, done := muxClient(t, a)
	defer done()
	src := "try { timeout 200ms { par 2 chan c(1) { branch { cat /srv/ops/a; send c 1 }; for x in recv c { echo $x } } } } catch e { echo $e.status }"
	start := time.Now()
	out := Run(src, parGrant(), c, Options{})
	if time.Since(start) > 3*time.Second || out.Stdout != "2\n" {
		t.Fatalf("%+v", out)
	}
	// Under the baton: the receiver parks on the channel while the
	// sender holds the baton in a door call past the deadline. The
	// sender's next statement is timeout, which ends the par; the
	// receiver is woken, never left parked.
	src = "try { timeout 200ms { par 2 chan c(1) as r { for x in recv c { echo $x }; branch { cat /srv/ops/a; send c 1 } } } } catch e { echo $e.status }\necho [ $r[1].message ]"
	start = time.Now()
	out = Run(src, parGrant(), &catCounter{seen: map[string]int{}, delay: time.Second}, Options{})
	if time.Since(start) > 3*time.Second || out.Stdout != "x\n2\ntimeout\n" {
		t.Fatalf("baton: %v %+v", time.Since(start), out)
	}
}

// Under confirm the branches run one at a time in a fixed order: the same
// prompts in the same order on every run, and a 1-slot buffer is no false
// deadlock.
func TestChanConfirmDeterministic(t *testing.T) {
	src := `set -o confirm
par 3 chan c(1) {
  for u in [ ["a", "b", "c", "d"] ] { unit restart $u; send c $u }
  for x in recv c { unit restart "w1-$x" }
  for x in recv c { unit restart "w2-$x" }
}`
	var first string
	for i := 0; i < 20; i++ {
		var asked []string
		opt := Options{State: NewState(), Confirm: func(p string) (bool, bool) { asked = append(asked, p); return true, false }}
		out := Run(src, parGrant(), &fake{}, opt)
		if out.Status != 0 || len(asked) != 8 {
			t.Fatalf("%+v %q", out, asked)
		}
		if got := strings.Join(asked, "\n"); i == 0 {
			// Lowest-numbered runnable branch first, switching only when
			// it blocks on a channel or ends.
			var want []string
			for _, u := range []string{"a", "b", "w1-a", "c", "w1-b", "d", "w1-c", "w1-d"} {
				want = append(want, "confirm unit.restart unit restart "+u+"? [y + Enter / N]")
			}
			if got != strings.Join(want, "\n") {
				t.Fatalf("order %q", asked)
			}
			first = got
		} else if got != first {
			t.Fatalf("run %d: %q\nfirst: %q", i, got, first)
		}
	}
}

// chanRecSrc is a producer, three workers, and an aggregator over 50
// items, for record and replay.
const chanRecSrc = `par 5 chan jobs(4), done(2) {
  for i in [ range(1, 50) ] { send jobs $i }
  for j in recv jobs { cat /srv/ops/$j; send done [ {"j": $j} ] }
  for j in recv jobs { cat /srv/ops/$j; send done [ {"j": $j} ] }
  for j in recv jobs { cat /srv/ops/$j; send done [ {"j": $j} ] }
  branch { let n = 0; for d in recv done { let n = [ $n + 1 ] }; echo "seen $n" }
}`

// chanEvents is the index of every chan event line of a recording.
func chanEvents(t *testing.T, lines [][]byte) []int {
	t.Helper()
	var out []int
	for i, l := range lines[1:] {
		var ev recEvent
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Kind == "chan" {
			out = append(out, i+1)
		}
	}
	return out
}

func chanEv(t *testing.T, line []byte) recEvent {
	t.Helper()
	var ev recEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// A par with channels records every send, recv, and close with one cseq
// per par and the value's hash, and replays to the same output in the
// recorded order. An edited cseq (moved or duplicated), an edited hash,
// and a deleted chan event each diverge.
func TestChanRecordReplay(t *testing.T) {
	rec, out := record(t, chanRecSrc)
	if out.Status != 0 || !strings.HasSuffix(out.Stdout, "seen 50\n") {
		t.Fatalf("record: %+v", out)
	}
	lines := recLines(rec)
	evs := chanEvents(t, lines)
	// 50 sends and recvs on jobs and on done, and a close of each.
	if len(evs) != 202 {
		t.Fatalf("%d chan events", len(evs))
	}
	for k, i := range evs {
		ev := chanEv(t, lines[i])
		if ev.CSeq != k+1 || ev.Branch == "" || (ev.Op != "close" && len(ev.Hash) != 64) {
			t.Fatalf("event %d: %s", k, lines[i])
		}
	}
	got, err := replay(t, rec, chanRecSrc)
	if err != nil || got.Stdout != out.Stdout || got.Status != 0 {
		t.Fatalf("replay: %v %+v", err, got)
	}

	tampered := map[string][]byte{}
	// Two workers' receives on jobs, their cseqs swapped: each would
	// take the other's item.
	for k := 0; k < len(evs) && tampered["swapped cseq"] == nil; k++ {
		a := chanEv(t, lines[evs[k]])
		for j := k + 1; j < len(evs) && a.Op == "recv" && a.Chan == "jobs"; j++ {
			if b := chanEv(t, lines[evs[j]]); b.Op == "recv" && b.Chan == "jobs" && b.Branch != a.Branch {
				c := append([][]byte(nil), lines...)
				c[evs[k]] = edit(t, c[evs[k]], func(m map[string]any) { m["cseq"] = b.CSeq })
				c[evs[j]] = edit(t, c[evs[j]], func(m map[string]any) { m["cseq"] = a.CSeq })
				tampered["swapped cseq"] = rechain(t, c)
				break
			}
		}
	}
	c := append([][]byte(nil), lines...)
	c[evs[10]] = edit(t, c[evs[10]], func(m map[string]any) { m["cseq"] = 10 })
	tampered["duplicate cseq"] = rechain(t, c)
	c = append([][]byte(nil), lines...)
	c[evs[20]] = edit(t, c[evs[20]], func(m map[string]any) { m["cseq"] = 9999 })
	tampered["cseq out of range"] = rechain(t, c)
	for _, k := range []int{3, 60} {
		c = append([][]byte(nil), lines...)
		c[evs[k]] = edit(t, c[evs[k]], func(m map[string]any) { m["hash"] = strings.Repeat("0", 64) })
		tampered[fmt.Sprintf("hash %d", k)] = rechain(t, c)
	}
	for _, k := range []int{7, len(evs) - 1} {
		c = append(append([][]byte(nil), lines[:evs[k]]...), lines[evs[k]+1:]...)
		tampered[fmt.Sprintf("deleted %d", k)] = rechain(t, c)
	}
	if len(tampered) != 7 {
		t.Fatalf("%d cases", len(tampered))
	}
	for name, r := range tampered {
		got, err := replay(t, r, chanRecSrc)
		if diverged(err) < 0 || got.Status != exitType || !strings.Contains(got.Stderr, "replay_diverged") {
			t.Errorf("%s: %v %d %q", name, err, got.Status, got.Stderr)
		}
	}
}

// A redacted event matches only a value that is secret: a recording with
// every hash replaced by redacted is replay_diverged, and so is one that
// also swaps two workers' receives, which only the hashes could see.
func TestChanReplayRedactedTamper(t *testing.T) {
	redactAll := func(c [][]byte, evs []int) {
		for _, i := range evs {
			if chanEv(t, c[i]).Op != "close" {
				c[i] = edit(t, c[i], func(m map[string]any) { delete(m, "hash"); m["redacted"] = true })
			}
		}
	}
	rec, _ := record(t, chanRecSrc)
	lines := recLines(rec)
	evs := chanEvents(t, lines)
	c := append([][]byte(nil), lines...)
	redactAll(c, evs)
	if got, err := replay(t, rechain(t, c), chanRecSrc); diverged(err) != chanEv(t, lines[evs[0]]).Seq {
		t.Errorf("redacted: %v %+v", err, got)
	}

	src := "par 3 chan c(1) { for i in [ range(1, 10) ] { send c $i }; for j in recv c { echo \"w1 $j\"; break }; for j in recv c { echo \"w2 $j\" } }"
	rec, _ = record(t, src)
	lines = recLines(rec)
	evs = chanEvents(t, lines)
	c = append([][]byte(nil), lines...)
	swapped := false
	for k := 0; k < len(evs) && !swapped; k++ {
		a := chanEv(t, c[evs[k]])
		for j := k + 1; j < len(evs) && a.Op == "recv"; j++ {
			if b := chanEv(t, c[evs[j]]); b.Op == "recv" && b.Branch != a.Branch {
				c[evs[k]] = edit(t, c[evs[k]], func(m map[string]any) { m["cseq"] = b.CSeq })
				c[evs[j]] = edit(t, c[evs[j]], func(m map[string]any) { m["cseq"] = a.CSeq })
				swapped = true
				break
			}
		}
	}
	redactAll(c, evs)
	if got, err := replay(t, rechain(t, c), src); !swapped || diverged(err) < 0 {
		t.Errorf("swapped and redacted: %v %+v", err, got)
	}

	// A *** in a value, from host data or the script, is not a response
	// replay served redacted: the swap and the redaction still diverge.
	for _, sc := range []struct{ src, file string }{
		{"par 3 chan c(1) { branch { let s = (cat /srv/ops/s | lines); send c [ $s[0].line ]; for i in [ range(1, 10) ] { send c $i } }; for j in recv c { echo \"w1 $j\"; break }; for j in recv c { echo \"w2 $j\" } }", "masked: ***\n"},
		{"par 3 chan c(1) { branch { send c [ \"***\" ]; for i in [ range(1, 10) ] { send c $i } }; for j in recv c { echo \"w1 $j\"; break }; for j in recv c { echo \"w2 $j\" } }", ""},
		{"par 3 chan c(1) { for i in [ range(1, 10) ] { send c \"*** $i\" }; for j in recv c { echo \"w1 $j\"; break }; for j in recv c { echo \"w2 $j\" } }", ""},
	} {
		rec := recordFiles(t, sc.src, map[string]string{"/srv/ops/s": sc.file})
		if _, err := replay(t, rec, sc.src); err != nil {
			t.Fatalf("%q: honest replay %v", sc.src, err)
		}
		lines := recLines(rec)
		evs := chanEvents(t, lines)
		c := append([][]byte(nil), lines...)
		swapped := false
		for k := 0; k < len(evs) && !swapped; k++ {
			a := chanEv(t, c[evs[k]])
			for j := k + 1; j < len(evs) && a.Op == "recv"; j++ {
				if b := chanEv(t, c[evs[j]]); b.Op == "recv" && b.Branch != a.Branch {
					c[evs[k]] = edit(t, c[evs[k]], func(m map[string]any) { m["cseq"] = b.CSeq })
					c[evs[j]] = edit(t, c[evs[j]], func(m map[string]any) { m["cseq"] = a.CSeq })
					swapped = true
					break
				}
			}
		}
		redactAll(c, evs)
		if got, err := replay(t, rechain(t, c), sc.src); !swapped || diverged(err) < 0 {
			t.Errorf("*** %q: %v %+v", sc.src, err, got)
		}
	}

	// Honest recordings still replay: a value made from a response the
	// recorder redacted holds *** on replay, not the secret's text, with
	// the secret bound before the par or after it.
	for _, src := range []string{
		"par 2 chan c(1) { branch { let l = (cat /srv/ops/u | lines); send c $l }; for j in recv c { echo got } }\ndeclare -s tok = \"hunter2-SECRET\"",
		"declare -s tok = \"hunter2-SECRET\"\npar 2 chan c(1) { branch { let l = (cat /srv/ops/u | lines); send c [ \"x\" + $l[0].line ] }; for j in recv c { echo got } }",
		"par 2 chan c(1) { branch { let l = (cat /srv/ops/u | lines); send c [ \"x\" + $l[0].line ] }; for j in recv c { echo got } }\ndeclare -s tok = \"hunter2-SECRET\"",
	} {
		rec := recordFiles(t, src, map[string]string{"/srv/ops/u": "hunter2-SECRET\n"})
		if got, err := replay(t, rec, src); err != nil || got.Status != 0 {
			t.Errorf("%q: %v %+v", src, err, got)
		}
	}
	// The same with the redacted response's origin lost to an overflow:
	// the branch has had a redacted response, so the overflow may hide it.
	files := map[string]string{"/srv/ops/u": "hunter2-SECRET\n"}
	var b strings.Builder
	sum := ""
	for i := 0; i < 17; i++ {
		files[fmt.Sprintf("/srv/ops/f%d", i)] = fmt.Sprintf("f%d\n", i)
		fmt.Fprintf(&b, "let c%d = (cat /srv/ops/f%d | lines); ", i, i)
		sum += fmt.Sprintf("$c%d[0].line + ", i)
	}
	src = "declare -s tok = \"hunter2-SECRET\"\npar 2 chan c(1) { branch { " + b.String() + "let l = (cat /srv/ops/u | lines); send c [ " + sum + "$l[0].line ] }; for j in recv c { echo got } }"
	if got, err := replay(t, recordFiles(t, src, files), src); err != nil || got.Status != 0 {
		t.Errorf("overflow: %v %+v", err, got)
	}
	// Two 8 MB values from a redacted response fill the keep budget on
	// record, so the plain value after them is redacted for the budget
	// alone; on replay they are *** and small, and the budget is no
	// longer followed past the first of them.
	src = "declare -s tok = \"hunter2-SECRET\"\npar 2 chan c(1) { branch { let l = (cat /srv/ops/u | lines); send c $l; send c $l; send c [ \"b\" * 1000000 ] }; for j in recv c { echo got } }"
	rec = recordFiles(t, src, map[string]string{"/srv/ops/u": "hunter2-SECRET" + strings.Repeat("a", 8000000) + "\n"})
	lines = recLines(rec)
	n := 0
	for _, i := range chanEvents(t, lines) {
		if ev := chanEv(t, lines[i]); ev.Op == "send" && ev.Redacted {
			n++
		}
	}
	if got, err := replay(t, rec, src); n != 3 || err != nil || got.Status != 0 {
		t.Errorf("budget: %d redacted sends, %v %+v", n, err, got)
	}
}

// redactResp makes event line i's response a redacted one, as a forger
// would.
func redactResp(t *testing.T, c [][]byte, i int) {
	t.Helper()
	c[i] = edit(t, c[i], func(m map[string]any) {
		r := m["resp"].(map[string]any)
		m["resp"] = map[string]any{"kind": r["kind"], "exit": r["exit"], "redacted": true, "len": 5}
	})
}

// firstCall is the index of a recording's first call event.
func firstCall(t *testing.T, c [][]byte) int {
	t.Helper()
	for i, l := range c[1:] {
		if ev := chanEv(t, l); ev.Kind == "call" && ev.Resp != nil {
			return i + 1
		}
	}
	t.Fatal("no call")
	return 0
}

// What marks a value as made from something replay served as ***
// belongs to that value, not to its branch: a redacted response the
// branch read for something else does not excuse a value whose
// provenance overflowed. The value that does hold one still replays,
// however far it overflowed (TestChanReplayRedactedTamper), and so does
// a term answer the recorder wrote as ***.
func TestChanReplayStarsPerValue(t *testing.T) {
	files := map[string]string{"/srv/ops/z": "unrelated\n"}
	var b strings.Builder
	sum := ""
	for i := 0; i < 17; i++ {
		files[fmt.Sprintf("/srv/ops/f%d", i)] = fmt.Sprintf("f%d\n", i)
		fmt.Fprintf(&b, "let c%d = (cat /srv/ops/f%d | lines); ", i, i)
		sum += fmt.Sprintf(" + $c%d[0].line", i)
	}
	src := "par 2 chan c(1) { branch { let z = (cat /srv/ops/z); " + b.String() + "let y = [ \"\"" + sum + " ]; send c $y }; for j in recv c { echo \"got $j\" } }"
	rec := recordFiles(t, src, files)
	for _, forged := range []bool{false, true} {
		c := append([][]byte(nil), recLines(rec)...)
		for _, i := range chanEvents(t, c) {
			if chanEv(t, c[i]).Op != "close" {
				c[i] = edit(t, c[i], func(m map[string]any) { delete(m, "hash"); m["redacted"] = true })
			}
		}
		if forged {
			redactResp(t, c, firstCall(t, c))
		}
		if got, err := replay(t, rechain(t, c), src); diverged(err) < 0 {
			t.Errorf("forged %v: %v %+v", forged, err, got)
		}
	}

	src = "let a = (term prompt \"x?\")\npar 2 chan c(1) { send c $a; for j in recv c { echo got } }\ndeclare -s tok = \"hunter2-SECRET\""
	clocks(t)
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, recNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(src, termGrant(), &hostParent{}, Options{Tape: tape, State: NewState(), Term: &fakeTerm{answers: []string{"my hunter2-SECRET"}}})
	if err := tape.Finish(); err != nil || out.Status != 0 {
		t.Fatalf("record: %v %+v", err, out)
	}
	if got, err := replay(t, buf.Bytes(), src); err != nil || got.Status != 0 {
		t.Errorf("term answer: %v %+v", err, got)
	}
}

// One forged redacted response feeding a send cannot claim the whole
// keep budget, and so switch off the value checks of every later send:
// a value's share is at most dataMaxBytes.
func TestChanReplayKeepForged(t *testing.T) {
	src := "par 3 chan c(1) { branch { let s = (cat /srv/ops/s | lines); send c [ $s[0].line ]; for i in [ range(1, 10) ] { send c $i } }; for j in recv c { echo \"w1 $j\"; break }; for j in recv c { echo \"w2 $j\" } }"
	rec := recordFiles(t, src, map[string]string{"/srv/ops/s": "plain line\n"})
	c := append([][]byte(nil), recLines(rec)...)
	evs := chanEvents(t, c)
	for _, i := range evs {
		ev := chanEv(t, c[i])
		if ev.Op == "close" {
			continue
		}
		c[i] = edit(t, c[i], func(m map[string]any) {
			delete(m, "hash")
			m["redacted"] = true
			if ev.Op == "send" {
				m["keep"] = chanKeepMax
			}
		})
	}
	redactResp(t, c, firstCall(t, c))
	if got, err := replay(t, rechain(t, c), src); diverged(err) < 0 {
		t.Errorf("forged keep: %v %+v", err, got)
	}
}

// Every call sets whether replay served it redacted, and a glob listing
// takes it: a redacted listing never marks the next verb's value.
func TestTapeServedRedactedPairs(t *testing.T) {
	src := "declare -s tok = \"content of /var/log/a\"\ncat /var/log/a\ncat /var/log/b"
	rec, _ := record(t, src)
	tape, err := LoadReplay(bytes.NewReader(rec), src, recNow)
	if err != nil {
		t.Fatal(err)
	}
	verb := func(p string) parent.Verb {
		return parent.Verb{Key: "file.read", Argv: []string{"cat", p}, Path: p}
	}
	// The first response is redacted in the recording.
	check, err := LoadReplay(bytes.NewReader(rec), src, recNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := check.call(nil, verb("/var/log/a"), false, nil); err != nil || !check.takeServedRedacted() {
		t.Fatalf("first call not served redacted: %v", err)
	}
	if _, err := (tapeParent{t: tape}).Do(verb("/var/log/a")); err != nil || tape.servedRedacted {
		t.Fatalf("listing left the flag: %v %v", err, tape.servedRedacted)
	}
	if _, err := tape.call(nil, verb("/var/log/b"), false, nil); err != nil || tape.takeServedRedacted() {
		t.Fatalf("plain call marked: %v", err)
	}
}

// A send's keep total is checked: missing, on another op, or past the
// budget is replay_bad_format; one that goes down or does not follow the
// value's cost is replay_diverged.
func TestChanReplayKeepTotal(t *testing.T) {
	rec, _ := record(t, chanRecSrc)
	lines := recLines(rec)
	evs := chanEvents(t, lines)
	var sends, recvs []int
	for _, i := range evs {
		switch chanEv(t, lines[i]).Op {
		case "send":
			sends = append(sends, i)
		case "recv":
			recvs = append(recvs, i)
		}
	}
	try := func(i int, f func(map[string]any)) error {
		c := append([][]byte(nil), lines...)
		c[i] = edit(t, c[i], f)
		_, err := replay(t, rechain(t, c), chanRecSrc)
		return err
	}
	for name, err := range map[string]error{
		"missing":     try(sends[0], func(m map[string]any) { delete(m, "keep") }),
		"on a recv":   try(recvs[0], func(m map[string]any) { m["keep"] = 0 }),
		"past budget": try(sends[3], func(m map[string]any) { m["keep"] = chanKeepMax + 1 }),
		"negative":    try(sends[3], func(m map[string]any) { m["keep"] = -1 }),
	} {
		if err == nil || !strings.Contains(err.Error(), "replay_bad_format") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"goes down":    try(sends[5], func(m map[string]any) { m["keep"] = 0 }),
		"off by one":   try(sends[5], func(m map[string]any) { m["keep"] = m["keep"].(float64) + 1 }),
		"not followed": try(sends[0], func(m map[string]any) { m["keep"] = 0 }),
		// The last send claims the budget was full and is redacted: it
		// was not, so that is not why it was redacted.
		"claims the budget": try(sends[len(sends)-1], func(m map[string]any) {
			m["keep"] = chanEv(t, lines[sends[len(sends)-2]]).Keep
			delete(m, "hash")
			m["redacted"] = true
		}),
	} {
		if diverged(err) < 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A chan event's shape is checked at load: a hash and redacted together,
// an unknown op, a chan field on another kind, or a chan event outside a
// par is replay_bad_format.
func TestChanRecordBadFormat(t *testing.T) {
	rec, _ := record(t, chanRecSrc)
	lines := recLines(rec)
	evs := chanEvents(t, lines)
	for name, f := range map[string]func(map[string]any){
		"hash and redacted": func(m map[string]any) { m["redacted"] = true },
		"unknown op":        func(m map[string]any) { m["op"] = "peek" },
		"no cseq":           func(m map[string]any) { delete(m, "cseq") },
		"outside par":       func(m map[string]any) { delete(m, "branch"); delete(m, "bseq") },
		"not chan":          func(m map[string]any) { m["kind"] = "now" },
	} {
		c := append([][]byte(nil), lines...)
		c[evs[0]] = edit(t, c[evs[0]], f)
		if _, err := replay(t, rechain(t, c), chanRecSrc); err == nil || !strings.Contains(err.Error(), "replay_bad_format") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A secret sent is recorded redacted, with no hash and no text, and the
// recording still replays.
func TestChanRecordSecretRedacted(t *testing.T) {
	src := "declare -s s = \"hunter2-long\"\npar 3 chan c(1), d(1) { send c $s; for x in recv c { send d [ {\"k\": $x} ] }; for y in recv d { echo $y } }"
	rec, out := record(t, src)
	if out.Status != 0 || out.Stdout != "***\n" || bytes.Contains(rec, []byte("hunter2")) {
		t.Fatalf("%+v\n%s", out, rec)
	}
	for _, i := range chanEvents(t, recLines(rec)) {
		ev := chanEv(t, recLines(rec)[i])
		if ev.Op != "close" && (!ev.Redacted || ev.Hash != "") {
			t.Fatalf("not redacted: %+v", ev)
		}
	}
	if got, err := replay(t, rec, src); err != nil || got.Stdout != out.Stdout {
		t.Fatalf("replay: %v %+v", err, got)
	}
}

// recordFiles records src against a parent that serves files, and
// returns the recording.
func recordFiles(t *testing.T, src string, files map[string]string) []byte {
	t.Helper()
	clocks(t)
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, recNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(src, parGrant(), &tplParent{files: files}, Options{Tape: tape, State: NewState()})
	if err := tape.Finish(); err != nil || out.Status != 0 {
		t.Fatalf("record %q: %v %+v", src, err, out)
	}
	return buf.Bytes()
}

// A value holding a secret's text keeps no hash, which could be guessed
// offline, whatever shape the text has: read from a file with its
// newline (the value lines made has none), holding a quote or a
// backslash (escaped in JSON), or under four runes (matched as a whole
// leaf). Each is caught when the same file is read again in the branch
// that bound it (its origin is hidden), when another file with the same
// text is sent and the secret is bound only after the par (the leaves,
// checked at the end), and when the secret is computed from the value
// (the origin alone). A plain value next to it keeps its hash.
func TestChanRecordSecretShapes(t *testing.T) {
	plain := "send c [ \"plain-value\" ]"
	for _, text := range []string{"hunter2-SECRET\n", "pw\"12345", "back\\slash9", "417", "417\n"} {
		for name, src := range map[string]string{
			"re-read": "par 2 chan c(1) { branch { declare -s tok = (cat /srv/ops/t); let l = (cat /srv/ops/t | lines); send c $l; " + plain + " }; for j in recv c { echo got } }",
			"later":   "par 2 chan c(1) { branch { let l = (cat /srv/ops/u | lines); send c $l; " + plain + " }; for j in recv c { echo got } }\ndeclare -s tok = (cat /srv/ops/t)",
			"origin":  "par 2 chan c(1) { branch { let l = (cat /srv/ops/u | lines); declare -s tok = [ upper($l[0].line) + \"-x\" ]; send c $l; " + plain + " }; for j in recv c { echo got } }",
		} {
			rec := recordFiles(t, src, map[string]string{"/srv/ops/t": text, "/srv/ops/u": text})
			lines := recLines(rec)
			var redacted, hashed int
			for _, i := range chanEvents(t, lines) {
				ev := chanEv(t, lines[i])
				switch {
				case ev.Op == "close":
				case ev.Redacted && ev.Hash == "":
					redacted++
				default:
					hashed++
				}
			}
			// The secret's send and recv are redacted; the plain value's
			// send and recv keep their hash.
			if redacted != 2 || hashed != 2 {
				t.Errorf("%q %s: %d redacted, %d hashed\n%s", text, name, redacted, hashed, rec)
			}
			if got, err := replay(t, rec, src); err != nil || got.Status != 0 {
				t.Errorf("%q %s: replay %v %+v", text, name, err, got)
			}
		}
	}
}

// A recording counts what it writes for a chan event, a hash, never the
// value: six values of 6 MB record. The tape holds the leaves of the
// first values for the check at Finish within its keep budget; past it a
// value keeps none and is recorded redacted, and the recording replays.
func TestChanRecordLargeValues(t *testing.T) {
	src := "let s = [ \"a\" * 6000000 ]\npar 2 chan c(1) { for i in [ range(1, 6) ] { send c $s }; for x in recv c { echo [ len($x) ] } }"
	rec, out := record(t, src)
	if out.Status != 0 || out.Stdout != strings.Repeat("6000000\n", 6) || len(rec) > 1<<16 {
		t.Fatalf("%d %q %q %d", out.Status, out.Stdout, out.Stderr, len(rec))
	}
	lines := recLines(rec)
	var got []string
	for _, i := range chanEvents(t, lines) {
		if ev := chanEv(t, lines[i]); ev.Op == "send" {
			got = append(got, strconv.FormatBool(ev.Redacted))
		}
	}
	if strings.Join(got, " ") != "false false true true true true" {
		t.Fatalf("redacted sends: %v", got)
	}
	if r, err := replay(t, rec, src); err != nil || r.Stdout != out.Stdout {
		t.Fatalf("replay: %v %+v", err, r)
	}
}

// A value whose provenance overflowed may have lost a hidden origin: it is
// recorded redacted when its branch hides any origin, and keeps its hash
// when the branch hides none.
func TestChanRecordOverflowRedacted(t *testing.T) {
	files := map[string]string{"/srv/ops/t": "hunter2-SECRET\n"}
	var b strings.Builder
	sum := ""
	for i := 0; i < 17; i++ {
		files[fmt.Sprintf("/srv/ops/f%d", i)] = fmt.Sprintf("f%d\n", i)
		fmt.Fprintf(&b, "let c%d = (cat /srv/ops/f%d | lines); ", i, i)
		sum += fmt.Sprintf(" + $c%d[0].line", i)
	}
	body := b.String() + "let y = [ \"\"" + sum + " ]; send c $y"
	for hides, src := range map[bool]string{
		true:  "par 2 chan c(1) { branch { declare -s tok = (cat /srv/ops/t); " + body + " }; for j in recv c { echo got } }",
		false: "par 2 chan c(1) { branch { " + body + " }; for j in recv c { echo got } }",
	} {
		lines := recLines(recordFiles(t, src, files))
		for _, i := range chanEvents(t, lines) {
			if ev := chanEv(t, lines[i]); ev.Op != "close" && ev.Redacted != hides {
				t.Errorf("hides %v: %s", hides, lines[i])
			}
		}
	}
}

// A secret sent stays a secret: the receiver prints ***, a sink refuses
// it, directly, through two channels, and nested in a list of maps, and
// the session knows its text after the join.
func TestChanSecretCrosses(t *testing.T) {
	for src, want := range map[string]string{
		"par 2 chan c(1) { branch { declare -s s = \"hunter2-long\"; send c $s }; for x in recv c { echo $x; unit status $x } }":                                                         "***\n",
		"par 3 chan a(1), b(1) { branch { declare -s s = \"hunter2-long\"; send a $s }; for x in recv a { send b $x }; for y in recv b { echo $y; unit status $y } }":                    "***\n",
		"par 2 chan c(1) { branch { declare -s s = \"hunter2-long\"; send c [ [ {\"k\": [ $s ]} ] ] }; for x in recv c { echo $x; unit status [ $x[0].k[0] ] } }":                        "***\n",
		"declare -s s = \"hunter2-long\"\nlet rs = [ [ {\"a\": 1, \"k\": $s}, {\"a\": 2, \"k\": \"x\"} ] ]\npar 2 chan c(1) { send c $rs; for x in recv c { echo $x; unit status $x } }": "***\n",
	} {
		st := NewState()
		f := &fake{}
		out := Run(src, allow(), f, Options{State: st})
		if out.Status != exitType || out.Stdout != want || !strings.Contains(out.Stderr, "a secret cannot leave sash") || strings.Contains(out.Stdout+out.Stderr, "hunter2") || len(f.calls) != 0 {
			t.Errorf("%q: %+v calls %v", src, out, f.calls)
		}
		if !st.secretText["hunter2-long"] {
			t.Errorf("%q: the session does not know the secret", src)
		}
	}
	// The receiver marks what it received: a response it reads after the
	// value crossed, holding the secret's text, is secret there, so its
	// origin hides its hash. Only the receiver's own marking can do this;
	// the sender's sink reaches the session only at the join.
	src := "par 2 chan c(1) { branch { declare -s s = \"content of /srv/x\"; send c $s }; for x in recv c { let r = (cat /srv/x); origin r |> select target hash } }"
	out := Run(src, recGrant(), &hostParent{}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "/srv/x") || !strings.Contains(out.Stdout, "***") {
		t.Fatalf("receiver did not mark: %+v", out)
	}
	// Spelled into a word, a secret would cross as plain text: refused,
	// as let refuses it.
	out = Run("declare -s s = \"hunter2-long\"\npar 2 chan c(1) { send c \"x$s\"; for x in recv c { echo $x } }", allow(), &fake{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "a secret in a word: build it inside [ ]") || strings.Contains(out.Stdout, "hunter2") {
		t.Fatalf("word: %+v", out)
	}
	// The wrapper is part of the datum at any depth.
	v := value{kind: valData, d: []any{&omap{keys: []string{"k"}, m: map[string]any{"k": secretVal{"hunter2-long"}}}}}
	cp, _, why := chanCopy(v)
	if why != "" || !isSecret(cp.d.([]any)[0].(*omap).m["k"]) {
		t.Fatalf("nested secret lost: %+v", cp)
	}
}

// An origin the sender hides (a response a secret came from) stays
// hidden in the receiver, which never bound that secret itself.
func TestChanSecretOriginFlagCrosses(t *testing.T) {
	src := `par 2 chan c(1) {
  branch { declare -s tok = (cat /srv/token); let l = (cat /srv/token | lines); send c $l }
  for j in recv c { origin j |> select target hash }
}`
	out := Run(src, recGrant(), &hostParent{}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "/srv/token") || !strings.Contains(out.Stdout, "***") {
		t.Fatalf("%+v", out)
	}
}

// A term prompt answer bound before the par still selects no subcommand
// after it crosses: as the value, inside a map, and through two channels.
func TestChanAnswerCrosses(t *testing.T) {
	for _, src := range []string{
		"let a = (term prompt \"q\")\npar 2 chan c(1) { send c $a; for j in recv c { unit $j nginx } }",
		"let a = (term prompt \"q\")\npar 2 chan c(1) { send c [ {\"op\": $a} ]; for j in recv c { unit $j.op nginx } }",
		"let a = (term prompt \"q\")\npar 3 chan c(1), d(1) { send c $a; for j in recv c { send d $j }; for k in recv d { unit $k nginx } }",
	} {
		p := &recParent{}
		out := Run(src, termGrant(), p, Options{Term: &fakeTerm{answers: []string{"restart"}}})
		if out.Status != exitType || !strings.Contains(out.Stderr, msgSubFromAnswer) || len(p.calls) != 0 {
			t.Errorf("%q: %+v calls %v", src, out, p.calls)
		}
	}
}

// Origin ids never cross. Sender and receiver each bind five origins
// after the fork, so the same id names a different read in each: the
// receiver's origin rows name the sender's reads, with the sender's
// lines, and the answer in the value still selects no subcommand.
func TestChanOriginIDCollision(t *testing.T) {
	src := `let a = (term prompt "q")
par 2 chan c(1) {
  branch { let s1 = (cat /srv/s1); let s2 = (cat /srv/s2); let s3 = (cat /srv/s3 | lines); let s4 = (cat /srv/s4); let s5 = (cat /srv/s5 | lines); send c [ $s3[0].line ]; send c [ $s5[0].line + $a ] }
  branch {
    let r1 = (cat /srv/r1); let r2 = (cat /srv/r2); let r3 = (cat /srv/r3); let r4 = (cat /srv/r4); let r5 = (cat /srv/r5)
    let n = 0
    for j in recv c { let n = [ $n + 1 ]; if [ $n == 1 ] { origin j |> select target line } else { unit $j nginx } }
  }
}`
	out := Run(src, termGrant(), &hostParent{}, Options{Term: &fakeTerm{answers: []string{"restart"}}})
	if !strings.Contains(out.Stdout, "/srv/s3") || strings.Contains(out.Stdout, "/srv/r") || !strings.Contains(out.Stderr, msgSubFromAnswer) || out.Status != exitType {
		t.Fatalf("%+v", out)
	}
}

// A provenance cut at 16 origins crosses as overflow: the answer it may
// hide still selects nothing on the receiving side.
func TestChanOverflowCrosses(t *testing.T) {
	var b strings.Builder
	sum := ""
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&b, "let c%d = (cat /srv/f%d)\n", i, i)
		sum += fmt.Sprintf("$c%d + ", i)
	}
	b.WriteString("let x = (term prompt \"q\")\nlet y = [ " + sum + "$x ]\n")
	b.WriteString("par 2 chan c(1) { send c $y; for j in recv c { origin j |> where origin == overflow |> select target; unit $j nginx } }")
	p := &recParent{}
	out := Run(b.String(), termGrant(), p, Options{Term: &fakeTerm{answers: []string{"restart"}}})
	if out.Status != exitType || !strings.Contains(out.Stdout, "truncated") || !strings.Contains(out.Stderr, msgSubFromAnswer) || len(p.calls) != 16 {
		t.Fatalf("%+v calls %d", out, len(p.calls))
	}
}

// Sends on one channel past loopmax halt.
func TestChanLoopMax(t *testing.T) {
	src := "set loopmax 5\npar 2 chan c(64) { branch { send c 1; send c 2; send c 3; send c 4; send c 5; send c 6 }; for x in recv c { echo $x } }"
	out := Run(src, allow(), &fake{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "loop max") {
		t.Fatalf("%+v", out)
	}
}

// A received value is a copy: changing it in the receiver leaves nothing
// shared (rows keep their kind).
func TestChanDeepCopy(t *testing.T) {
	v := value{kind: valData, d: &omap{keys: []string{"a"}, m: map[string]any{"a": []any{"x"}}}}
	cp, _, why := chanCopy(v)
	if why != "" {
		t.Fatal(why)
	}
	cp.d.(*omap).m["a"].([]any)[0] = "y"
	if v.d.(*omap).m["a"].([]any)[0] != "x" {
		t.Fatal("shared")
	}
	r := value{kind: valRows, cols: []string{"a"}, rows: []parent.Row{{"a": "1"}}}
	cr, _, _ := chanCopy(r)
	cr.rows[0]["a"] = "2"
	if r.rows[0]["a"] != "1" || cr.kind != valRows {
		t.Fatal("rows shared")
	}
}

// A recorded failure (a worker fails, the par is cancelled, the producer
// closes late), a recorded deadlock, and a receiver that breaks replay to
// the same outcome.
func TestChanRecordReplayFailures(t *testing.T) {
	for _, src := range []string{
		"par 2 chan c(1) { for i in [ range(1, 10) ] { send c $i }; for x in recv c { cat /srv/ops/$x; egrep zz <<< a } }",
		"par 2 chan a(1), b(1) { for x in recv a { send b $x }; for y in recv b { send a $y } }",
		"par 3 chan c(2) { branch { send c 1; send c 2 }; for x in recv c { cat /srv/ops/$x }; for x in recv c { break } }",
	} {
		rec, out := record(t, src)
		got, err := replay(t, rec, src)
		if err != nil || got.Status != out.Status || got.Stdout != out.Stdout || got.Stderr != out.Stderr {
			t.Errorf("%q: %v\nrecord %+v\nreplay %+v", src, err, out, got)
		}
	}
}
