package lang

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto"
)

// hostParent is a fake host: rows for ps and ls, a body for cat, a
// failing status, and a restart that fails twice before it works.
type hostParent struct {
	restarts int
}

func (h *hostParent) Do(v parent.Verb) (parent.Result, error) {
	switch {
	case v.Key == "ps":
		return parent.Result{Kind: parent.KindRows, Cols: []string{"user", "cmd"}, Rows: []parent.Row{{"user": "root", "cmd": "sshd"}, {"user": "ryan", "cmd": "vim"}}}, nil
	case len(v.Argv) > 0 && v.Argv[0] == "ls":
		return parent.Result{Kind: parent.KindRows, Rows: []parent.Row{{"name": "a.log", "type": "file"}, {"name": "b.log", "type": "file"}}}, nil
	case v.Path == "/var/log/s.yaml":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("a: 1\n")}, nil
	case v.Key == "file.read":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("content of " + v.Path + "\n")}, nil
	case v.Key == "unit.status":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("inactive\n"), Exit: 3}, nil
	case v.Key == "unit.restart":
		h.restarts++
		if h.restarts < 3 {
			return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
		}
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

func recGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"ps", "file.read", "file.write", "unit.status", "unit.restart"}, FileRead: []string{"/var/log", "/tmp"}, FileWrite: []string{"/tmp"}}
}

var recNow = time.Unix(1_800_000_000, 0)

// clocks pins the clock and the sleep for a recording: the clock moves
// a second per reading, a sleep moves it too.
func clocks(t *testing.T) *[]time.Duration {
	t.Helper()
	now := recNow
	var slept []time.Duration
	oldNow, oldSleep := nowFn, sleepFn
	nowFn = func() time.Time { now = now.Add(time.Second); return now }
	sleepFn = func(d time.Duration) { slept = append(slept, d); now = now.Add(d) }
	t.Cleanup(func() { nowFn, sleepFn = oldNow, oldSleep })
	return &slept
}

// noClock makes any clock reading or sleep fail the test: replay must
// answer them from the tape.
func noClock(t *testing.T) {
	t.Helper()
	oldNow, oldSleep := nowFn, sleepFn
	nowFn = func() time.Time { t.Error("replay read the clock"); return recNow }
	sleepFn = func(time.Duration) { t.Error("replay slept") }
	t.Cleanup(func() { nowFn, sleepFn = oldNow, oldSleep })
}

func record(t *testing.T, src string) ([]byte, Outcome) {
	t.Helper()
	clocks(t)
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, recNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(src, recGrant(), &hostParent{}, Options{Tape: tape, State: NewState()})
	if tape.Err() != nil {
		t.Fatalf("record %q: %v", src, tape.Err())
	}
	return buf.Bytes(), out
}

func replay(t *testing.T, rec []byte, src string) (Outcome, error) {
	t.Helper()
	noClock(t)
	tape, err := LoadReplay(bytes.NewReader(rec), src, recNow)
	if err != nil {
		return Outcome{}, err
	}
	cp := &countParent{}
	out := Run(src, recGrant(), cp, Options{Tape: tape, State: NewState()})
	if cp.n != 0 {
		t.Fatalf("replay called a parent %d times", cp.n)
	}
	return out, tape.Finish()
}

var recScripts = []string{
	"ps\nps |> where user == root",
	"cat /var/log/a",
	"unit status nginx; echo after",
	"echo hello > /tmp/out",
	"template_file /var/log/s.yaml /var/log/t /tmp/o",
	"retry 3 delay 2s { unit restart nginx }",
	"timeout 30s { unit restart a; unit restart b; unit restart c }",
	"echo [ uuid() ] [ uuid() ]\nps |> each [ uuid() ]",
	"set -o plan\nunit restart nginx\necho x > /tmp/y",
	"declare -s tok = s3cr3tvalue\necho $tok\nlet n = [ len($tok) ]\ncat /var/log/a",
	"declare -s tok = s3cr3tvalue\nlet x = [ int($tok) ]",
	"log info hello [ {\"k\": 1} ]",
	"ls /var/log/*.log",
	"try { unit restart x } catch e { echo $e.status }",
}

// Every script records and replays to the same output, with no parent,
// no clock, and no sleep in the replay.
func TestRecordReplayRoundTrip(t *testing.T) {
	for _, src := range recScripts {
		rec, out1 := record(t, src)
		out2, err := replay(t, rec, src)
		if err != nil {
			t.Errorf("%q: replay %v\n%s", src, err, rec)
			continue
		}
		if out1.Status != out2.Status || out1.Stdout != out2.Stdout || out1.Stderr != out2.Stderr {
			t.Errorf("%q:\nrecord %+v\nreplay %+v", src, out1, out2)
		}
	}
	// The events are the kinds the run produced.
	rec, _ := record(t, "set -o plan\nunit restart nginx\nretry 2 delay 1s { unit status x }\ntimeout 1m { echo [ uuid() ] }")
	for _, kind := range []string{`"kind":"plan"`, `"kind":"sleep"`, `"kind":"uuid"`, `"kind":"now"`} {
		if !bytes.Contains(rec, []byte(kind)) {
			t.Errorf("no %s event:\n%s", kind, rec)
		}
	}
	rec, _ = record(t, "declare -s tok = s3cr3tvalue\nlet x = [ int($tok) ]")
	if !bytes.Contains(rec, []byte(`"kind":"fault"`)) {
		t.Errorf("no fault event:\n%s", rec)
	}
}

// A secret never reaches a recording: not its text, its base64, or its
// hash, even when a request or a response holds it.
func TestRecordNeverHoldsASecret(t *testing.T) {
	const secret = "s3cr3tvalue"
	h := sha256.Sum256([]byte(secret))
	for _, src := range []string{
		"declare -s tok = s3cr3tvalue\necho $tok\ncat /var/log/a",
		"declare -s tok = /var/log/s3cr3tvalue\ncat /var/log/s3cr3tvalue",
		"declare -s tok = s3cr3tvalue\nlet x = [ int($tok) ]",
		"declare -s tok = s3cr3tvalue\ncat /var/log/s3cr3tvalue",
	} {
		rec, _ := record(t, src)
		for _, bad := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), hex.EncodeToString(h[:])} {
			if bytes.Contains(rec, []byte(bad)) {
				t.Errorf("%q: recording holds %q:\n%s", src, bad, rec)
			}
		}
		// Decoded too: a response body is base64 in the JSON.
		for _, l := range recLines(rec)[1:] {
			var ev recEvent
			if err := json.Unmarshal(l, &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Resp != nil {
				all := string(ev.Resp.Bytes) + ev.Resp.Error + ev.Resp.Value + strings.Join(ev.Resp.Cols, " ")
				for _, r := range ev.Resp.Rows {
					for k, v := range r {
						all += k + v
					}
				}
				if strings.Contains(all, secret) {
					t.Errorf("%q: a response holds the secret: %s", src, l)
				}
			}
		}
		if _, err := replay(t, rec, src); err != nil {
			t.Errorf("%q: redacted recording does not replay: %v", src, err)
		}
	}
	rec, _ := record(t, "declare -s tok = /var/log/s3cr3tvalue\ncat /var/log/s3cr3tvalue")
	if !bytes.Contains(rec, []byte(`"redacted":true`)) || !bytes.Contains(rec, []byte(`"argv":["***"]`)) {
		t.Fatalf("not redacted:\n%s", rec)
	}
}

func recLines(rec []byte) [][]byte {
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(rec))
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

// rechain recomputes every prev, as anyone holding the file can.
func rechain(t *testing.T, lines [][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	prev := sha256.Sum256(lines[0])
	buf.Write(lines[0])
	buf.WriteByte('\n')
	for i, l := range lines[1:] {
		var ev map[string]any
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		ev["prev"] = hex.EncodeToString(prev[:])
		ev["seq"] = i + 1
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
		prev = sha256.Sum256(b)
	}
	return buf.Bytes()
}

func edit(t *testing.T, line []byte, f func(map[string]any)) []byte {
	t.Helper()
	var ev map[string]any
	if err := json.Unmarshal(line, &ev); err != nil {
		t.Fatal(err)
	}
	f(ev)
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func diverged(err error) int {
	var te *TapeError
	if errors.As(err, &te) && te.Code == "replay_diverged" {
		return te.Seq
	}
	return -1
}

// Each field of a request must match: a recording that differs in the
// key, argv, path, body, or kind diverges at that event, as do a missing
// and an extra event; replay exits 2 replay_diverged <seq>.
func TestReplayDivergesAtEachField(t *testing.T) {
	src := "ps\necho hello > /tmp/out\ncat /var/log/a"
	rec, _ := record(t, src)
	lines := recLines(rec)
	// Find the events: 1 ps, then the write, then the read.
	idx := map[string]int{}
	for i, l := range lines[1:] {
		var ev recEvent
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Kind == "call" {
			idx[ev.Req.Key] = i + 1
		}
	}
	setReq := func(k string, v any) func(map[string]any) {
		return func(ev map[string]any) { ev["req"].(map[string]any)[k] = v }
	}
	cases := []struct {
		name string
		at   int
		f    func(map[string]any)
	}{
		{"key", idx["ps"], setReq("key", "journal")},
		{"argv", idx["ps"], setReq("argv", []string{"ps", "-ef"})},
		{"path", idx["file.read"], setReq("path", "/var/log/b")},
		{"body", idx["file.write"], setReq("body_sha256", strings.Repeat("0", 64))},
		{"body length", idx["file.write"], setReq("body_len", 99)},
		{"kind", idx["ps"], func(ev map[string]any) { ev["kind"] = "plan" }},
	}
	for _, c := range cases {
		ls := append([][]byte(nil), lines...)
		ls[c.at] = edit(t, ls[c.at], c.f)
		out, err := replay(t, rechain(t, ls), src)
		if diverged(err) != c.at && !strings.Contains(out.Stderr, "replay_diverged") {
			t.Errorf("%s: err %v out %+v", c.name, err, out)
		}
		if out.Status != exitType && err == nil {
			t.Errorf("%s: status %d", c.name, out.Status)
		}
	}
	// Missing the last event.
	out, err := replay(t, rechain(t, lines[:len(lines)-1]), src)
	if diverged(err) < 0 && !strings.Contains(out.Stderr, "replay_diverged") {
		t.Errorf("missing event: %v %+v", err, out)
	}
	// An extra event the run never asks for.
	extra := append(append([][]byte(nil), lines...), lines[len(lines)-1])
	if _, err := replay(t, rechain(t, extra), src); diverged(err) != len(extra)-1 {
		t.Errorf("extra event: %v", err)
	}
	if out, err := replay(t, rec, src); err != nil || out.Status != 0 {
		t.Fatalf("the untouched recording: %v %+v", err, out)
	}
}

// A changed byte anywhere breaks the chain at that event; another script,
// an expired recording, and a bad header are refused before any event.
func TestReplayRefusesBrokenRecordings(t *testing.T) {
	src := "ps\ncat /var/log/a"
	rec, _ := record(t, src)
	lines := recLines(rec)
	for i := 1; i < len(lines); i++ {
		ls := append([][]byte(nil), lines...)
		ls[i] = bytes.Replace(ls[i], []byte(`"line":`), []byte(`"line": `), 1)
		tampered := append(bytes.Join(ls, []byte("\n")), '\n')
		var te *TapeError
		if _, err := replay(t, tampered, src); i+1 < len(lines) && (!errors.As(err, &te) || te.Code != "replay_bad_chain" || te.Seq != i+1) {
			t.Errorf("tamper at %d: %v", i, err)
		}
	}
	ls := append([][]byte(nil), lines...)
	ls[1] = edit(t, ls[1], func(ev map[string]any) { ev["line"] = 7 })
	var te *TapeError
	if _, err := replay(t, append(bytes.Join(ls, []byte("\n")), '\n'), src); !errors.As(err, &te) || te.Code != "replay_bad_chain" || te.Seq != 2 {
		t.Fatalf("tampered event 1: %v", err)
	}
	if _, err := replay(t, rec, src+"\necho more"); !errors.As(err, &te) || te.Code != "replay_other_script" {
		t.Fatalf("other script: %v", err)
	}
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, recNow.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	clocks(t)
	Run(src, recGrant(), &hostParent{}, Options{Tape: tape})
	if _, err := replay(t, buf.Bytes(), src); !errors.As(err, &te) || te.Code != "replay_expired" {
		t.Fatalf("expired: %v", err)
	}
	for _, bad := range []string{"", "{}\n", `{"v":"sash-rec/2","script_sha256":"x","sash_version":"x","modules":{},"expires":9999999999}` + "\n", "not json\n"} {
		if _, err := replay(t, []byte(bad), src); !errors.As(err, &te) || (te.Code != "replay_bad_format" && te.Code != "replay_other_script") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// set -o record is not built in a session: it needs file.write with a
// mode.
func TestSetRecordRefused(t *testing.T) {
	for _, src := range []string{"set -o record", "set -o record /tmp/r.jsonl", "shopt -s record"} {
		out := Run(src, recGrant(), &countParent{}, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "record needs file.write mode") {
			t.Errorf("%q: %+v", src, out)
		}
	}
}

func TestRecordEventCapIsTheFrameCap(t *testing.T) {
	if recMaxEvent != proto.MaxFrame {
		t.Fatalf("recMaxEvent %d proto.MaxFrame %d", recMaxEvent, proto.MaxFrame)
	}
}
