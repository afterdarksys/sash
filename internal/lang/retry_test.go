package lang

import (
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// flakyParent fails unit.restart until the nth call. Each call advances
// the fake clock by step.
type flakyParent struct {
	okAt, n int
	clock   *time.Time
	step    time.Duration
}

func (p *flakyParent) Do(v parent.Verb) (parent.Result, error) {
	p.n++
	if p.clock != nil {
		*p.clock = p.clock.Add(p.step)
	}
	if p.n >= p.okAt {
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
	}
	return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
}

func fakeClock(t *testing.T) (*time.Time, *[]time.Duration) {
	t.Helper()
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	var slept []time.Duration
	pn, ps := nowFn, sleepFn
	nowFn = func() time.Time { return now }
	sleepFn = func(d time.Duration) { slept = append(slept, d); now = now.Add(d) }
	t.Cleanup(func() { nowFn, sleepFn = pn, ps })
	return &now, &slept
}

func restartGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"unit.restart", "ps"}}
}

func TestRetry(t *testing.T) {
	_, slept := fakeClock(t)
	p := &flakyParent{okAt: 3}
	out := Run("retry 5 delay 2s { unit restart nginx }", restartGrant(), p, Options{})
	if out.Status != 0 || p.n != 3 || len(*slept) != 2 || (*slept)[0] != 2*time.Second {
		t.Fatalf("%+v calls %d slept %v", out, p.n, *slept)
	}
	p = &flakyParent{okAt: 99}
	out = Run("retry 3 { unit restart nginx }\necho after", restartGrant(), p, Options{})
	if out.Status != 0 || p.n != 3 || out.Stdout != "after\n" {
		t.Fatalf("gives up: %+v calls %d", out, p.n)
	}
	out = Run("retry 3 { unit restart nginx }", grant.Grant{V: "sash/1"}, &flakyParent{}, Options{})
	if out.Status != exitDenied || strings.Count(out.Stderr, "denied") != 1 {
		t.Fatalf("a denial retries and keeps one message: %+v", out)
	}
	p = &flakyParent{okAt: 99}
	out = Run("for i in [ [1, 2] ] { retry 5 { break } }\necho x", restartGrant(), p, Options{})
	if out.Stdout != "x\n" {
		t.Fatalf("break passes through: %+v", out)
	}
	for src, msg := range map[string]string{
		"retry 0 { echo }":           "usage retry",
		"retry 101 { echo }":         "usage retry",
		"retry x { echo }":           "usage retry",
		"retry 2 delay 2h { echo }":  "usage duration",
		"retry 2 delay -1s { echo }": "usage duration",
		"timeout 0s { echo }":        "usage duration",
		"timeout 25h { echo }":       "usage duration",
	} {
		if out := Run(src, restartGrant(), &flakyParent{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v", src, out)
		}
	}
}

func TestTimeout(t *testing.T) {
	now, _ := fakeClock(t)
	p := &flakyParent{okAt: 1, clock: now, step: 25 * time.Second}
	out := Run("timeout 1m { unit restart a; unit restart b; unit restart c; unit restart d }\necho after", restartGrant(), p, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "timeout") || p.n != 3 || out.Stdout != "ok\nok\nok\n" {
		t.Fatalf("%+v calls %d", out, p.n)
	}
	p = &flakyParent{okAt: 1, clock: now, step: 20 * time.Second}
	out = Run("try { timeout 30s { unit restart a; unit restart b; unit restart c } } catch e { echo caught $e.message }", restartGrant(), p, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "caught timeout") || p.n != 2 {
		t.Fatalf("catchable: %+v calls %d", out, p.n)
	}
	// The inner, shorter deadline wins; the outer one comes back after.
	p = &flakyParent{okAt: 1, clock: now, step: 20 * time.Second}
	out = Run("timeout 10m { try { timeout 30s { unit restart a; unit restart b; unit restart c } }; unit restart d }", restartGrant(), p, Options{})
	if out.Status != 0 || p.n != 3 {
		t.Fatalf("nested: %+v calls %d", out, p.n)
	}
	// Retry stops early when the deadline passes during its waits.
	p = &flakyParent{okAt: 99, clock: now, step: time.Second}
	out = Run("timeout 10s { retry 50 delay 3s { unit restart a } }", restartGrant(), p, Options{})
	if p.n > 4 {
		t.Fatalf("retry past the deadline: calls %d %+v", p.n, out)
	}
}
