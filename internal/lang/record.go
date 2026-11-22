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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/parent"
)

// Version is this build's sash version, for a recording's header.
const Version = "sash/v6-dev"

const (
	recFormat = "sash-rec/1"
	// recMaxEvent caps one event line, as proto.MaxFrame caps a frame (a
	// test keeps them equal).
	recMaxEvent = 8 << 20
	// The caps below bound a whole recording, at record time
	// (it is buffered in memory until Finish) and at replay load: 64 MiB
	// is eight full frames, far past a test or debug run, and a million
	// events allows a long loop's clock readings without letting a
	// recording grow without bound.
	redacted3 = "***"
)

// The whole-recording caps are variables so tests can lower them.
var (
	recMaxTotal  = 64 << 20
	recMaxEvents = 1_000_000
)

type recHeader struct {
	V            string            `json:"v"`
	ScriptSHA256 string            `json:"script_sha256"`
	SashVersion  string            `json:"sash_version"`
	Modules      map[string]string `json:"modules"`
	Expires      int64             `json:"expires"`
	// A segmented recording (in a session, over recSegMax) is files
	// <path>.1 .. <path>.N. Each header names its segment and the count,
	// and from the second on, the sha256 of the previous segment's last
	// line; the segment's first event chains to its own header. An
	// unsegmented recording has none of these.
	Segment     int    `json:"segment,omitempty"`
	Segments    int    `json:"segments,omitempty"`
	PrevSegment string `json:"prev_segment,omitempty"`
	// Prompt marks a session recording: its lines were typed at the
	// prompt, so a replay runs them under the prompt's rules (path.go).
	Prompt bool `json:"prompt,omitempty"`
}

// recSegMax is the largest session recording segment: file.write sends
// the body base64-encoded in one frame of at most proto.MaxFrame, so a
// segment is three quarters of that, less room for the rest of the frame.
// A variable so tests can lower it.
var recSegMax = (8<<20)/4*3 - 64<<10

type recReq struct {
	Key      string   `json:"key"`
	Argv     []string `json:"argv,omitempty"`
	Path     string   `json:"path,omitempty"`
	BodySHA  string   `json:"body_sha256,omitempty"`
	Mode     string   `json:"mode,omitempty"`
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
	Seq  int    `json:"seq"`
	Line int    `json:"line"`
	Kind string `json:"kind"`
	// Branch is a par branch ("<par>.<branch>", both from 1) and BSeq
	// its own sequence; both are empty outside par.
	Branch string   `json:"branch,omitempty"`
	BSeq   int      `json:"bseq,omitempty"`
	Req    *recReq  `json:"req,omitempty"`
	Resp   *recResp `json:"resp,omitempty"`
	Prev   string   `json:"prev"`
}

// pending is one recorded event held in memory until Finish, unredacted,
// so it can be redacted against every secret the whole run bound.
type pending struct {
	branch  string
	bseq    int
	kind    string
	line    int
	v       *parent.Verb
	tainted bool
	res     parent.Result
	err     error
	hasResp bool
	value   string
}

// Tape is one recording, being written (record) or read back (replay),
// as seen by one machine: the top level, or one par branch. Every view
// shares one tapeCore under its mutex. Each branch keeps its own stream:
// recording appends events in completion order (the chain covers that
// merged order), and replay answers each branch from its own events in
// its own order, never comparing the merged order, and requires every
// event to be used exactly once.
type Tape struct {
	*tapeCore
	branch string // "" at top level
	bseq   int    // events this branch has recorded
	at     int
}

type tapeCore struct {
	mu     sync.Mutex
	replay bool
	// record: nothing reaches w until Finish
	w        io.Writer
	header   recHeader
	pend     []pending
	size     int64
	secrets  map[string]bool
	finished bool
	// replay: events, the indexes of each branch's events, and how many
	// of each a run has used
	events []recEvent
	by     map[string][]int
	used   map[string]int
	// segMax, when set, is a session recording: Finish keeps the
	// output in segs, split at segMax, instead of writing w.
	segMax int
	segs   [][]byte
	// truncated: a session recording reached a cap and stopped (it is
	// written with a final truncated event); warn is the one warning not
	// yet printed. On replay, truncAt is that event's seq.
	truncated bool
	warn      bool
	truncAt   int
	// both
	pars     int
	nl       []int
	err      error // first write error or divergence
	diverged int   // seq of the first divergence, or -1
}

// branchView is the tape for branch i (from 0) of par p (from newPar).
func (t *Tape) branchView(p, i int) *Tape {
	return &Tape{tapeCore: t.tapeCore, branch: strconv.Itoa(p) + "." + strconv.Itoa(i+1), at: t.at}
}

// newPar numbers the next par statement, from 1. Par is never nested and
// a branch never starts one, so the numbering is the same on replay.
func (t *Tape) newPar() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pars++
	return t.pars
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

// NewRecorder returns a recording tape for script that expires at
// expires. Nothing is written to w until Finish: every event is held in
// memory, so it can be redacted against every secret the run ever bound,
// even one bound after the event (declare -s tok = (cat ...)). A run
// that never reaches Finish writes nothing.
func NewRecorder(w io.Writer, script string, expires time.Time) (*Tape, error) {
	return &Tape{tapeCore: &tapeCore{w: w, diverged: -1, secrets: map[string]bool{},
		header: recHeader{V: recFormat, ScriptSHA256: sha([]byte(script)), SashVersion: Version, Modules: map[string]string{}, Expires: expires.Unix()}}}, nil
}

// LoadReplay reads a recording for script and checks it before any
// event is used: the format, the script hash, the expiry against now, and
// every link of the chain.
func LoadReplay(r io.Reader, script string, now time.Time) (*Tape, error) {
	return LoadReplaySegments([]io.Reader{r}, script, now)
}

// RecSegments reads a recording's first header line and returns how many
// segments it has: 0 for an unsegmented recording, else N (the file is
// segment 1 of N, named <base>.1). It checks nothing else.
func RecSegments(first []byte) int {
	line, _, _ := bytes.Cut(first, []byte("\n"))
	var h recHeader
	if json.Unmarshal(line, &h) != nil || h.Segment != 1 {
		return 0
	}
	return h.Segments
}

// LoadReplaySegments is LoadReplay over a recording's segments in order.
// Every segment's header must match the first's, name its place and the
// same count, and carry the sha256 of the previous segment's last line;
// the chain runs through every header and event, and seq continues across
// segments. A missing, extra, reordered, or edited segment is refused.
func LoadReplaySegments(rs []io.Reader, script string, now time.Time) (*Tape, error) {
	if len(rs) == 0 {
		return nil, &TapeError{"replay_bad_format", -1}
	}
	var t *Tape
	var first recHeader
	prev := ""
	total := 0
	for k, r := range rs {
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
		if k == 0 {
			if h.ScriptSHA256 != sha([]byte(script)) {
				return nil, &TapeError{"replay_other_script", -1}
			}
			if now.Unix() >= h.Expires {
				return nil, &TapeError{"replay_expired", -1}
			}
			segmented := h.Segment != 0 || h.Segments != 0 || h.PrevSegment != ""
			if (len(rs) > 1 || segmented) && (h.Segment != 1 || h.Segments != len(rs) || h.Segments < 2 || h.PrevSegment != "") {
				return nil, &TapeError{"replay_bad_segment", 1}
			}
			first = h
			t = &Tape{tapeCore: &tapeCore{replay: true, diverged: -1, header: h, by: map[string][]int{}, used: map[string]int{}}}
		} else {
			same := h
			same.Segment, same.PrevSegment = first.Segment, first.PrevSegment
			a, errA := json.Marshal(same)
			b, errB := json.Marshal(first)
			if errA != nil || errB != nil || !bytes.Equal(a, b) || h.Segment != k+1 || h.PrevSegment != prev {
				return nil, &TapeError{"replay_bad_segment", k + 1}
			}
		}
		prev = sha(headLine)
		total += len(headLine) + 1
		if err := t.loadEvents(sc, &prev, &total); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// loadEvents reads one segment's events after its header, chaining from
// prev.
func (t *Tape) loadEvents(sc *bufio.Scanner, prevp *string, totalp *int) error {
	prev, total := *prevp, *totalp
	defer func() { *prevp, *totalp = prev, total }()
	for sc.Scan() {
		line := sc.Bytes()
		total += len(line) + 1
		if total > recMaxTotal || len(t.events) >= recMaxEvents {
			return &TapeError{"replay_too_large", len(t.events) + 1}
		}
		var ev recEvent
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		if err := d.Decode(&ev); err != nil {
			return &TapeError{"replay_bad_format", len(t.events) + 1}
		}
		if ev.Prev != prev || ev.Seq != len(t.events)+1 {
			return &TapeError{"replay_bad_chain", len(t.events) + 1}
		}
		prev = sha(line)
		if (ev.Branch == "") != (ev.BSeq == 0) || t.truncAt != 0 || (ev.Kind == "truncated" && ev.Branch != "") {
			// Nothing follows a truncated event.
			return &TapeError{"replay_bad_format", len(t.events) + 1}
		}
		if ev.Kind == "truncated" {
			t.truncAt = ev.Seq
		}
		t.by[ev.Branch] = append(t.by[ev.Branch], len(t.events))
		t.events = append(t.events, ev)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return &TapeError{"replay_too_large", len(t.events) + 1}
		}
		return &TapeError{"replay_bad_format", len(t.events) + 1}
	}
	return nil
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
func (t *Tape) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Finish ends the tape. On replay, an event the run never asked for is a
// divergence. On record, every event is redacted against every secret
// text the run bound, chained, and written in one go; on any error
// nothing is written.
func (t *Tape) Finish() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.replay {
		// Every event used exactly once: the first one any stream left
		// unused diverges.
		first := 0
		for b, list := range t.by {
			if u := t.used[b]; u < len(list) {
				if seq := t.events[list[u]].Seq; first == 0 || seq < first {
					first = seq
				}
			}
		}
		if t.err == nil && t.truncAt != 0 {
			// A partial recording never replays as complete.
			t.err = &TapeError{"replay_truncated", t.truncAt}
		}
		if t.err == nil && first != 0 {
			t.divergeLocked(first)
		}
		return t.err
	}
	if t.finished {
		return t.err
	}
	t.finished = true
	if t.err != nil {
		return t.err
	}
	secrets := make([]string, 0, len(t.secrets))
	for s := range t.secrets {
		secrets = append(secrets, s)
	}
	sort.Strings(secrets)
	evs := make([]recEvent, len(t.pend))
	sizes := make([]int, len(t.pend))
	zero := strings.Repeat("0", 64)
	total := 0
	limit, evMax := recMaxTotal, recMaxEvent
	if t.segMax > 0 {
		// Room for the truncated event, and every event must fit a
		// segment with its header.
		limit -= 1 << 10
		evMax = min(evMax, t.segMax-t.segHeaderMax()-1)
	}
	for i, p := range t.pend {
		ev := recEvent{Seq: i + 1, Line: p.line, Kind: p.kind, Branch: p.branch, BSeq: p.bseq}
		if p.v != nil {
			ev.Req = reqOf(*p.v, p.tainted, secrets)
		}
		if p.hasResp {
			ev.Resp = respOf(p.res, p.err, secrets, p.tainted)
		}
		if p.kind == "now" || p.kind == "sleep" || p.kind == "uuid" {
			ev.Resp = &recResp{Value: p.value}
			if holdsAny(p.value, secrets) {
				ev.Resp = &recResp{Value: redacted3}
			}
		}
		// Every prev is 64 hex digits, so a line's size is known before
		// the chain is.
		ev.Prev = zero
		line, err := json.Marshal(ev)
		if err != nil {
			t.err = &TapeError{"record_failed", i + 1}
			return t.err
		}
		if len(line) > evMax || total+len(line)+1 > limit {
			if t.segMax == 0 {
				t.err = &TapeError{"record_too_large", i + 1}
				return t.err
			}
			t.truncated = true
			evs, sizes = evs[:i], sizes[:i]
			break
		}
		total += len(line) + 1
		evs[i], sizes[i] = ev, len(line)
	}
	if t.truncated {
		// The last event says the recording stops here, so replay knows
		// it is partial.
		ev := recEvent{Seq: len(evs) + 1, Kind: "truncated", Prev: zero}
		line, err := json.Marshal(ev)
		if err != nil {
			t.err = &TapeError{"record_failed", ev.Seq}
			return t.err
		}
		total += len(line) + 1
		evs, sizes = append(evs, ev), append(sizes, len(line))
	}
	groups, err := t.segment(sizes, total)
	if err != nil {
		t.err = err
		return t.err
	}
	t.segs = make([][]byte, len(groups))
	prev := ""
	for k, g := range groups {
		h := t.header
		if len(groups) > 1 {
			h.Segment, h.Segments, h.PrevSegment = k+1, len(groups), prev
		}
		var buf bytes.Buffer
		head, err := json.Marshal(h)
		if err != nil {
			t.err = &TapeError{"record_failed", 0}
			return t.err
		}
		buf.Write(head)
		buf.WriteByte('\n')
		prev = sha(head)
		for _, i := range g {
			ev := evs[i]
			ev.Prev = prev
			line, err := json.Marshal(ev)
			if err != nil {
				t.err = &TapeError{"record_failed", i + 1}
				return t.err
			}
			buf.Write(line)
			buf.WriteByte('\n')
			prev = sha(line)
		}
		t.segs[k] = buf.Bytes()
	}
	if t.segMax > 0 {
		return nil
	}
	if _, err := t.w.Write(t.segs[0]); err != nil {
		t.err = &TapeError{"record_failed", len(t.pend)}
	}
	return t.err
}

// segment splits the events into segments of at most segMax bytes, each
// with its header: one group when the recording is offline or fits. An
// event too large for a segment with its header is record_too_large.
func (t *tapeCore) segment(sizes []int, total int) ([][]int, error) {
	all := make([]int, len(sizes))
	for i := range all {
		all[i] = i
	}
	plain, err := json.Marshal(t.header)
	if err != nil {
		return nil, &TapeError{"record_failed", 0}
	}
	if t.segMax == 0 || len(plain)+1+total <= t.segMax {
		return [][]int{all}, nil
	}
	big := t.segHeaderMax()
	var groups [][]int
	var cur []int
	used := big + 1
	for i, n := range sizes {
		if used+n+1 > t.segMax {
			if len(cur) == 0 {
				return nil, &TapeError{"record_too_large", i + 1}
			}
			groups = append(groups, cur)
			cur, used = nil, big+1
			if used+n+1 > t.segMax {
				return nil, &TapeError{"record_too_large", i + 1}
			}
		}
		cur = append(cur, i)
		used += n + 1
	}
	return append(groups, cur), nil
}

// segHeaderMax is the largest a segment's header can be.
func (t *tapeCore) segHeaderMax() int {
	h := t.header
	h.Segment, h.Segments, h.PrevSegment = recMaxEvents, recMaxEvents, strings.Repeat("0", 64)
	big, err := json.Marshal(h)
	if err != nil {
		return t.segMax
	}
	return len(big)
}

// takeWarn reports, once, that a session recording stopped at a cap.
func (t *Tape) takeWarn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.warn
	t.warn = false
	return w
}

// Truncated reports whether a recording stopped at a cap.
func (t *Tape) Truncated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.truncated
}

// Segments is a finished session recording's files, in order: one, or
// <path>.1 .. <path>.N when it was split.
func (t *Tape) Segments() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.segs
}

// see adds secret texts to the run's set. It only grows: a secret later
// unset or reassigned is still redacted everywhere.
func (t *Tape) see(texts []string) {
	if t.replay {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range texts {
		t.secrets[s] = true
	}
}

// add holds one event, within the size caps.
func (t *Tape) add(p pending) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return
	}
	n := int64(64 + len(p.value))
	if p.v != nil {
		n += int64(len(p.v.Path) + len(p.v.Body))
		for _, a := range p.v.Argv {
			n += int64(len(a))
		}
	}
	n += int64(len(p.res.Bytes))
	for _, r := range p.res.Rows {
		for k, v := range r {
			n += int64(len(k) + len(v))
		}
	}
	if t.truncated {
		return
	}
	t.size += n
	maxEvents := recMaxEvents
	if t.segMax > 0 {
		// Room for the truncated event, so a truncated recording still
		// loads under the event cap.
		maxEvents--
	}
	if len(t.pend) >= maxEvents || t.size > int64(recMaxTotal) {
		if t.segMax > 0 {
			// A session's verbs never stop for its recording: the
			// recording stops, and is written marked truncated.
			t.truncated, t.warn = true, true
			return
		}
		t.err = &TapeError{"record_too_large", len(t.pend) + 1}
		return
	}
	p.line = t.line()
	if t.branch != "" {
		t.bseq++
		p.branch, p.bseq = t.branch, t.bseq
	}
	t.pend = append(t.pend, p)
}

// secretMinRunes is the shortest secret text redacted where it appears
// inside other text. A shorter one is redacted only when a whole field
// equals it (ignoring one trailing newline): matching "ok" or "1" inside
// every response would redact most of a recording and break its replay,
// and a secret that short is guessable anyway. Four keeps a four-digit
// PIN or a short word caught inside text.
const secretMinRunes = 4

// holdsAny reports whether field s holds any secret text: as a substring
// for a text of secretMinRunes or more, or as the whole field for a
// shorter one. When s has a CR or LF it is checked once more with every
// CR and LF removed from it and from the secrets, so a secret split across
// lines ("hunter2\n-long") is caught. That one extra pass can also join
// the ends of two unrelated lines into a secret's text: it over-redacts
// (or marks a render secret), never leaks. Recording, provenance, and the
// template_file render scan all use this.
func holdsAny(s string, secrets []string) bool {
	if holdsAnyLine(s, secrets) {
		return true
	}
	if !strings.ContainsAny(s, "\r\n") {
		return false
	}
	joined := make([]string, len(secrets))
	for i, sec := range secrets {
		joined[i] = crlf.Replace(sec)
	}
	return holdsAnyLine(crlf.Replace(s), joined)
}

var crlf = strings.NewReplacer("\r", "", "\n", "")

func holdsAnyLine(s string, secrets []string) bool {
	whole := strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
	for _, sec := range secrets {
		switch {
		case sec == "":
		case utf8.RuneCountInString(sec) >= secretMinRunes:
			if strings.Contains(s, sec) {
				return true
			}
		case s == sec || whole == sec:
			return true
		}
	}
	return false
}

func (t *Tape) diverge(seq int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.divergeLocked(seq)
}

// divergeHere diverges at the event this view would use next.
func (t *Tape) divergeHere() {
	t.mu.Lock()
	defer t.mu.Unlock()
	seq := len(t.events) + 1
	if list, u := t.by[t.branch], t.used[t.branch]; u < len(list) {
		seq = t.events[list[u]].Seq
	}
	t.divergeLocked(seq)
}

func (t *Tape) divergeLocked(seq int) {
	if t.err == nil {
		t.diverged = seq
		t.err = &TapeError{"replay_diverged", seq}
	}
}

// next takes this view's next event (replay mode) if it is kind, else
// diverges. A branch's events must also be in its own order.
func (t *Tape) next(kind string) (recEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return recEvent{}, false
	}
	list, u := t.by[t.branch], t.used[t.branch]
	if t.truncAt != 0 && (u >= len(list) || t.events[list[u]].Kind == "truncated") {
		// The recording stopped at a cap; the run went past it.
		t.err = &TapeError{"replay_truncated", t.truncAt}
		return recEvent{}, false
	}
	if u >= len(list) {
		t.divergeLocked(len(t.events) + 1)
		return recEvent{}, false
	}
	ev := t.events[list[u]]
	if ev.Kind != kind || (t.branch != "" && ev.BSeq != u+1) {
		t.divergeLocked(ev.Seq)
		return recEvent{}, false
	}
	t.used[t.branch] = u + 1
	return ev, true
}

// reqOf is the recorded form of a request: argv, path, and a body hash,
// or "***" and lengths when it is tainted or holds a secret's text.
func reqOf(v parent.Verb, tainted bool, secrets []string) *recReq {
	holds := func(s string) bool { return holdsAny(s, secrets) }
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
		return &recReq{Key: v.Key, Mode: v.Mode, Redacted: true, Argv: []string{redacted3}, ArgvLen: n, PathLen: len(v.Path), BodyLen: len(v.Body)}
	}
	r := &recReq{Key: v.Key, Mode: v.Mode, Argv: append([]string(nil), v.Argv...), Path: v.Path}
	if v.Body != nil {
		r.BodySHA = sha(v.Body)
		r.BodyLen = len(v.Body)
	}
	return r
}

// respOf is the recorded form of a result, "***" and its length when it
// holds a secret's text.
func respOf(res parent.Result, err error, secrets []string, tainted bool) *recResp {
	r := &recResp{Kind: "bytes", Exit: res.Exit}
	if res.Kind == parent.KindRows {
		r.Kind = "rows"
	}
	if err != nil {
		r.Error = err.Error()
	}
	fields := append([]string{string(res.Bytes), r.Error}, res.Cols...)
	n := len(res.Bytes) + len(r.Error)
	for _, row := range res.Rows {
		for k, v := range row {
			fields = append(fields, k, v)
			n += len(k) + len(v)
		}
	}
	if !tainted {
		for _, f := range fields {
			tainted = tainted || holdsAny(f, secrets)
		}
	}
	if tainted {
		return &recResp{Kind: r.Kind, Exit: r.Exit, Redacted: true, Len: n}
	}
	r.Cols, r.Rows, r.Bytes = res.Cols, res.Rows, res.Bytes
	return r
}

// reqMatches compares a replayed request with the recorded one. The
// recorder redacts against secrets bound later in the run, which replay
// does not know yet, so a redacted event is compared on what it kept:
// the key and every length (the kind is checked by next). An unredacted
// event must match exactly: argv, path, and body hash.
func reqMatches(rec *recReq, v parent.Verb) bool {
	if rec == nil {
		return false
	}
	want := reqOf(v, rec.Redacted, nil)
	a, errA := json.Marshal(rec)
	b, errB := json.Marshal(want)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// call is one door call under the tape: recorded through p, or answered
// from the recording.
func (t *Tape) call(p parent.Parent, v parent.Verb, tainted bool, secrets []string) (parent.Result, error) {
	if !t.replay {
		res, err := p.Do(v)
		vv := v
		t.add(pending{kind: "call", v: &vv, tainted: tainted, res: res, err: err, hasResp: true})
		return res, err
	}
	ev, ok := t.next("call")
	if !ok {
		return parent.Result{}, t.Err()
	}
	if !reqMatches(ev.Req, v) {
		t.diverge(ev.Seq)
		return parent.Result{}, t.Err()
	}
	r := ev.Resp
	if r == nil {
		t.diverge(ev.Seq)
		return parent.Result{}, t.Err()
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
	if !t.replay {
		vv := v
		t.add(pending{kind: "plan", v: &vv, tainted: tainted})
		return
	}
	if ev, ok := t.next("plan"); ok && !reqMatches(ev.Req, v) {
		t.diverge(ev.Seq)
	}
}

// value records a reading (now, sleep, uuid) from read, or returns the
// recorded one on replay without calling read.
func (t *Tape) value(kind string, read func() string) string {
	if !t.replay {
		v := read()
		t.add(pending{kind: kind, value: v})
		return v
	}
	ev, ok := t.next(kind)
	if !ok || ev.Resp == nil {
		return ""
	}
	return ev.Resp.Value
}

// module records a loaded wasm module's hash in the header, or, on
// replay, checks the header names the same module with the same hash.
func (t *Tape) module(name, hash string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.replay {
		t.header.Modules[name] = hash
		return nil
	}
	if t.header.Modules[name] != hash {
		if t.err == nil {
			t.err = &TapeError{"replay_module", -1}
		}
		return t.err
	}
	return nil
}

// fault records that a secret ended the line here, or checks it on replay.
func (t *Tape) fault() {
	if !t.replay {
		t.add(pending{kind: "fault"})
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
		m.tape.divergeHere()
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
	if m.tape.Err() != nil {
		m.ctl = ctlHalt
	}
}

// sleepFor sleeps d in steps of at most 100ms, stopping early when stop says
// so (a hangup, a cancelled parallel branch), so either is seen promptly. A
// recording keeps one sleep event for the whole wait, not one per step, so a
// long await stays within the tape caps; a replay does not sleep.
func (m *machine) sleepFor(d time.Duration, stop func() bool) {
	total := d
	run := func() {
		for d > 0 && !stop() {
			step := d
			if step > 100*time.Millisecond {
				step = 100 * time.Millisecond
			}
			sleepFn(step)
			d -= step
		}
	}
	if m.tape == nil {
		run()
		return
	}
	m.tape.value("sleep", func() string {
		run()
		return total.String()
	})
	if m.tape.Err() != nil {
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
		if err := m.tape.Err(); err != nil {
			m.ctl = ctlHalt
			return "", err
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

// secretTexts are the text of every bound secret, for redaction: the
// whole value and each leaf value in it (not map keys), so a response
// holding one element of a secret list is caught too.
func (m *machine) secretTexts() []string {
	var out []string
	var leaves func(d any)
	leaves = func(d any) {
		switch x := d.(type) {
		case []any:
			for _, it := range x {
				leaves(it)
			}
		case *omap:
			// Keys are structure, not secret values.
			for _, k := range x.keys {
				leaves(x.m[k])
			}
		case nil:
		default:
			if s := textOf(x); s != "" {
				out = append(out, s)
			}
		}
	}
	// Texts the session marked secret and no longer binds (a par
	// branch's, or a secret reassigned) are redacted too.
	for t := range m.st.secretText {
		out = append(out, t)
	}
	for _, v := range m.vars {
		if v.kind == valData {
			if inner, ok := unsecret(v.d); ok {
				if s := textOf(inner); s != "" {
					out = append(out, s)
				}
				leaves(inner)
			}
		}
	}
	return out
}

// tapeDo is machine.do's call through the tape.
func (m *machine) tapeDo(v parent.Verb) (parent.Result, error) {
	res, err := m.tape.call(m.p, v, m.sawSecret || m.secretPipe || v.Mode != "", m.secretTexts())
	if terr := m.tape.Err(); terr != nil {
		m.ctl = ctlHalt
		return parent.Result{}, terr
	}
	return res, err
}
