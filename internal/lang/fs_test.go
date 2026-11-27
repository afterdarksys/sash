package lang

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// writes are the file.write calls a host saw.
func writes(h *skillHost) []string {
	var out []string
	for _, c := range h.calls {
		if strings.HasPrefix(c, "file.write ") {
			out = append(out, c)
		}
	}
	return out
}

// The sink sends today's file.write frame, unchanged: key, argv > path,
// path, and the body, with a mode only when one was asked for.
func TestFsWriteAtomicSendsTheSameFrameAsRedirect(t *testing.T) {
	h := &skillHost{}
	out := Run("cat /srv/app/app.conf | fs write-atomic /srv/app/new.conf\n", skillGrant(nil), h, Options{})
	want := `file.write argv=> /srv/app/new.conf path=/srv/app/new.conf mode= body="listen 8080\n"`
	if out.Status != 0 || out.Stdout != "" || out.Stderr != "" || len(writes(h)) != 1 || writes(h)[0] != want {
		t.Errorf("%+v\ncalls %q", out, h.calls)
	}
	// The same pipeline with > sends the same call.
	r := &skillHost{}
	Run("cat /srv/app/app.conf > /srv/app/new.conf\n", skillGrant(nil), r, Options{})
	if len(writes(r)) != 1 || writes(r)[0] != want {
		t.Errorf("> sends %q", writes(r))
	}
	// Rows render as > renders them.
	h, r = &skillHost{}, &skillHost{}
	Run("ps | fs write-atomic /srv/app/ps.txt\n", skillGrant(nil), h, Options{})
	Run("ps > /srv/app/ps.txt\n", skillGrant(nil), r, Options{})
	if len(writes(h)) != 1 || len(writes(r)) != 1 || writes(h)[0] != writes(r)[0] || !strings.Contains(writes(h)[0], `root  1  init\n`) {
		t.Errorf("rows: sink %q, > %q", writes(h), writes(r))
	}
	// Each mode the agent sets rides in the frame.
	for _, mode := range []string{"0600", "0640", "0644"} {
		h = &skillHost{}
		out = Run("echo x | fs write-atomic /srv/app/m --mode "+mode+"\n", skillGrant(nil), h, Options{})
		if out.Status != 0 || len(writes(h)) != 1 || writes(h)[0] != `file.write argv=> /srv/app/m path=/srv/app/m mode=`+mode+` body="x\n"` {
			t.Errorf("mode %s: %+v %q", mode, out, writes(h))
		}
	}
	// Input may be a here-string or a < read; a relative path uses the prefix.
	h = &skillHost{}
	out = Run("fs write-atomic /srv/app/h <<< text\nfs write-atomic /srv/app/copy < /srv/app/app.conf\ncd /srv/app\necho r | fs write-atomic rel.txt\n", skillGrant(nil), h, Options{})
	if out.Status != 0 || len(writes(h)) != 3 || !strings.Contains(writes(h)[0], `body="text\n"`) || !strings.Contains(writes(h)[1], `path=/srv/app/copy mode= body="listen 8080\n"`) || !strings.Contains(writes(h)[2], "path=/srv/app/rel.txt") {
		t.Errorf("inputs: %+v %q", out, writes(h))
	}
	// As a value pipe, the value is what was written.
	h = &skillHost{}
	out = Run("let v = (echo kept | fs write-atomic /srv/app/v)\necho got $v\n", skillGrant(nil), h, Options{})
	if out.Status != 0 || !strings.HasPrefix(out.Stdout, "got kept") || len(writes(h)) != 1 {
		t.Errorf("value: %+v", out)
	}
}

// What the sink adds over >: when an earlier stage failed nothing is
// sent, stderr says so, and the status is the failing stage's.
func TestFsWriteAtomicWritesNothingAfterAFailedStage(t *testing.T) {
	for _, c := range []struct {
		src    string
		status int
	}{
		{"cat /srv/app/app.conf | egrep zzz | fs write-atomic /srv/app/out\n", 1},
		{"sysctl get fail.three | fs write-atomic /srv/app/out\n", 3},
		{"cat /srv/app/app.conf | egrep zzz | head -n 1 | fs write-atomic /srv/app/out\n", 1},
		{"cat /srv/app/missing | fs write-atomic /srv/app/out\n", 1},
		{"fs write-atomic /srv/app/out < /srv/app/missing\n", 1},
		{"echo '\"x\"' | from json |> fs write-atomic /srv/app/out\n", exitType},
		{"set -e\ncat /srv/app/app.conf | egrep zzz | fs write-atomic /srv/app/out\n", 1},
		{"cat /srv/app/app.conf | from json | fs write-atomic /srv/app/out\n", exitType},
	} {
		h := &skillHost{}
		out := Run(c.src, skillGrant(nil), h, Options{})
		if out.Status != c.status || len(writes(h)) != 0 || !strings.Contains(out.Stderr, "fs write-atomic: not written") {
			t.Errorf("%q: status %d, %d writes, stderr %q", c.src, out.Status, len(writes(h)), out.Stderr)
		}
		// The same pipeline with > still replaces the target, where it runs
		// to the end: that is the difference.
		if c.status == 1 && strings.Contains(c.src, "egrep zzz |") && !strings.Contains(c.src, "head") {
			r := &skillHost{}
			Run(strings.Replace(c.src, "| fs write-atomic", ">", 1), skillGrant(nil), r, Options{})
			if len(writes(r)) != 1 {
				t.Errorf("%q: > wrote %d times", c.src, len(writes(r)))
			}
		}
	}
	// A stage that succeeds with nothing to say writes an empty file: that
	// is not a failure.
	h := &skillHost{}
	if out := Run("echo a | head -n 0 | fs write-atomic /srv/app/out\n", skillGrant(nil), h, Options{}); out.Status != 0 || len(writes(h)) != 1 {
		t.Errorf("an empty success: %+v %q", out, writes(h))
	}
}

// Everything refused, before any call: a wrong mode, a mode or a word
// from a value, a wrong place, no input, a secret, a path outside the
// roots, a missing key.
func TestFsWriteAtomicRefusals(t *testing.T) {
	for src, want := range map[string]string{
		"echo x | fs write-atomic /srv/app/o --mode 0666\n":             "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o --mode 0777\n":             "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o --mode 600\n":              "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o --mode\n":                  "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o --chmod 0600\n":            "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o 0600\n":                    "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o --mode 0600 extra\n":       "usage fs write-atomic",
		"let m = 0644\necho x | fs write-atomic /srv/app/o --mode $m\n": "usage fs write-atomic",
		"let f = --mode\necho x | fs write-atomic /srv/app/o $f 0644\n": msgCommandFromValue,
		"let op = write-atomic\necho x | fs $op /srv/app/o\n":           msgCommandFromValue,
		"echo x | fs\n":                                                                   "usage fs write-atomic",
		"echo x | fs write-atomic\n":                                                      "usage fs write-atomic",
		"echo x | fs write /srv/app/o\n":                                                  "usage fs write-atomic",
		"echo x | fs tempfile /srv/app\n":                                                 "usage fs write-atomic",
		"echo x | fs rmtemp /srv/app/o\n":                                                 "usage fs write-atomic",
		"echo x | fs write-atomic \"\"\n":                                                 "usage",
		"echo x | fs write-atomic /srv/app/o | cat\n":                                     "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o | fs write-atomic /srv/app/p\n":              "usage fs write-atomic",
		"fs write-atomic /srv/app/o\n":                                                    "usage fs write-atomic",
		"echo x | fs write-atomic /srv/app/o > /srv/app/p\n":                              "usage fs write-atomic",
		"fs write-atomic /srv/app/o <<< x > /srv/app/p\n":                                 "usage fs write-atomic",
		"echo x | fs write-atomic rel.txt\n":                                              "prefix_unset",
		"declare -s tok = hunter2-SECRET\necho $tok | fs write-atomic /srv/app/o\n":       "a secret cannot leave sash",
		"declare -s tok = hunter2-SECRET\necho $tok | cat | fs write-atomic /srv/app/o\n": "a secret cannot leave sash",
		"declare -s tok = hunter2-SECRET\necho x | fs write-atomic /srv/$tok\n":           "a secret cannot leave sash",
		"fn fs() { echo x }\n":                                                            "usage fn",
	} {
		h := &skillHost{}
		out := Run(src, skillGrant(nil), h, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || len(writes(h)) != 0 || strings.Contains(strings.Join(h.calls, "\n"), "hunter2") {
			t.Errorf("%q: want %q, got %+v, calls %q", src, want, out, h.calls)
		}
	}
	for src, drop := range map[string][]string{
		"echo x | fs write-atomic /etc/passwd\n":              nil,
		"echo x | fs write-atomic /srv/../etc/passwd\n":       nil,
		"echo x | fs write-atomic /var/log/outside\n":         nil,
		"echo x | fs write-atomic /srv/app/o\n":               {"file.write"},
		"cd /srv/app\necho x | fs write-atomic ../../etc/x\n": nil,
	} {
		h := &skillHost{}
		out := Run(src, skillGrant(drop), h, Options{})
		if out.Status != exitDenied || !strings.Contains(out.Stderr, "denied file.write") || len(writes(h)) != 0 {
			t.Errorf("%q: %+v, calls %q", src, out, h.calls)
		}
	}
}

// Plan mode prints the write line, with the mode when one was given, and
// sends nothing; confirm asks as > asks; a no is declined.
func TestFsWriteAtomicPlanAndConfirm(t *testing.T) {
	for src, want := range map[string]string{
		"echo hello | fs write-atomic /srv/app/o\n":             "plan: would write /srv/app/o (6 bytes)\n",
		"echo hello | fs write-atomic /srv/app/o --mode 0640\n": "plan: would write /srv/app/o (6 bytes) (mode 0640)\n",
	} {
		st := NewState()
		st.LockPlan()
		h := &skillHost{}
		out := Run(src, skillGrant(nil), h, Options{State: st})
		if out.Status != 0 || out.Stderr != want || len(writes(h)) != 0 {
			t.Errorf("%q: %+v, calls %q", src, out, h.calls)
		}
	}
	// A failed stage is not written in plan mode either, and says so.
	st := NewState()
	st.LockPlan()
	h := &skillHost{}
	if out := Run("echo a | egrep zzz | fs write-atomic /srv/app/o\n", skillGrant(nil), h, Options{State: st}); out.Status != 1 || strings.Contains(out.Stderr, "plan: would") || !strings.Contains(out.Stderr, "not written") {
		t.Errorf("plan after a failed stage: %+v", out)
	}
	for _, yes := range []bool{true, false} {
		var asked []string
		h := &skillHost{}
		out := Run("set -o confirm\necho x | fs write-atomic /srv/app/o --mode 0600\n", skillGrant(nil), h, Options{Confirm: func(q string) (bool, bool) {
			asked = append(asked, q)
			return yes, false
		}})
		if len(asked) != 1 || asked[0] != "confirm file.write > /srv/app/o? [y + Enter / N]" {
			t.Errorf("asked %q", asked)
		}
		if yes && (out.Status != 0 || len(writes(h)) != 1) || !yes && (out.Status != exitDenied || len(writes(h)) != 0 || !strings.Contains(out.Stderr, "declined file.write")) {
			t.Errorf("yes %v: %+v %q", yes, out, writes(h))
		}
	}
}

// Inside atomic { } the write is journalled like any file.write: begin,
// the write, commit; and a failure after it aborts.
func TestFsWriteAtomicInsideAtomic(t *testing.T) {
	h := &skillHost{}
	out := Run("atomic {\n  echo a | fs write-atomic /srv/app/one --mode 0644\n  echo b | fs write-atomic /srv/app/two\n}\n", skillGrant(nil), h, Options{})
	calls := strings.Join(h.calls, "\n")
	if out.Status != 0 || len(writes(h)) != 2 || !strings.Contains(calls, "atomic.begin") || !strings.Contains(calls, "atomic.commit") || strings.Contains(calls, "atomic.abort") {
		t.Errorf("%+v\n%s", out, calls)
	}
	if b, w, c := strings.Index(calls, "atomic.begin"), strings.Index(calls, "file.write"), strings.Index(calls, "atomic.commit"); !(b < w && w < c) {
		t.Errorf("order:\n%s", calls)
	}
	h = &skillHost{}
	out = Run("atomic {\n  echo a | fs write-atomic /srv/app/one\n  echo a | egrep zzz | fs write-atomic /srv/app/two\n}\n", skillGrant(nil), h, Options{})
	calls = strings.Join(h.calls, "\n")
	if out.Status == 0 || len(writes(h)) != 1 || !strings.Contains(calls, "atomic.abort") || strings.Contains(calls, "atomic.commit") {
		t.Errorf("abort: %+v\n%s", out, calls)
	}
}

// oldAgentParent is an agent too old for a file mode: it refuses a moded
// write and counts every call.
type oldAgentParent struct{ calls []parent.Verb }

func (p *oldAgentParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls = append(p.calls, v)
	if v.Mode != "" {
		return parent.Result{}, parent.ErrModeUnsupported
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

// An agent too old for the mode refuses, and the write is never sent
// again without it.
func TestFsWriteAtomicIsNotRetriedWithoutItsMode(t *testing.T) {
	p := &oldAgentParent{}
	out := Run("echo x | fs write-atomic /srv/app/o --mode 0600\necho after\n", skillGrant(nil), p, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "agent too old for file.write mode") || out.Stdout != "" {
		t.Errorf("%+v", out)
	}
	if len(p.calls) != 1 || p.calls[0].Mode != "0600" {
		t.Errorf("calls %+v", p.calls)
	}
	// The agent's own refusals print as > prints them.
	out = Run("echo x | fs write-atomic /srv/app/o\n", skillGrant(nil), errParent{"owner_changed"}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "refused file.write: owner_changed (") {
		t.Errorf("refusal: %+v", out)
	}
}

type errParent struct{ msg string }

func (p errParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{}, &textErr{p.msg}
}

type textErr struct{ s string }

func (e *textErr) Error() string { return e.s }

// --needs reports the write and its path; lint treats the sink as the
// mutation it is; why and the closed table name it.
func TestFsWriteAtomicNeedsLintWhy(t *testing.T) {
	got := Needs("cat /srv/app/a | fs write-atomic /srv/app/b --mode 0640\n", NeedsOptions{})
	if got.Status != 0 || !strings.Contains(got.Stdout, "file.write") || !strings.Contains(got.Stdout, "/srv/app/b") || !strings.Contains(got.Stdout, "yes") {
		t.Errorf("needs: %+v", got)
	}
	if got = Needs("cat /srv/app/a | fs write-atomic /srv/app/b\n", NeedsOptions{Policy: true}); !strings.Contains(got.Stdout, "--file-write /srv/app") {
		t.Errorf("needs --policy: %+v", got)
	}
	for src, rule := range map[string]string{
		"retry 3 { echo a | fs write-atomic /srv/app/o }\n":                                        "retry-on-mutation",
		"par 2 {\n echo a | fs write-atomic /srv/app/o\n echo b | fs write-atomic /srv/app/o\n}\n": "par-same-target",
		"let p = (term prompt \"path?\")\necho a | fs write-atomic $p\n":                           "prompt-to-mutation",
		"let b = (term prompt \"body?\")\necho $b | fs write-atomic /srv/app/o\n":                  "prompt-to-mutation",
		"declare -s t = (cat /srv/app/token)\necho $t | fs write-atomic /srv/app/o\n":              "secret-to-sink",
	} {
		if l := Lint(src); !strings.Contains(l.Stdout, rule) {
			t.Errorf("%q: want %s in\n%s", src, rule, l.Stdout)
		}
	}
	if l := Lint("echo a | fs write-atomic /srv/app/o\n"); l.Stdout != "" {
		t.Errorf("a plain sink is flagged:\n%s", l.Stdout)
	}
	if w := Why("echo a | fs write-atomic /srv/app/o", skillGrant(nil), Options{}); !strings.Contains(w.Stdout, "file.write") || !strings.Contains(w.Stdout, "granted") {
		t.Errorf("why: %+v", w)
	}
	if w := Why("echo a | fs write-atomic /etc/o", skillGrant(nil), Options{}); !strings.Contains(w.Stdout, "outside_roots") || !strings.Contains(w.Stdout, "roots: /srv /var/tmp") {
		t.Errorf("why outside the roots: %+v", w)
	}
	out := Run("builtin fs\n", grant.Grant{}, skillParent{t}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "fs write-atomic  verb  file.write") {
		t.Errorf("builtin: %+v", out)
	}
	for _, b := range Builtins() {
		if b.Form == "fs write-atomic" && (strings.Join(b.Flags, " ") != "--mode" || !mutating[b.Key]) {
			t.Errorf("table row %+v", b)
		}
	}
}

// A recording holds the write as any file.write, mode included and not
// redacted (a mode is not a secret here), and it replays.
func TestFsWriteAtomicRecordsAndReplays(t *testing.T) {
	const src = "echo x | fs write-atomic /srv/app/o --mode 0640\n"
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if out := Run(src, skillGrant(nil), &skillHost{}, Options{Tape: tape}); out.Status != 0 {
		t.Fatalf("record: %+v", out)
	}
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	if rec := buf.String(); !strings.Contains(rec, `"key":"file.write"`) || !strings.Contains(rec, `"path":"/srv/app/o"`) || !strings.Contains(rec, `"mode":"0640"`) || strings.Contains(rec, `"redacted":true`) {
		t.Errorf("recording:\n%s", rec)
	}
	replay, err := LoadReplay(bytes.NewReader(buf.Bytes()), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out := Run(src, skillGrant(nil), skillParent{t}, Options{Tape: replay}); out.Status != 0 {
		t.Errorf("replay: %+v", out)
	}
	if err := replay.Finish(); err != nil {
		t.Errorf("replay finish: %v", err)
	}
}
