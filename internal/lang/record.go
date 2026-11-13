package lang

// Record and replay, offline (docs/plans/2026-11-12-sash-v6-tier3.md §4).
//
// A Tape sits under machine.do, after the plan and confirm checks: in
// record mode it calls the real (offline, fake) parent and writes one JSON
// line per event; in replay mode it answers from the recording and never
// calls a parent. Events are call, plan, now, sleep, uuid, and fault, each
// {seq, line, kind, req, resp, prev}, where prev is the sha256 of the line
// before it, starting from the header {v, script_sha256, sash_version,
// modules, expires}.
//
// Threats: a recording concentrates host data, so it is off by default
// and offline only in this build (in-session recording waits for
// file.write with a mode). A secret never reaches it: a request whose
// stage read a secret, or whose argv, path, or body holds a bound secret's
// text, stores "***" and lengths, never a hash; so does a response that
// holds a secret's text. The chain proves the file is internally
// consistent, not who made it: anyone can recompute it, and sash holds no
// key. A recording never authorizes anything: replay drives only the tape,
// never a host, and refuses an expired recording or one for another
// script. The clock, sleeps, and uuid are pinned by recording each reading
// and feeding it back in replay only; recording still uses crypto/rand.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

// Version is this build's sash version, for a recording's header.
const Version = "sash/v6-dev"

const (
	recFormat = "sash-rec/1"
	// recMaxEvent caps one event line, as proto.MaxFrame caps a frame (a
	// test keeps them equal).
	recMaxEvent = 8 << 20
	redacted3   = "***"
)

type recHeader struct {
	V            string            `json:"v"`
	ScriptSHA256 string            `json:"script_sha256"`
	SashVersion  string            `json:"sash_version"`
	Modules      map[string]string `json:"modules"`
	Expires      int64             `json:"expires"`
}

type recReq struct {
	Key      string   `json:"key"`
	Argv     []string `json:"argv,omitempty"`
	Path     string   `json:"path,omitempty"`
	BodySHA  string   `json:"body_sha256,omitempty"`
	Redacted bool     `json:"redacted,omitempty"`
	ArgvLen  int      `json:"argv_len,omitempty"`
	PathLen  int      `json:"path_len,omitempty"`
	BodyLen  int      `json:"body_len,omitempty"`
}

type recResp struct {
	Kind     string       `json:"kind,omitempty"`
	Exit     int          `json:"exit,omitempty"`
	Cols     []string     `json:"cols,omitempty"`
	Rows     []parent.Row `json:"rows,omitempty"`
	Bytes    []byte       `json:"bytes,omitempty"`
	Error    string       `json:"error,omitempty"`
	Redacted bool         `json:"redacted,omitempty"`
	Len      int          `json:"len,omitempty"`
	Value    string       `json:"value,omitempty"`
}

type recEvent struct {
	Seq  int      `json:"seq"`
	Line int      `json:"line"`
	Kind string   `json:"kind"`
	Req  *recReq  `json:"req,omitempty"`
	Resp *recResp `json:"resp,omitempty"`
	Prev string   `json:"prev"`
}

// Tape is one recording, being written (record) or read back (replay).
type Tape struct {
	replay bool
	// record
	w    io.Writer
	prev string
	// replay
	events []recEvent
	pos    int
	// both
	seq      int
	nl       []int
	at       int
	err      error // first write error or divergence
	diverged int   // seq of the first divergence, or -1
}

// TapeError is how a tape failed: a divergence, a broken chain, an
// expired or foreign recording, or a write error. Its text is a fixed
// code plus a number, never recorded data.
type TapeError struct {
	Code string
	Seq  int
}

func (e *TapeError) Error() string {
	if e.Seq >= 0 {
		return e.Code + " " + strconv.Itoa(e.Seq)
	}
	return e.Code
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// NewRecorder writes the header for script to w and returns a recording
// tape. The recording expires at expires.
func NewRecorder(w io.Writer, script string, expires time.Time) (*Tape, error) {
	t := &Tape{w: w, diverged: -1}
	line, err := json.Marshal(recHeader{V: recFormat, ScriptSHA256: sha([]byte(script)), SashVersion: Version, Modules: map[string]string{}, Expires: expires.Unix()})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	t.prev = sha(line)
	return t, nil
}

// LoadReplay reads a recording for script and checks it before any
// event is used: the format, the script hash, the expiry against now, and
// every link of the chain.
func LoadReplay(r io.Reader, script string, now time.Time) (*Tape, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), recMaxEvent+1)
	if !sc.Scan() {
		return nil, &TapeError{"replay_bad_format", -1}
	}
	headLine := append([]byte(nil), sc.Bytes()...)
	var h recHeader
	dec := json.NewDecoder(bytes.NewReader(headLine))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil || h.V != recFormat || h.Expires == 0 {
		return nil, &TapeError{"replay_bad_format", -1}
	}
	if h.ScriptSHA256 != sha([]byte(script)) {
		return nil, &TapeError{"replay_other_script", -1}
	}
	if now.Unix() >= h.Expires {
		return nil, &TapeError{"replay_expired", -1}
	}
	t := &Tape{replay: true, diverged: -1}
	prev := sha(headLine)
	for sc.Scan() {
		line := sc.Bytes()
		var ev recEvent
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		if err := d.Decode(&ev); err != nil {
			return nil, &TapeError{"replay_bad_format", len(t.events) + 1}
		}
		if ev.Prev != prev || ev.Seq != len(t.events)+1 {
			return nil, &TapeError{"replay_bad_chain", len(t.events) + 1}
		}
		prev = sha(line)
		t.events = append(t.events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, &TapeError{"replay_bad_format", len(t.events) + 1}
	}
	return t, nil
}

// begin gives the tape the text being run, for line numbers.
func (t *Tape) begin(text string) {
	t.nl = t.nl[:0]
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			t.nl = append(t.nl, i)
		}
	}
}

func (t *Tape) line() int { return sort.SearchInts(t.nl, t.at) + 1 }

// Err is the tape's failure, or nil. Run turns a divergence into exit 2.
func (t *Tape) Err() error { return t.err }

// Finish is the end of a replay: an event the run never asked for is a
// divergence too.
func (t *Tape) Finish() error {
	if t.err == nil && t.replay && t.pos < len(t.events) {
		t.diverge(t.events[t.pos].Seq)
	}
	return t.err
}

func (t *Tape) diverge(seq int) {
	if t.err == nil {
		t.diverged = seq
		t.err = &TapeError{"replay_diverged", seq}
	}
}

// write records one event (record mode).
func (t *Tape) write(ev recEvent) {
	if t.err != nil {
		return
	}
	t.seq++
	ev.Seq, ev.Line, ev.Prev = t.seq, t.line(), t.prev
	line, err := json.Marshal(ev)
	if err == nil && len(line) > recMaxEvent {
		err = errors.New("record_too_large")
	}
	if err == nil {
		_, err = t.w.Write(append(line, '\n'))
	}
	if err != nil {
		t.err = &TapeError{"record_failed", t.seq}
		return
	}
	t.prev = sha(line)
}

// next takes the next event (replay mode) if it is kind, else diverges.
func (t *Tape) next(kind string) (recEvent, bool) {
	if t.err != nil {
		return recEvent{}, false
	}
	if t.pos >= len(t.events) {
		t.diverge(len(t.events) + 1)
		return recEvent{}, false
	}
	ev := t.events[t.pos]
	if ev.Kind != kind {
		t.diverge(ev.Seq)
		return recEvent{}, false
	}
	t.pos++
	return ev, true
}

// reqOf is the recorded form of a request: argv, path, and a body hash,
// or "***" and lengths when it is tainted or holds a secret's text.
func reqOf(v parent.Verb, tainted bool, secrets []string) *recReq {
	holds := func(s string) bool {
		for _, sec := range secrets {
			if sec != "" && strings.Contains(s, sec) {
				return true
			}
		}
		return false
	}
	if !tainted {
		tainted = holds(v.Path) || holds(string(v.Body))
		for _, a := range v.Argv {
			tainted = tainted || holds(a)
		}
	}
	if tainted {
		n := 0
		for _, a := range v.Argv {
			n += len(a) + 1
		}
		return &recReq{Key: v.Key, Redacted: true, Argv: []string{redacted3}, ArgvLen: n, PathLen: len(v.Path), BodyLen: len(v.Body)}
	}
	r := &recReq{Key: v.Key, Argv: append([]string(nil), v.Argv...), Path: v.Path}
	if v.Body != nil {
		r.BodySHA = sha(v.Body)
		r.BodyLen = len(v.Body)
	}
	return r
}

// respOf is the recorded form of a result, "***" and its length when it
// holds a secret's text.
func respOf(res parent.Result, err error, secrets []string) *recResp {
	r := &recResp{Kind: "bytes", Exit: res.Exit}
	if res.Kind == parent.KindRows {
		r.Kind = "rows"
	}
	if err != nil {
		r.Error = err.Error()
	}
	text := string(res.Bytes) + "\x00" + strings.Join(res.Cols, "\x00") + "\x00" + r.Error
	for _, row := range res.Rows {
		for k, v := range row {
			text += "\x00" + k + "\x00" + v
		}
	}
	for _, sec := range secrets {
		if sec != "" && strings.Contains(text, sec) {
			return &recResp{Kind: r.Kind, Exit: r.Exit, Redacted: true, Len: len(text)}
		}
	}
	r.Cols, r.Rows, r.Bytes = res.Cols, res.Rows, res.Bytes
	return r
}

// call is one door call under the tape: recorded through p, or answered
// from the recording.
func (t *Tape) call(p parent.Parent, v parent.Verb, tainted bool, secrets []string) (parent.Result, error) {
	req := reqOf(v, tainted, secrets)
	if !t.replay {
		res, err := p.Do(v)
		t.write(recEvent{Kind: "call", Req: req, Resp: respOf(res, err, secrets)})
		return res, err
	}
	ev, ok := t.next("call")
	if !ok {
		return parent.Result{}, t.err
	}
	want, _ := json.Marshal(ev.Req)
	got, _ := json.Marshal(req)
	if !bytes.Equal(want, got) {
		t.diverge(ev.Seq)
		return parent.Result{}, t.err
	}
	r := ev.Resp
	if r == nil {
		t.diverge(ev.Seq)
		return parent.Result{}, t.err
	}
	res := parent.Result{Kind: parent.KindBytes, Exit: r.Exit, Cols: r.Cols, Rows: r.Rows, Bytes: r.Bytes}
	if r.Kind == "rows" {
		res.Kind = parent.KindRows
	}
	if r.Redacted {
		res = parent.Result{Kind: parent.KindBytes, Exit: r.Exit, Bytes: []byte(redacted3)}
	}
	if r.Error != "" {
		return res, errors.New(r.Error)
	}
	return res, nil
}

// plan records a plan-mode stop, or checks it on replay.
func (t *Tape) plan(v parent.Verb, tainted bool, secrets []string) {
	req := reqOf(v, tainted, secrets)
	if !t.replay {
		t.write(recEvent{Kind: "plan", Req: req})
		return
	}
	if ev, ok := t.next("plan"); ok {
		want, _ := json.Marshal(ev.Req)
		got, _ := json.Marshal(req)
		if !bytes.Equal(want, got) {
			t.diverge(ev.Seq)
		}
	}
}

// value records a reading (now, sleep, uuid) from read, or returns the
// recorded one on replay without calling read.
func (t *Tape) value(kind string, read func() string) string {
	if !t.replay {
		v := read()
		t.write(recEvent{Kind: kind, Resp: &recResp{Value: v}})
		return v
	}
	ev, ok := t.next(kind)
	if !ok || ev.Resp == nil {
		return ""
	}
	return ev.Resp.Value
}

// fault records that a secret ended the line here, or checks it on replay.
func (t *Tape) fault() {
	if !t.replay {
		t.write(recEvent{Kind: "fault"})
		return
	}
	t.next("fault")
}

// tapeParent routes the calls Expand makes (glob listing) through the
// tape, so replay never reaches a parent and recording sees them.
type tapeParent struct {
	t *Tape
	p parent.Parent
}

func (tp tapeParent) Do(v parent.Verb) (parent.Result, error) {
	return tp.t.call(tp.p, v, false, nil)
}

// Machine seams: every clock reading, sleep, and uuid a run makes goes
// through these, so a tape can pin them.

func (m *machine) now() time.Time {
	if m.tape == nil {
		return nowFn()
	}
	v := m.tape.value("now", func() string { return nowFn().UTC().Format(time.RFC3339Nano) })
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		m.tape.diverge(m.tape.pos)
		m.ctl = ctlHalt
		return time.Time{}
	}
	return t
}

func (m *machine) sleep(d time.Duration) {
	if m.tape == nil {
		sleepFn(d)
		return
	}
	m.tape.value("sleep", func() string {
		sleepFn(d)
		return d.String()
	})
	if m.tape.err != nil {
		m.ctl = ctlHalt
	}
}

func (m *machine) uuidPin() func() (string, error) {
	if m.tape == nil {
		return nil
	}
	return func() (string, error) {
		var rerr error
		v := m.tape.value("uuid", func() string {
			s, err := fnUUID(nil, nil)
			if err != nil {
				rerr = err
				return ""
			}
			return s.(string)
		})
		if rerr != nil {
			return "", rerr
		}
		if m.tape.err != nil {
			m.ctl = ctlHalt
			return "", m.tape.err
		}
		return v, nil
	}
}

// secretFault records a secret fault on the tape.
func (m *machine) secretFault() {
	if m.tape != nil {
		m.tape.fault()
	}
}

// secretTexts are the text of every bound secret, for redaction.
func (m *machine) secretTexts() []string {
	var out []string
	for _, v := range m.vars {
		if v.kind == valData {
			if inner, ok := unsecret(v.d); ok {
				if s := textOf(inner); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// tapeDo is machine.do's call through the tape.
func (m *machine) tapeDo(v parent.Verb) (parent.Result, error) {
	res, err := m.tape.call(m.p, v, m.sawSecret, m.secretTexts())
	if m.tape.err != nil {
		m.ctl = ctlHalt
		return parent.Result{}, m.tape.err
	}
	return res, err
}
