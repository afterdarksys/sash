package lang

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

// atomicAgent is a fake agent holding host files, with hooks on a few
// test paths: /srv/ops/slow sleeps, /srv/ops/expire restores the open
// transaction as the deadline would, /srv/ops/tamper changes out/a as
// another writer would.
func atomicAgent(t *testing.T) *prototest.Agent {
	t.Helper()
	var a *prototest.Agent
	a = &prototest.Agent{Max: 4,
		Files: map[string]string{"/srv/ops/out/a": "old\n", "/srv/ops/x": "x\n", "/srv/ops/slow": "s\n", "/srv/ops/expire": "e\n", "/srv/ops/tamper": "t\n"},
		Before: func(v parent.Verb) {
			switch v.Path {
			case "/srv/ops/slow":
				time.Sleep(1200 * time.Millisecond)
			case "/srv/ops/expire":
				a.Expire()
			case "/srv/ops/tamper":
				a.SetFile("/srv/ops/out/a", "other\n")
			}
		},
		Handle: func(v parent.Verb) (parent.Result, error) { return parent.Result{Kind: parent.KindBytes}, nil },
	}
	return a
}

// runAtomic runs src against a fresh fake agent and returns what happened.
func runAtomicSrc(t *testing.T, a *prototest.Agent, src string, opt Options) Outcome {
	t.Helper()
	c, done := muxClient(t, a)
	defer done()
	return Run(src, parGrant(), c, opt)
}

func fileIs(t *testing.T, a *prototest.Agent, p, want string) {
	t.Helper()
	got, ok := a.File(p)
	if want == "" {
		if ok {
			t.Fatalf("%s exists: %q", p, got)
		}
		return
	}
	if !ok || got != want {
		t.Fatalf("%s is %q (exists %v), want %q", p, got, ok, want)
	}
}

func atomics(a *prototest.Agent) string { return strings.Join(a.Atomics(), " ") }

// The writes of a block that runs through stay: begin, then commit.
func TestAtomicCommit(t *testing.T) {
	a := atomicAgent(t)
	out := runAtomicSrc(t, a, "atomic { echo new > /srv/ops/out/a; echo b > /srv/ops/out/b; check \"ok\" [ true ] }\necho after", Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "after") || atomics(a) != "atomic.begin atomic.commit" {
		t.Fatalf("%+v %s", out, atomics(a))
	}
	fileIs(t, a, "/srv/ops/out/a", "new\n")
	fileIs(t, a, "/srv/ops/out/b", "b\n")
}

// A failed check (no set -e), a non-zero status, an uncaught error, and
// a timeout each abort: every file is back, and the rest of the block does
// not run.
func TestAtomicAbort(t *testing.T) {
	for name, body := range map[string]string{
		"failed check": `check "conf" [ false ]`,
		// The statement ends 0; the failed check inside still aborts.
		"nested failed check": `if [ true ] { check "conf" [ false ]; echo fine }`,
		"non-zero":            "cat /srv/ops/missing",
		"uncaught error":      "echo [ 1 + \"x\" ]",
		"denied":              "cat /etc/passwd",
	} {
		a := atomicAgent(t)
		out := runAtomicSrc(t, a, "atomic { echo new > /srv/ops/out/a; echo b > /srv/ops/out/b; "+body+"; echo c > /srv/ops/out/c }\necho after", Options{})
		if out.Status == 0 || !strings.Contains(out.Stderr, "atomic: rolled back") || atomics(a) != "atomic.begin atomic.abort" {
			t.Errorf("%s: %+v %s", name, out, atomics(a))
			continue
		}
		fileIs(t, a, "/srv/ops/out/a", "old\n")
		fileIs(t, a, "/srv/ops/out/b", "")
		fileIs(t, a, "/srv/ops/out/c", "")
	}
	// The block's deadline: the statement after a slow one is past it.
	a := atomicAgent(t)
	out := runAtomicSrc(t, a, "atomic deadline 1s { echo new > /srv/ops/out/a; cat /srv/ops/slow; echo c > /srv/ops/out/c }", Options{})
	if out.Status == 0 || !strings.Contains(out.Stderr, "timeout") || atomics(a) != "atomic.begin atomic.abort" {
		t.Fatalf("deadline: %+v %s", out, atomics(a))
	}
	fileIs(t, a, "/srv/ops/out/a", "old\n")
	fileIs(t, a, "/srv/ops/out/c", "")
	// A cap: verbmax reached inside still aborts (abort is not counted).
	a = atomicAgent(t)
	out = runAtomicSrc(t, a, "set verbmax 1\natomic { echo new > /srv/ops/out/a; echo b > /srv/ops/out/b }", Options{})
	if out.Status == 0 || atomics(a) != "atomic.begin atomic.abort" {
		t.Fatalf("cap: %+v %s", out, atomics(a))
	}
	fileIs(t, a, "/srv/ops/out/a", "old\n")
}

// Everything atomic cannot hold is refused before begin, through called
// fns too, with no agent call.
func TestAtomicStaticRefusal(t *testing.T) {
	for src, want := range map[string]string{
		"atomic { unit restart nginx }":                                        "unit.restart",
		"atomic { package install curl }":                                      "package.install",
		"atomic { sysctl set k 1 }":                                            "sysctl.set",
		"fn f() { unit restart nginx }\natomic { echo x > /srv/ops/out/a; f }": "unit.restart (in fn f)",
		"fn g() { f }\nfn f() { sysctl set k 1 }\natomic { g }":                "sysctl.set (in fn f) (in fn g)",
		"atomic { atomic { echo x > /srv/ops/out/a } }":                        "nested atomic",
		"atomic { par 2 { echo a; echo b } }":                                  "par",
		"atomic { use /srv/ops/lib.sash }":                                     "use",
		"atomic { retry 3 { unit restart nginx } }":                            "unit.restart",
		"atomic { if [ false ] { unit restart nginx } }":                       "unit.restart",
		"atomic { let x = (unit restart nginx) }":                              "unit.restart",
		"let op = restart\natomic { unit $op nginx }":                          "unit.restart",
	} {
		a := atomicAgent(t)
		out := runAtomicSrc(t, a, src, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "atomic_irreversible: "+want) || atomics(a) != "" {
			t.Errorf("%q: %+v %s", src, out, atomics(a))
		}
		fileIs(t, a, "/srv/ops/out/a", "old\n")
	}
	if out := Run("atomic { break }", parGrant(), &tplParent{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "break would leave an atomic block") {
		t.Errorf("break: %+v", out)
	}
	if out := Run("par 2 { atomic { echo x } }", parGrant(), &tplParent{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "atomic inside par") {
		t.Errorf("in par: %+v", out)
	}
}

// The agent restored on its own (deadline): the commit reports it, the
// block is rolled back, and try catches it with key atomic. A file another
// writer changed is left as found: inconsistent.
func TestAtomicRestoredAndInconsistent(t *testing.T) {
	a := atomicAgent(t)
	out := runAtomicSrc(t, a, "try { atomic { echo new > /srv/ops/out/a; cat /srv/ops/expire } } catch e { echo caught $e.key $e.status }", Options{})
	if out.Status != 0 || out.Stdout != "e\ncaught atomic 2\n" {
		t.Fatalf("restored: %+v", out)
	}
	fileIs(t, a, "/srv/ops/out/a", "old\n")
	a = atomicAgent(t)
	out = runAtomicSrc(t, a, "atomic { echo new > /srv/ops/out/a; cat /srv/ops/expire }", Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "refused atomic: atomic_restored (") {
		t.Fatalf("restored message: %+v", out)
	}
	a = atomicAgent(t)
	out = runAtomicSrc(t, a, "atomic { echo new > /srv/ops/out/a; echo b > /srv/ops/out/b; cat /srv/ops/tamper; check \"x\" [ false ] }", Options{})
	if out.Status == 0 || !strings.Contains(out.Stderr, "refused atomic: atomic_inconsistent (") {
		t.Fatalf("inconsistent: %+v", out)
	}
	fileIs(t, a, "/srv/ops/out/a", "other\n")
	fileIs(t, a, "/srv/ops/out/b", "")
	a = atomicAgent(t)
	out = runAtomicSrc(t, a, "try { atomic { echo new > /srv/ops/out/a; cat /srv/ops/tamper; check \"x\" [ false ] } } catch e { echo $e.key }", Options{})
	if out.Stdout != "t\nFAIL x\natomic\n" {
		t.Fatalf("inconsistent key: %+v", out)
	}
}

// An agent before atomic denies begin: a clear error, and the block never
// runs unprotected.
func TestAtomicOldAgent(t *testing.T) {
	a := atomicAgent(t)
	a.NoAtomic = true
	out := runAtomicSrc(t, a, "atomic { echo new > /srv/ops/out/a }", Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "atomic unavailable: agent too old, or no file write targets in the grant") || atomics(a) != "atomic.begin" {
		t.Fatalf("%+v %s", out, atomics(a))
	}
	fileIs(t, a, "/srv/ops/out/a", "old\n")
}

// Plan mode: no agent call for atomic, would-lines in order.
func TestAtomicPlan(t *testing.T) {
	a := atomicAgent(t)
	st := NewState()
	st.LockPlan()
	out := runAtomicSrc(t, a, "atomic { echo new > /srv/ops/out/a }", Options{State: st})
	if out.Status != 0 || atomics(a) != "" || !strings.Contains(out.Stderr, "plan: would begin atomic\nplan: would write /srv/ops/out/a (4 bytes)\nplan: would commit atomic\n") {
		t.Fatalf("%+v %s", out, atomics(a))
	}
	fileIs(t, a, "/srv/ops/out/a", "old\n")
}

// Confirm asks for the writes inside, never for begin, commit, or abort.
func TestAtomicConfirm(t *testing.T) {
	a := atomicAgent(t)
	var asked []string
	opt := Options{State: NewState(), Confirm: func(q string) (bool, bool) { asked = append(asked, q); return true, false }}
	out := runAtomicSrc(t, a, "set -o confirm\natomic { echo new > /srv/ops/out/a; echo b > /srv/ops/out/b }", opt)
	if out.Status != 0 || len(asked) != 2 || atomics(a) != "atomic.begin atomic.commit" {
		t.Fatalf("%+v %q %s", out, asked, atomics(a))
	}
}

// atomic.* events are recorded and replayed, rollback included.
func TestAtomicRecordReplay(t *testing.T) {
	for _, src := range []string{
		"atomic { echo new > /srv/ops/out/a }",
		"atomic { echo new > /srv/ops/out/a; check \"x\" [ false ] }",
	} {
		a := atomicAgent(t)
		c, done := muxClient(t, a)
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
		if !strings.Contains(buf.String(), `"key":"atomic.begin"`) {
			t.Fatalf("%q: not recorded:\n%s", src, buf.String())
		}
		rt, err := LoadReplay(bytes.NewReader(buf.Bytes()), src, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		cp := &countParent{}
		got := Run(src, parGrant(), cp, Options{Tape: rt})
		if err := rt.Finish(); err != nil || got.Status != out.Status || got.Stderr != out.Stderr || cp.n != 0 {
			t.Fatalf("%q: replay %v %+v vs %+v", src, err, got, out)
		}
	}
}

// needs, lint, and why walk atomic bodies.
func TestAtomicWalkers(t *testing.T) {
	if out := Needs("atomic { echo x > /srv/ops/out/a; cat /srv/ops/x }", NeedsOptions{}); !strings.Contains(out.Stdout, "/srv/ops/out/a") || !strings.Contains(out.Stdout, "/srv/ops/x") {
		t.Errorf("needs: %+v", out)
	}
	if rows := lintRows(t, Lint("atomic { echo x > /srv/ops/out/a; unit restart nginx }")); !hasRule(rows, "atomic-irreversible", "warning") {
		t.Errorf("lint: %+v", rows)
	}
	if rows := lintRows(t, Lint("atomic { echo x > /srv/ops/out/a }")); hasRule(rows, "atomic-irreversible", "") {
		t.Errorf("lint clean: %+v", rows)
	}
	if out := Why("atomic { cat /etc/passwd }", parGrant(), Options{}); !strings.Contains(out.Stdout, "outside_roots") {
		t.Errorf("why: %+v", out)
	}
}

// An exit reached inside a block (through a fn it calls) is refused there:
// the block rolls back and the run ends 2, never 0. So does a rollback a
// try caught, and a return can never leave a block.
func TestAtomicExitInside(t *testing.T) {
	for _, src := range []string{
		"fn f() { exit 0 }\natomic { echo new > /srv/ops/out/a; f }\necho after",
		"fn f() { exit 0 }\nfn w() { atomic { echo new > /srv/ops/out/a; f } }\nw\necho after",
		"fn f() { exit 0 }\ntry { atomic { echo new > /srv/ops/out/a; f } } catch e { echo caught }\nexit 0",
		"try { atomic { echo new > /srv/ops/out/a; check \"x\" [ false ] } } catch e { echo caught }",
	} {
		a := atomicAgent(t)
		out := runAtomicSrc(t, a, src, Options{})
		// try keeps the block's messages for its catch; the status is
		// still 2.
		caught := strings.HasPrefix(src, "try") || strings.Contains(src, "\ntry")
		if out.Status != exitType || (!caught && !strings.Contains(out.Stderr, "atomic: rolled back")) || (caught && !strings.Contains(out.Stdout, "caught")) || atomics(a) != "atomic.begin atomic.abort" {
			t.Errorf("%q: %+v %s", src, out, atomics(a))
		}
		if strings.Contains(src, "exit 0 }\natomic") && !strings.Contains(out.Stderr, "exit would leave an atomic block") {
			t.Errorf("%q: %+v", src, out)
		}
		fileIs(t, a, "/srv/ops/out/a", "old\n")
	}
	if out := Run("fn w() { atomic { echo new > /srv/ops/out/a; return 0 } }", parGrant(), &tplParent{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "return would leave an atomic block") {
		t.Errorf("return: %+v", out)
	}
}
