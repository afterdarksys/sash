package lang

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

// agentWriteRule is the agent's own check on a file.write verb
// (systemapi-agent sash_verb.go, sashDoFileWrite): exactly argv [">",
// path] for the verb's path, else denied. Every fake parent that answers
// file.write applies it, so sash sending anything else fails a test.
func agentWriteRule(v parent.Verb) error {
	if v.Path == "" || len(v.Argv) != 2 || v.Argv[0] != ">" || v.Argv[1] != v.Path {
		return errors.New("denied")
	}
	return nil
}

// writeParent is hostParent that keeps every file.write (it never
// writes) and answers it with err.
type writeParent struct {
	hostParent
	data   string // /tmp/a's content, when set
	writes []parent.Verb
	err    error
}

func (p *writeParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "file.write" {
		if err := agentWriteRule(v); err != nil {
			return parent.Result{}, err
		}
		v.Body = append([]byte(nil), v.Body...)
		p.writes = append(p.writes, v)
		return parent.Result{Kind: parent.KindBytes}, p.err
	}
	if v.Path == "/tmp/a" && p.data != "" {
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte(p.data)}, nil
	}
	return p.hostParent.Do(v)
}

// A session recording starts at set -o record, covers every later line,
// and is written once, through file.write with mode 0600, at set +o
// record; it replays.
func TestSessionRecordWritten(t *testing.T) {
	clocks(t)
	p := &writeParent{}
	st := NewState()
	opt := Options{State: st}
	src := "set -o record /tmp/s.jsonl\nps"
	if out := Run(src, recGrant(), p, opt); out.Status != 0 || !st.Recording() {
		t.Fatalf("%+v", out)
	}
	if out := Run("cat /tmp/a", recGrant(), p, opt); out.Status != 0 || len(p.writes) != 0 {
		t.Fatalf("written early: %+v %+v", out, p.writes)
	}
	if out := Run("set +o record", recGrant(), p, opt); out.Status != 0 || st.Recording() {
		t.Fatalf("%+v", out)
	}
	if len(p.writes) != 1 || p.writes[0].Mode != "0600" || p.writes[0].Path != "/tmp/s.jsonl" || p.writes[0].Argv[0] != ">" {
		t.Fatalf("writes %+v", p.writes)
	}
	body := string(p.writes[0].Body)
	if !strings.Contains(body, `"key":"ps"`) || !strings.Contains(body, `"path":"/tmp/a"`) || strings.Contains(body, "segment") {
		t.Fatalf("body %s", body)
	}
	// A script that starts recording at its first statement replays.
	p = &writeParent{}
	st = NewState()
	src = "set -o record /tmp/s.jsonl\nps\ncat /tmp/a"
	out := Run(src, recGrant(), p, Options{State: st})
	fin := FinishRecording(recGrant(), p, Options{State: st})
	if out.Status != 0 || fin.Status != 0 || len(p.writes) != 1 || p.writes[0].Mode != "0600" {
		t.Fatalf("%+v %+v %+v", out, fin, p.writes)
	}
	got, err := replay(t, p.writes[0].Body, src)
	if err != nil || got.Stdout != out.Stdout {
		t.Fatalf("replay: %v %+v", err, got)
	}
	if fin := FinishRecording(recGrant(), p, Options{State: st}); fin.Status != 0 || len(p.writes) != 1 {
		t.Fatal("finished twice")
	}
}

// Over the segment cap, a recording is written as <path>.1 .. <path>.N,
// each at 0600 and within the cap; replay reads them in order with the
// chain checked across them. A segment from another recording, a missing
// one, a reordered set, or an edited one is refused.
func TestSessionRecordSegments(t *testing.T) {
	old := recSegMax
	recSegMax = 2048
	defer func() { recSegMax = old }()
	src := "set -o record /tmp/s.jsonl\nfor i in [ 1..40 ] { cat /tmp/a }"
	rec := func(data string) [][]byte {
		clocks(t)
		p := &writeParent{data: data}
		st := NewState()
		if out := Run(src, recGrant(), p, Options{State: st}); out.Status != 0 {
			t.Fatalf("%+v", out)
		}
		if fin := FinishRecording(recGrant(), p, Options{State: st}); fin.Status != 0 {
			t.Fatalf("%+v", fin)
		}
		var segs [][]byte
		for i, w := range p.writes {
			if w.Mode != "0600" || w.Path != fmt.Sprintf("/tmp/s.jsonl.%d", i+1) || len(w.Body) > recSegMax {
				t.Fatalf("segment %d: %s mode %q %d bytes", i+1, w.Path, w.Mode, len(w.Body))
			}
			segs = append(segs, w.Body)
		}
		if len(segs) < 3 {
			t.Fatalf("%d segments", len(segs))
		}
		return segs
	}
	a := rec("alpha\n")
	b := rec("bravo\n")
	if RecSegments(a[0]) != len(a) {
		t.Fatalf("RecSegments %d of %d", RecSegments(a[0]), len(a))
	}
	load := func(segs [][]byte) error {
		var rs []io.Reader
		for _, s := range segs {
			rs = append(rs, bytes.NewReader(s))
		}
		noClock(t)
		tape, err := LoadReplaySegments(rs, src, recNow)
		if err != nil {
			return err
		}
		cp := &countParent{}
		out := Run(src, recGrant(), cp, Options{Tape: tape})
		if cp.n != 0 || out.Status != 0 || !strings.Contains(out.Stdout, "alpha") {
			return fmt.Errorf("replay %+v (parent calls %d)", out, cp.n)
		}
		return tape.Finish()
	}
	if err := load(a); err != nil {
		t.Fatal(err)
	}
	spliced := append([][]byte{a[0], b[1]}, a[2:]...)
	edited := append([][]byte(nil), a...)
	edited[1] = bytes.Replace(a[1], []byte(`"seq":`), []byte(`"seq": `), 1)
	for name, segs := range map[string][][]byte{
		"another recording's segment": spliced,
		"missing last":                a[:len(a)-1],
		"missing middle":              append([][]byte{a[0]}, a[2:]...),
		"reordered":                   append([][]byte{a[0], a[2], a[1]}, a[3:]...),
		"edited":                      edited,
		"second alone":                a[1:],
	} {
		if err := load(segs); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

// A secret never reaches any segment.
func TestSessionRecordSecret(t *testing.T) {
	old := recSegMax
	recSegMax = 2048
	defer func() { recSegMax = old }()
	clocks(t)
	p := &writeParent{data: "hunter2-long\n"}
	st := NewState()
	src := "set -o record /tmp/s.jsonl\ndeclare -s tok = (cat /tmp/a)\nfor i in [ 1..20 ] { cat /tmp/a }"
	if out := Run(src, recGrant(), p, Options{State: st}); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	if fin := FinishRecording(recGrant(), p, Options{State: st}); fin.Status != 0 {
		t.Fatalf("%+v", fin)
	}
	if len(p.writes) < 2 {
		t.Fatalf("%d segments", len(p.writes))
	}
	for i, w := range p.writes {
		// In clear, as response bytes (base64), or hashed.
		secretFree(t, fmt.Sprintf("segment %d", i+1), w.Body, "hunter2-long")
	}
	// A secret bound by the last statement redacts what came before it.
	p = &writeParent{data: "hunter2-long\n"}
	st = NewState()
	Run("set -o record /tmp/s.jsonl\ncat /tmp/a\ndeclare -s tok = (cat /tmp/a)", recGrant(), p, Options{State: st})
	if fin := FinishRecording(recGrant(), p, Options{State: st}); fin.Status != 0 || len(p.writes) != 1 {
		t.Fatalf("last-statement secret: %+v", fin)
	}
	secretFree(t, "last-statement secret", p.writes[0].Body, "hunter2-long")
}

// An agent too old for the mode: the recording is not written, the error
// says why, and the write is never tried again without the mode. Over the
// real wire, against an agent without mode.
func TestSessionRecordOldAgent(t *testing.T) {
	clocks(t)
	p := &writeParent{err: parent.ErrModeUnsupported}
	st := NewState()
	Run("set -o record /tmp/s.jsonl\nps", recGrant(), p, Options{State: st})
	fin := FinishRecording(recGrant(), p, Options{State: st})
	if fin.Status != exitType || !strings.Contains(fin.Stderr, "agent too old for file.write mode") ||
		!strings.Contains(fin.Stderr, "record: not written: 0 of 1 segments written") || len(p.writes) != 1 || p.writes[0].Mode != "0600" {
		t.Fatalf("%+v %+v", fin, p.writes)
	}
	a := &prototest.Agent{Max: 0, Handle: func(v parent.Verb) (parent.Result, error) {
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
	}}
	c1, c2 := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		a.Serve(c2)
	}()
	c := &proto.Client{RW: c1}
	st = NewState()
	Run("set -o record /tmp/s.jsonl\ncat /tmp/a", recGrant(), c, Options{State: st})
	fin = FinishRecording(recGrant(), c, Options{State: st})
	c1.Close()
	<-served
	for _, r := range a.Stats().Ran {
		if strings.HasPrefix(r, "file.write") {
			t.Fatalf("an old agent wrote the recording: %+v", a.Stats())
		}
	}
	if fin.Status != exitType || !strings.Contains(fin.Stderr, "agent too old for file.write mode") {
		t.Fatalf("wire: %+v", fin)
	}
}

// Plan mode refuses to start a recording; confirm asks before writing it.
func TestSessionRecordPlanAndConfirm(t *testing.T) {
	clocks(t)
	st := NewState()
	st.LockPlan()
	p := &writeParent{}
	if out := Run("set -o record /tmp/s.jsonl", recGrant(), p, Options{State: st}); out.Status != exitType || !strings.Contains(out.Stderr, "refused in plan mode") || st.Recording() {
		t.Fatalf("plan: %+v", out)
	}
	for _, yes := range []bool{true, false} {
		p := &writeParent{}
		var asked []string
		opt := Options{State: NewState(), Confirm: func(q string) (bool, bool) { asked = append(asked, q); return yes, false }}
		Run("set -o confirm\nset -o record /tmp/s.jsonl\nps", recGrant(), p, opt)
		fin := FinishRecording(recGrant(), p, opt)
		if len(asked) != 1 || !strings.Contains(asked[0], "file.write > /tmp/s.jsonl") {
			t.Fatalf("asked %q", asked)
		}
		if yes && (fin.Status != 0 || len(p.writes) != 1) {
			t.Fatalf("yes: %+v %+v", fin, p.writes)
		}
		if !yes && (fin.Status != exitDenied || len(p.writes) != 0 || !strings.Contains(fin.Stderr, "record: not written")) {
			t.Fatalf("no: %+v %+v", fin, p.writes)
		}
	}
}

// The path is a literal under a file_write root; needs and lint report it.
func TestSessionRecordPathAndNeeds(t *testing.T) {
	p := &writeParent{}
	for src, want := range map[string]string{
		"set -o record /etc/r.jsonl":         "denied file.write",
		"let x = /tmp/r\nset -o record $x":   "the path must be literal",
		"set -o record":                      "usage set -o record",
		"shopt -s record":                    "usage set -o record",
		"with set -o record { ps }":          "usage set -o record",
		"par 2 { set -o record /tmp/r; ps }": "not in a par branch",
	} {
		st := NewState()
		if out := Run(src, recGrant(), p, Options{State: st}); out.Status == 0 || !strings.Contains(out.Stderr, want) || st.Recording() {
			t.Errorf("%q: %+v", src, out)
		}
	}
	st := NewState()
	Run("set -o record /tmp/r", recGrant(), p, Options{State: st})
	if out := Run("set -o record /tmp/q", recGrant(), p, Options{State: st}); out.Status != exitType || !strings.Contains(out.Stderr, "already recording") {
		t.Errorf("twice: %+v", out)
	}
	if out := Needs("set -o record /tmp/r.jsonl\nps", NeedsOptions{}); !strings.Contains(out.Stdout, "file.write") || !strings.Contains(out.Stdout, "/tmp/r.jsonl") {
		t.Errorf("needs: %+v", out)
	}
	if out := Needs("set -o record $x", NeedsOptions{}); !strings.Contains(out.Stdout, "dynamic") {
		t.Errorf("needs dynamic: %+v", out)
	}
	if rows := lintRows(t, Lint("set -o record /tmp/r.jsonl\nps")); !hasRule(rows, "record-file", "info") {
		t.Errorf("lint: %+v", rows)
	}
	var te *TapeError
	if _, err := LoadReplaySegments(nil, "", recNow); !errors.As(err, &te) {
		t.Errorf("no segments: %v", err)
	}
}

// Over the real wire, an agent that applies its own file.write rule
// accepts the recording write: argv [">", path], mode 0600.
func TestSessionRecordOverTheWire(t *testing.T) {
	clocks(t)
	var got []parent.Verb
	a := &prototest.Agent{Max: 4, Handle: func(v parent.Verb) (parent.Result, error) {
		if v.Key == "file.write" {
			got = append(got, v)
		}
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
	}}
	c1, c2 := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		a.Serve(c2)
	}()
	c := &proto.Client{RW: c1}
	st := NewState()
	Run("set -o record /tmp/s.jsonl\ncat /tmp/a", recGrant(), c, Options{State: st})
	fin := FinishRecording(recGrant(), c, Options{State: st})
	c1.Close()
	<-served
	if fin.Status != 0 || len(got) != 1 || got[0].Mode != "0600" || got[0].Path != "/tmp/s.jsonl" {
		t.Fatalf("%+v %+v", fin, got)
	}
}

// A hangup stops the run at its next statement (129) and starts no verb,
// but the session recording is still written.
func TestSessionRecordHangup(t *testing.T) {
	clocks(t)
	p := &writeParent{}
	st := NewState()
	Run("set -o record /tmp/s.jsonl\nps", recGrant(), p, Options{State: st})
	st.HangUp()
	if out := Run("ps\nps", recGrant(), p, Options{State: st}); out.Status != exitHangup || !strings.Contains(out.Stderr, "hangup") {
		t.Fatalf("%+v", out)
	}
	if fin := FinishRecording(recGrant(), p, Options{State: st}); fin.Status != 0 || len(p.writes) != 1 || p.writes[0].Mode != "0600" {
		t.Fatalf("%+v %+v", fin, p.writes)
	}
}

// At the size cap a session recording stops, warns once, never halts the
// session's verbs, and is written with a final truncated event; replay
// says it is partial.
func TestSessionRecordTruncated(t *testing.T) {
	oldTotal, oldEvents := recMaxTotal, recMaxEvents
	defer func() { recMaxTotal, recMaxEvents = oldTotal, oldEvents }()
	recMaxEvents = 5
	clocks(t)
	p := &writeParent{}
	st := NewState()
	src := "set -o record /tmp/s.jsonl\nfor i in [ 1..10 ] { cat /tmp/a }\necho done"
	out := Run(src, recGrant(), p, Options{State: st})
	if out.Status != 0 || !strings.Contains(out.Stdout, "done") || strings.Count(out.Stderr, "reached its size cap") != 1 {
		t.Fatalf("%+v", out)
	}
	if strings.Count(out.Stdout, "content of /tmp/a") != 10 {
		t.Fatalf("verbs stopped: %+v", out)
	}
	fin := FinishRecording(recGrant(), p, Options{State: st})
	if fin.Status != 0 || len(p.writes) != 1 || !strings.Contains(fin.Stderr, "written truncated") {
		t.Fatalf("%+v %+v", fin, p.writes)
	}
	lines := recLines(p.writes[0].Body)
	if !bytes.Contains(lines[len(lines)-1], []byte(`"kind":"truncated"`)) || len(lines) != 1+4+1 {
		t.Fatalf("recording:\n%s", p.writes[0].Body)
	}
	_, err := replay(t, p.writes[0].Body, src)
	var te *TapeError
	if !errors.As(err, &te) || te.Code != "replay_truncated" {
		t.Fatalf("replay: %v", err)
	}
	// An event too large for a segment truncates there too.
	oldSeg := recSegMax
	defer func() { recSegMax = oldSeg }()
	recMaxEvents = oldEvents
	recSegMax = 1024
	clocks(t)
	p = &writeParent{data: strings.Repeat("x", 2048)}
	st = NewState()
	Run("set -o record /tmp/s.jsonl\nps\ncat /tmp/a\nps", recGrant(), p, Options{State: st})
	fin = FinishRecording(recGrant(), p, Options{State: st})
	if fin.Status != 0 || !strings.Contains(fin.Stderr, "written truncated") || len(p.writes) == 0 {
		t.Fatalf("big event: %+v %d writes", fin, len(p.writes))
	}
	last := p.writes[len(p.writes)-1].Body
	if !bytes.Contains(last, []byte(`"kind":"truncated"`)) || bytes.Contains(last, []byte(strings.Repeat("x", 100))) {
		t.Fatalf("big event recording: %s", last)
	}
}
