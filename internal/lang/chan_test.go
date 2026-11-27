package lang

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
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
		"fn send() { echo a }":                                                     "",
		"fn chan() { echo a }":                                                     "",
		"fn recv() { echo a }":                                                     "",
	} {
		f := &fake{}
		out := Run(src, allow(), f, Options{})
		if out.Status != exitType || (want != "" && !strings.Contains(out.Stderr, want)) || len(f.calls) != 0 {
			t.Errorf("%q: status %d err %q", src, out.Status, out.Stderr)
		}
	}
	// Legal: retry inside the loop body, per item; try around an op.
	for _, src := range []string{
		"par 2 chan c(1) { send c 1; for j in recv c { retry 2 { echo $j } } }",
		"par 2 chan c(1) { try { send c 1 } catch { echo no }; try { for j in recv c { echo $j } } catch { echo no } }",
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
		"let s = (cat /srv/ops/g | lines)\npar 2 chan c(1) { send c $s; for x in recv c { echo $x } }":                                           "send c: value over 8 MiB",
		"let s = [ repeat(\"a\", 3145728) ]\npar 2 chan c(64) { for i in [ range(1, 20) ] { send c $s }; for x in recv c { echo [ len($x) ] } }": "send c: buffers over 32 MiB",
	} {
		p := &tplParent{files: map[string]string{"/srv/ops/f": big, "/srv/ops/g": strings.Repeat(strings.Repeat("a", 1000)+"\n", 9000)}}
		out := Run(src, g, p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, want) {
			t.Errorf("%q: %d %q", src[:30], out.Status, firstLine(out.Stderr))
		}
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
	// Spelled into a word, a secret would cross as plain text: refused,
	// as let refuses it.
	out := Run("declare -s s = \"hunter2-long\"\npar 2 chan c(1) { send c \"x$s\"; for x in recv c { echo $x } }", allow(), &fake{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "a secret in a word: build it inside [ ]") || strings.Contains(out.Stdout, "hunter2") {
		t.Fatalf("word: %+v", out)
	}
	// The wrapper is part of the datum at any depth.
	v := value{kind: valData, d: []any{&omap{keys: []string{"k"}, m: map[string]any{"k": secretVal{"hunter2-long"}}}}}
	cp, _, ok := chanCopy(v)
	if !ok || !isSecret(cp.d.([]any)[0].(*omap).m["k"]) {
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
  branch { let s1 = (cat /srv/s1); let s2 = (cat /srv/s2); let s3 = (cat /srv/s3); let s4 = (cat /srv/s4); let s5 = (cat /srv/s5); send c [ $s3 ]; send c [ $s5 + $a ] }
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
	cp, _, ok := chanCopy(v)
	if !ok {
		t.Fatal("copy")
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
