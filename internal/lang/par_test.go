package lang

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

func parGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"file.read", "file.write", "unit.restart"}, FileRead: []string{"/srv/ops"}, FileWrite: []string{"/srv/ops/out"}}
}

// muxClient serves a on one end of a pipe and returns the client end.
// done closes the pipe and waits for a, failing on a protocol violation.
func muxClient(t *testing.T, a *prototest.Agent) (*proto.Client, func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		a.Serve(c2)
	}()
	return &proto.Client{RW: c1}, func() {
		c1.Close()
		<-served
		c2.Close()
		if err := a.Stats().Err; err != nil {
			t.Errorf("agent saw a protocol violation: %v", err)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 5000; i++ {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// barrierCat answers cat <path> with the file's base name, after all n
// reads have arrived (so they ran at once), finishing in reverse order.
func barrierCat(n int) func(parent.Verb) (parent.Result, error) {
	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})
	return func(v parent.Verb) (parent.Result, error) {
		name := v.Path[strings.LastIndex(v.Path, "/")+1:]
		if n > 1 {
			mu.Lock()
			arrived++
			if arrived == n {
				close(all)
			}
			mu.Unlock()
			select {
			case <-all:
			case <-time.After(5 * time.Second):
				return parent.Result{Exit: 9}, nil
			}
			time.Sleep(time.Duration('d'-name[0]) * 40 * time.Millisecond)
		}
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte(name + "\n")}, nil
	}
}

const fourCats = "par 4 { cat /srv/ops/a; cat /srv/ops/b; cat /srv/ops/c; cat /srv/ops/d }"

// Four branches run at once against a mux agent, finish in reverse, and
// print in branch order. An old agent gives N = 1 and the same output.
func TestParConcurrentAndOrdered(t *testing.T) {
	a := &prototest.Agent{Max: 4, Handle: barrierCat(4)}
	c, done := muxClient(t, a)
	out := Run(fourCats, parGrant(), c, Options{})
	done()
	st := a.Stats()
	if out.Status != 0 || out.Stdout != "a\nb\nc\nd\n" || st.MaxRunning != 4 || st.Hellos != 1 {
		t.Fatalf("%+v %+v", out, st)
	}
	old := &prototest.Agent{Max: 0, Handle: barrierCat(1)}
	c, done = muxClient(t, old)
	out2 := Run(fourCats, parGrant(), c, Options{})
	done()
	if st := old.Stats(); out2 != out || st.MaxRunning != 1 || st.Hellos != 1 || st.Requests != 4 {
		t.Fatalf("old agent: %+v %+v", out2, st)
	}
	// A parent that is not a mux client runs one branch at a time.
	p := &tplParent{files: map[string]string{"/srv/ops/a": "a\n", "/srv/ops/b": "b\n", "/srv/ops/c": "c\n", "/srv/ops/d": "d\n"}}
	if out3 := Run(fourCats, parGrant(), p, Options{}); out3.Stdout != out.Stdout || out3.Status != 0 {
		t.Fatalf("fake parent: %+v", out3)
	}
}

// A sibling's failure cancels the queued request at the agent (it never
// runs) and the started mutation finishes and is reported; try catches
// the par's failure.
func TestParFailureCancelsSiblings(t *testing.T) {
	const src = "par 4 { unit restart nginx; cat /srv/ops/fail; unit restart queued }"
	run := func(src string) (Outcome, prototest.Stats) {
		release := make(chan struct{})
		never := make(chan struct{})
		var a *prototest.Agent
		a = &prototest.Agent{Max: 4,
			Hold: func(v parent.Verb) <-chan struct{} {
				if v.Key == "unit.restart" && v.Argv[2] == "queued" {
					return never
				}
				return nil
			},
			Handle: func(v parent.Verb) (parent.Result, error) {
				switch {
				case v.Key == "unit.restart":
					<-release
					return parent.Result{Kind: parent.KindBytes}, nil
				case v.Path == "/srv/ops/fail":
					// Fail once the restart runs and the other is
					// queued.
					for i := 0; i < 5000; i++ {
						if s := a.Stats(); s.Running == 2 && s.Open == 3 {
							break
						}
						time.Sleep(time.Millisecond)
					}
					return parent.Result{Exit: 1}, nil
				}
				return parent.Result{}, nil
			}}
		c, done := muxClient(t, a)
		defer done()
		go func() {
			waitFor(t, "two cancels", func() bool { return len(a.Stats().Cancels) >= 2 })
			close(release)
		}()
		out := Run(src, parGrant(), c, Options{})
		return out, a.Stats()
	}
	out, st := run("try { " + src + " } catch e { echo caught $e.status }")
	for _, r := range st.Ran {
		if r == "unit.restart queued" {
			t.Fatalf("the queued restart ran: %+v", st)
		}
	}
	if out.Status != 0 || out.Stdout != "caught 1\n" {
		t.Fatalf("%+v %+v", out, st)
	}
	out, st = run(src)
	if out.Status != 1 || !strings.Contains(out.Stderr, "branch 1: unit.restart nginx finished after cancel (exit 0)") ||
		!strings.Contains(out.Stderr, "par: branch 2 failed (exit 1)") || len(st.Ran) != 2 {
		t.Fatalf("%+v %+v", out, st)
	}
}

// One interrupt cancels and waits; a second stops waiting and names the
// mutating verb still outstanding.
func TestParInterrupts(t *testing.T) {
	for _, n := range []int{1, 2} {
		release := make(chan struct{})
		var a *prototest.Agent
		a = &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
			if v.Key == "unit.restart" {
				<-release
			}
			return parent.Result{Kind: parent.KindBytes}, nil
		}}
		c, done := muxClient(t, a)
		intr := make(chan struct{}, 2)
		opt := Options{Interrupts: func() (<-chan struct{}, func()) { return intr, func() {} }}
		go func() {
			waitFor(t, "restart running", func() bool { return a.Stats().Running == 1 })
			intr <- struct{}{}
			waitFor(t, "cancel", func() bool { return len(a.Stats().Cancels) == 1 })
			if n == 2 {
				intr <- struct{}{}
			} else {
				close(release)
			}
		}()
		out := Run("par 2 { unit restart nginx; echo b }\necho after", parGrant(), c, opt)
		if n == 2 {
			close(release)
		}
		done()
		if out.Status != exitInterrupted || strings.Contains(out.Stdout, "after") || !strings.Contains(out.Stderr, "par: interrupted") {
			t.Fatalf("%d interrupts: %+v", n, out)
		}
		if n == 2 && !strings.Contains(out.Stderr, "outcome unknown: unit.restart nginx") {
			t.Fatalf("second interrupt: %+v", out)
		}
		if n == 1 && !strings.Contains(out.Stderr, "branch 1: unit.restart nginx finished after cancel (exit 0)") {
			t.Fatalf("first interrupt: %+v", out)
		}
	}
}

// verbmax is one count across every branch: 16 branches of 2 reads each
// under verbmax 10 make at most 10 requests.
func TestParVerbMaxShared(t *testing.T) {
	a := &prototest.Agent{Max: 4, Handle: barrierCat(1)}
	c, done := muxClient(t, a)
	defer done()
	items := make([]string, 16)
	for i := range items {
		items[i] = fmt.Sprint(i)
	}
	src := "set verbmax 10\npar 16 for i in [ [" + strings.Join(items, ",") + "] ] { cat /srv/ops/a; cat /srv/ops/b }"
	out := Run(src, parGrant(), c, Options{})
	// Never past 10. The branch that hits verbmax fails and cancels the
	// rest, so a verb already counted may stop before it is sent.
	if st := a.Stats(); st.Requests > 10 || st.Requests < 4 || out.Status == 0 || !strings.Contains(out.Stderr, "verb max") {
		t.Fatalf("requests %d: %+v", st.Requests, out)
	}
}

// Recorded at full concurrency (completions in reverse), replayed one
// branch at a time from each branch's stream: no divergence, same output.
func TestParRecordReplay(t *testing.T) {
	src := "par 3 { cat /srv/ops/a; cat /srv/ops/b; cat /srv/ops/c }\ncat /srv/ops/d"
	three := barrierCat(3)
	a := &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		if v.Path == "/srv/ops/d" {
			return barrierCat(1)(v)
		}
		return three(v)
	}}
	c, done := muxClient(t, a)
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(src, parGrant(), c, Options{Tape: tape})
	done()
	if err := tape.Finish(); err != nil || out.Status != 0 || out.Stdout != "a\nb\nc\nd\n" {
		t.Fatalf("record: %v %+v", err, out)
	}
	lines := strings.Split(buf.String(), "\n")
	if !strings.Contains(lines[1], `"branch":"1.3"`) {
		t.Fatalf("completion order not recorded first: %s", lines[1])
	}
	rt, err := LoadReplay(bytes.NewReader(buf.Bytes()), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cp := &countParent{}
	got := Run(src, parGrant(), cp, Options{Tape: rt})
	if err := rt.Finish(); err != nil || got.Stdout != out.Stdout || got.Status != 0 || cp.n != 0 {
		t.Fatalf("replay: %v %+v (parent calls %d)", err, got, cp.n)
	}
	// A recording missing one branch's event diverges at Finish or on use.
	var cut []string
	for _, l := range lines {
		if !strings.Contains(l, `"branch":"1.2"`) {
			cut = append(cut, l)
		}
	}
	if _, err := LoadReplay(strings.NewReader(strings.Join(cut, "\n")), src, time.Now()); err == nil {
		t.Fatal("a recording with an event removed loaded")
	}
}

// Under confirm, par runs one branch at a time, asks in source order, and
// sends no hello.
func TestParConfirmForcesOne(t *testing.T) {
	a := &prototest.Agent{Max: 4, Handle: func(parent.Verb) (parent.Result, error) { return parent.Result{Kind: parent.KindBytes}, nil }}
	c, done := muxClient(t, a)
	var asked []string
	opt := Options{State: NewState(), Confirm: func(p string) (bool, bool) { asked = append(asked, p); return true, false },
		Interrupts: func() (<-chan struct{}, func()) { t.Error("interrupts asked under confirm"); return nil, func() {} }}
	out := Run("set -o confirm\npar 4 { unit restart a; unit restart b; unit restart c }", parGrant(), c, opt)
	done()
	st := a.Stats()
	if out.Status != 0 || st.Hellos != 0 || st.MaxRunning != 1 || len(asked) != 3 ||
		!strings.Contains(asked[0], " a?") || !strings.Contains(asked[1], " b?") || !strings.Contains(asked[2], " c?") {
		t.Fatalf("%+v %+v %q", out, st, asked)
	}
}

// Plan mode holds in every branch: nothing mutating reaches the agent.
func TestParPlan(t *testing.T) {
	a := &prototest.Agent{Max: 4, Handle: func(parent.Verb) (parent.Result, error) { return parent.Result{Kind: parent.KindBytes}, nil }}
	c, done := muxClient(t, a)
	st := NewState()
	st.LockPlan()
	out := Run("par 2 { unit restart a; unit restart b }", parGrant(), c, Options{State: st})
	done()
	if s := a.Stats(); out.Status != 0 || s.Requests != 0 || !strings.Contains(out.Stderr, "plan: would unit restart a\nplan: would unit restart b\n") {
		t.Fatalf("%+v %+v", out, s)
	}
}

// A secret bound in one branch never reaches another's output, the
// session after the join, or the recording.
func TestParSecretStaysInBranch(t *testing.T) {
	a := &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("hunter2-long")}, nil
	}}
	c, done := muxClient(t, a)
	// Each statement of a par block is a branch; a fn makes one branch of
	// several statements.
	src := "fn b1() { declare -s s = (cat /srv/ops/tok); echo got $s }\npar 2 { b1; cat /srv/ops/other }\necho top"
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(src, parGrant(), c, Options{Tape: tape})
	done()
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.Stdout+out.Stderr+buf.String(), "hunter2") != 1 || !strings.Contains(out.Stdout, "got ***\nhunter2-long") || out.Status != 0 {
		t.Fatalf("%+v\n%s", out, buf.String())
	}
}

// Rows, par for items, and the parse and usage refusals.
func TestParShape(t *testing.T) {
	p := &tplParent{files: map[string]string{"/srv/ops/a": "a\n"}}
	out := Run("par 2 as r for x in [ [\"p\",\"q\"] ] { echo $x }\necho [ len($r) ] [ $r[1].branch ] [ $r[1].item ] [ $r[1].status ] [ $r[0].message ]", parGrant(), p, Options{})
	if out.Status != 0 || out.Stdout != "p\nq\n2 2 q 0 \n" {
		t.Fatalf("%+v", out)
	}
	for src, want := range map[string]string{
		"par 2 { break }":                          "parse: break would leave a par branch",
		"par 2 { for x in [ [1] ] { break } }":     "",
		"par 2 { return }":                         "parse: return would leave a par branch",
		"par 2 { exit 1 }":                         "parse: exit would leave a par branch",
		"par 2 { if [ true ] { continue } }":       "parse: continue would leave a par branch",
		"par 2 for x in [ [1] ] { break }":         "parse: break would leave a par branch",
		"par 2 { par 2 { echo x } }":               "parse: par inside par",
		"par 2 { fn f() { par 2 { echo } } }":      "parse: par inside par",
		"fn f() { par 2 { echo x } }\npar 2 { f }": "type par: par inside par",
		"fn g() { exit 3 }\npar 2 { g }":           "exit would leave a par branch",
		"par 0 { echo x }":                         "usage par: N is 1 to 16",
		"par 17 { echo x }":                        "usage par: N is 1 to 16",
		"par x { echo x }":                         "usage par: N is 1 to 16",
		"par 2 { atomic { echo x } }":              "parse: atomic inside par",
	} {
		out := Run(src, parGrant(), p, Options{})
		if want == "" {
			if out.Status != 0 {
				t.Errorf("%q: %+v", src, out)
			}
			continue
		}
		if out.Status == 0 || !strings.Contains(out.Stderr, want) {
			t.Errorf("%q: %+v", src, out)
		}
	}
}

// Lint walks par bodies and warns when two branches name one target.
func TestLintPar(t *testing.T) {
	for src, want := range map[string]bool{
		"par 2 { unit restart nginx; unit restart nginx }":           true,
		"par 2 { echo a > /srv/ops/out/x; echo b > /srv/ops/out/x }": true,
		"par 2 for u in [ [1,2] ] { unit restart nginx }":            true,
		"par 2 { unit restart nginx; unit restart redis }":           false,
		"par 2 for u in [ [\"a\",\"b\"] ] { unit restart $u }":       false,
	} {
		if got := hasRule(lintRows(t, Lint(src)), "par-same-target", "warning"); got != want {
			t.Errorf("%q: %v", src, got)
		}
	}
	if rows := lintRows(t, Lint("declare -s s = x\npar 2 { echo $s; echo b }")); !hasRule(rows, "secret-to-sink", "error") {
		t.Errorf("secret in a branch: %+v", rows)
	}
	if out := Needs("par 2 { unit restart nginx; cat /srv/ops/a }", NeedsOptions{}); !strings.Contains(out.Stdout, "unit.restart") || !strings.Contains(out.Stdout, "/srv/ops/a") {
		t.Errorf("needs: %+v", out)
	}
}

// Two interrupts: a branch that already ended (it bound a secret, and its
// restart finished after the first cancel) is reported and its secret
// kept; a branch still running reports when it ends, through the next
// run, and its secret is redacted from then on.
func TestParSecondInterruptReports(t *testing.T) {
	fast, slow := make(chan struct{}), make(chan struct{})
	var a *prototest.Agent
	a = &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		switch {
		case v.Key == "unit.restart" && v.Argv[2] == "fast":
			<-fast
		case v.Key == "unit.restart" && v.Argv[2] == "slow":
			<-slow
		case v.Path == "/srv/ops/tok":
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte("hunter2-long")}, nil
		case v.Path == "/srv/ops/tok2":
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte("swordfish-9")}, nil
		case v.Path == "/srv/ops/echo":
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte("hunter2-long swordfish-9")}, nil
		}
		return parent.Result{Kind: parent.KindBytes}, nil
	}}
	c, done := muxClient(t, a)
	defer done()
	var once sync.Once
	releaseSlow := func() { once.Do(func() { close(slow) }) }
	// Registered after done, so it runs first: a failed check never
	// leaves the slow handler holding the agent open.
	defer releaseSlow()
	st := NewState()
	intr := make(chan struct{}, 2)
	opt := Options{State: st, Interrupts: func() (<-chan struct{}, func()) { return intr, func() {} }}
	go func() {
		waitFor(t, "both restarts running", func() bool { return a.Stats().Running == 2 })
		intr <- struct{}{}
		waitFor(t, "two cancels", func() bool { return len(a.Stats().Cancels) == 2 })
		close(fast)
		waitFor(t, "fast done", func() bool { return a.Stats().Running == 1 })
		// The fast branch ends at its next boundary, microseconds later.
		time.Sleep(100 * time.Millisecond)
		intr <- struct{}{}
	}()
	src := "fn a1() { declare -s s = (cat /srv/ops/tok); unit restart fast }\n" +
		"fn b1() { declare -s t = (cat /srv/ops/tok2); unit restart slow }\n" +
		"par 2 { a1; b1 }"
	out := Run(src, parGrant(), c, opt)
	if out.Status != exitInterrupted || !strings.Contains(out.Stderr, "branch 1: unit.restart fast finished after cancel (exit 0)") ||
		!strings.Contains(out.Stderr, "outcome unknown: unit.restart slow") || strings.Contains(out.Stderr, "late:") {
		t.Fatalf("%+v", out)
	}
	if !st.secretText["hunter2-long"] {
		t.Fatal("the finished branch's secret was not kept")
	}
	releaseSlow()
	waitFor(t, "slow done", func() bool { return a.Stats().Running == 0 })
	// The abandoned branch ends at its next boundary; the next run
	// reports it before its first statement.
	var late Outcome
	for i := 0; i < 200 && !strings.Contains(late.Stderr, "late:"); i++ {
		time.Sleep(5 * time.Millisecond)
		late = Run("echo x", parGrant(), c, opt)
	}
	if !strings.Contains(late.Stderr, "late: branch 2: unit.restart slow finished after cancel (exit 0)") || !st.secretText["swordfish-9"] {
		t.Fatalf("late: %+v", late)
	}
	// Both secrets are redacted in a later recording.
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, "cat /srv/ops/echo", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	opt.Tape = tape
	Run("cat /srv/ops/echo", parGrant(), c, opt)
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), "swordfish") || !strings.Contains(buf.String(), `"redacted":true`) {
		t.Fatalf("recording: %s", buf.String())
	}
}

// A secret a branch bound before a second interrupt abandoned it is known
// to the session at once: the next run's template_file render of that
// text is written 0600, and its --check diff is not listed, while the
// branch is still running.
func TestParAbandonedSecretKnownAtOnce(t *testing.T) {
	slow := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(slow) }) }
	var mu sync.Mutex
	var writes []parent.Verb
	files := map[string]string{
		"/srv/ops/tok":          "swordfish-9",
		"/srv/ops/spec.yaml":    "pw: swordfish-9\n",
		"/srv/ops/t.tpl":        "password={{ $pw }}\n",
		"/srv/ops/out/app.conf": "password=old\n",
	}
	var a *prototest.Agent
	a = &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		switch {
		case v.Key == "unit.restart":
			<-slow
		case v.Key == "file.write":
			mu.Lock()
			writes = append(writes, v)
			mu.Unlock()
		case v.Key == "file.read":
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte(files[v.Path])}, nil
		}
		return parent.Result{Kind: parent.KindBytes}, nil
	}}
	c, done := muxClient(t, a)
	defer done()
	defer release()
	st := NewState()
	intr := make(chan struct{}, 2)
	opt := Options{State: st, Interrupts: func() (<-chan struct{}, func()) { return intr, func() {} }}
	go func() {
		waitFor(t, "restart running", func() bool { return a.Stats().Running == 1 })
		intr <- struct{}{}
		waitFor(t, "cancel", func() bool { return len(a.Stats().Cancels) == 1 })
		intr <- struct{}{}
	}()
	out := Run("fn b1() { declare -s s = (cat /srv/ops/tok); unit restart slow }\npar 2 { b1; echo a }", parGrant(), c, opt)
	if out.Status != exitInterrupted || !strings.Contains(out.Stderr, "outcome unknown: unit.restart slow") {
		t.Fatalf("%+v", out)
	}
	// The branch is still blocked in its restart.
	if a.Stats().Running != 1 {
		t.Fatal("the branch ended before the check")
	}
	chk := Run("template_file /srv/ops/spec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf --check", parGrant(), c, opt)
	if chk.Status != 1 || chk.Stdout != secretDiff || strings.Contains(chk.Stdout+chk.Stderr, "swordfish") {
		t.Fatalf("--check: %+v", chk)
	}
	w := Run("template_file /srv/ops/spec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", parGrant(), c, opt)
	mu.Lock()
	defer mu.Unlock()
	if w.Status != 0 || len(writes) != 1 || writes[0].Mode != "0600" {
		t.Fatalf("write: %+v %+v", w, writes)
	}
}
