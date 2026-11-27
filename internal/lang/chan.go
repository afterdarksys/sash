package lang

// Channels scoped to one par N { } block
// (docs/plans/2026-11-27-sash-par-channels.md sections 1 to 4).
//
// Threats: a channel outliving its par (a channel is a name in the par
// text, never a value; its table is a local of runPar, and every branch
// that can block on it is joined, or woken by the first cancel, before
// runPar returns); a producer outrunning workers or huge values (buffer 1
// to 64, 8 MiB per value, 32 MiB per par, sends past loopmax halt); a
// deadlock hanging the line (exact detection under one lock, on every
// block and branch end, never a timer); a dropped item being a dropped
// action (values left at the join fail the par); a received value sharing
// a map with its sender (every value is deep-copied at send); and an
// origin id meaning another origin in the receiving branch (ids never
// cross: a value's origins cross as records, each with whether its hash is
// secret, and the receiver interns them into its own table; an overflowed
// provenance crosses as overflow, so fromAnswer still fails closed; a
// secretVal crosses inside the datum, and the receiver marks its texts and
// sinks them for the session); and a replay accepting another
// interleaving (every completed op is a chan event with one cseq per par
// and the value's hash, or redacted for a secret; replay lets an op
// complete only when the next unused event by cseq names it, and anything
// else, a hash that differs, or no branch able to go on, is
// replay_diverged).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

// msgRecvOnly is the one place recv may stand.
const msgRecvOnly = "recv only as for x in recv c"

// chanOpsIn calls fn for every send and recv loop in p, nested bodies
// included, fn bodies too.
func chanOpsIn(p *Program, fn func(op, name string, s Stmt)) {
	if p == nil {
		return
	}
	for _, s := range p.Stmts {
		switch s := s.(type) {
		case *Send:
			fn("send", s.Chan, s)
		case *For:
			if s.Recv != "" {
				fn("recv", s.Recv, s)
			}
		}
		for _, b := range lintBodies(s) {
			chanOpsIn(b, fn)
		}
	}
}

// chanPlaces is the static channel rules (section 2): no send or recv in
// a fn or a retry, recv only as a for source, each op naming a channel of
// its own par, and each channel with a sender branch and a receiver
// branch, no branch both.
func chanPlaces(prog *Program) error {
	var msg string
	needsEachStage(prog, func(st Stage) {
		if msg == "" && len(st.Argv) > 0 && plain(st.Argv[0]) == "recv" {
			msg = msgRecvOnly
		}
	})
	if msg == "" {
		msg = chanWalk(prog, nil)
	}
	if msg != "" {
		return fmt.Errorf("parse: %s", msg)
	}
	return nil
}

// chanWalk checks p, where chans is the enclosing par's channels (nil
// outside a par with channels).
func chanWalk(p *Program, chans map[string]bool) string {
	if p == nil {
		return ""
	}
	for _, s := range p.Stmts {
		var msg string
		switch s := s.(type) {
		case *Fn:
			chanOpsIn(&s.Body, func(op, _ string, _ Stmt) {
				if msg == "" {
					msg = op + " inside fn"
				}
			})
		case *Retry:
			chanOpsIn(&s.Body, func(op, _ string, _ Stmt) {
				if msg == "" {
					msg = op + " inside retry"
				}
			})
		case *Send:
			if !chans[s.Chan] {
				msg = "send outside its par"
			}
		case *For:
			if s.Recv != "" && !chans[s.Recv] {
				msg = "recv outside its par"
			}
		case *Par:
			if msg = chanWalk(&s.Body, parChans(s)); msg == "" {
				msg = chanSides(s)
			}
			if msg != "" {
				return msg
			}
			continue
		}
		if msg != "" {
			return msg
		}
		for _, b := range lintBodies(s) {
			if msg := chanWalk(b, chans); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func parChans(s *Par) map[string]bool {
	if len(s.Chans) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, c := range s.Chans {
		out[c.Name] = true
	}
	return out
}

// parBranches is a par N { } body's branches: each top-level statement,
// or a branch { } block.
func parBranches(s *Par) []*Program {
	out := make([]*Program, len(s.Body.Stmts))
	for i, st := range s.Body.Stmts {
		if br, ok := st.(*Branch); ok {
			out[i] = &br.Body
			continue
		}
		out[i] = &Program{Stmts: []Stmt{st}, Pos: []int{s.Body.Pos[i]}}
	}
	return out
}

// branchSides is, per channel, whether a branch sends on it and whether
// it receives from it. Its text is the whole truth: no fn and no retry
// holds a channel op.
func branchSides(b *Program) (sends, recvs map[string]bool) {
	sends, recvs = map[string]bool{}, map[string]bool{}
	chanOpsIn(b, func(op, name string, _ Stmt) {
		if op == "send" {
			sends[name] = true
		} else {
			recvs[name] = true
		}
	})
	return sends, recvs
}

// chanSides refuses a channel no branch sends on, none receives from, or
// one branch both sends on and receives from.
func chanSides(s *Par) string {
	if len(s.Chans) == 0 {
		return ""
	}
	sent, recvd := map[string]bool{}, map[string]bool{}
	for _, b := range parBranches(s) {
		sends, recvs := branchSides(b)
		for c := range sends {
			if recvs[c] {
				return "chan " + c + ": one branch sends and receives"
			}
			sent[c] = true
		}
		for c := range recvs {
			recvd[c] = true
		}
	}
	for _, c := range s.Chans {
		if !sent[c.Name] {
			return "chan " + c.Name + ": no sender"
		}
		if !recvd[c.Name] {
			return "chan " + c.Name + ": no receiver"
		}
	}
	return ""
}

// chanParMaxBytes bounds every buffered value of one par together.
const chanParMaxBytes = 32 << 20

var (
	errChanDeadlock  = errors.New("deadlock")
	errChanCancelled = errors.New("cancelled")
	errChanTimeout   = errors.New("timeout")
	// errChanDiverged is a replay that left the recorded channel order;
	// Run reports the tape's error.
	errChanDiverged = errors.New("replay_diverged")
)

// chanItem is one queued value. Its provenance crosses by record, never
// by id: ids index the sender's origins table, which grew apart from the
// receiver's after the fork.
type chanItem struct {
	v    value
	size int
	orig []chanOrigin
	// over is an overflowed provenance, or an id the sender could not
	// name: the receiver keeps the overflow marker.
	over bool
	// enc is the value's JSON, kept only under a tape, for its hash;
	// secret is a value holding a secret, recorded redacted.
	enc    string
	secret bool
}

// chanOrigin is one origin as it crosses: the record, and whether the
// sender hides its hash (a secret produced it).
type chanOrigin struct {
	o      origin
	secret bool
}

// pchan is one channel: its queue, and how many of its sender and
// receiver branches have not ended.
type pchan struct {
	name      string
	buf       int
	q         []chanItem
	senders   int
	receivers int
	sends     int
}

// chanWaiter is a branch blocked on a channel op. A state change that
// makes its op ready marks it woken and counts it running again, under
// the table's lock, before any other branch can block: so a branch woken
// but not yet scheduled is never mistaken for a blocked one.
type chanWaiter struct {
	i     int
	what  string
	ready func() bool
	// plain is the op ready but for the recorded order (replay).
	plain func() bool
	woken bool
}

// chanTable is everything a par with channels shares between its
// branches, under one lock. It is a local of runPar.
type chanTable struct {
	mu      sync.Mutex
	cond    *sync.Cond
	ctx     context.Context
	cancel  context.CancelFunc
	chans   map[string]*pchan
	order   []string
	sides   []map[string]string // per branch: chan -> "send" or "recv"
	bytes   int
	running int // started, not ended, not blocked on a channel
	waiters []*chanWaiter
	dead    string
	// baton: one branch runs at a time (eff 1). holder is -1 when free;
	// want holds the runnable branches, and the lowest takes it.
	baton  bool
	holder int
	want   map[int]bool
	// slots gates door calls when eff >= 2.
	slots chan struct{}
	// tapes is each branch's tape view, when the run is recorded or
	// replayed. Recording numbers every completed op (cseq); replay
	// follows script, the par's chan events in cseq order.
	tapes  []*Tape
	cseq   int
	replay bool
	script []recEvent
	used   []bool
	next   int
	div    bool
}

// chanTableHook, when set (tests), is given each new table.
var chanTableHook func(*chanTable)

func newChanTable(ctx context.Context, cancel context.CancelFunc, s *Par, bodies []*Program, eff int) *chanTable {
	t := &chanTable{ctx: ctx, cancel: cancel, chans: map[string]*pchan{}, running: len(bodies), holder: -1}
	t.cond = sync.NewCond(&t.mu)
	for _, c := range s.Chans {
		t.chans[c.Name] = &pchan{name: c.Name, buf: c.Buf}
		t.order = append(t.order, c.Name)
	}
	for _, b := range bodies {
		side := map[string]string{}
		sends, recvs := branchSides(b)
		for c := range sends {
			side[c] = "send"
			if ch := t.chans[c]; ch != nil {
				ch.senders++
			}
		}
		for c := range recvs {
			side[c] = "recv"
			if ch := t.chans[c]; ch != nil {
				ch.receivers++
			}
		}
		t.sides = append(t.sides, side)
	}
	if eff <= 1 {
		t.baton = true
		t.want = map[int]bool{}
		for i := range bodies {
			t.want[i] = true
		}
	} else {
		t.slots = make(chan struct{}, eff)
	}
	if chanTableHook != nil {
		chanTableHook(t)
	}
	return t
}

// changed wakes every waiter whose op is now ready, or all of them when
// the par is cancelled or deadlocked, then declares a deadlock when no
// branch runs and some branch waits. Under t.mu.
func (t *chanTable) changed() {
	all := t.ctx.Err() != nil || t.dead != "" || t.div
	keep := t.waiters[:0]
	for _, w := range t.waiters {
		if all || w.ready() {
			t.wake(w)
			continue
		}
		keep = append(keep, w)
	}
	t.waiters = keep
	if t.running == 0 && len(t.waiters) > 0 && t.replay && !t.div {
		// No branch can go on. When an op would be ready but for the
		// recorded order, the run left the recording: never a deadlock
		// the recording did not have.
		for _, w := range t.waiters {
			if w.plain() {
				t.diverge(w.i)
				for _, w := range t.waiters {
					t.wake(w)
				}
				t.waiters = nil
				break
			}
		}
	}
	if t.running == 0 && len(t.waiters) > 0 && t.dead == "" && !t.div {
		var ops []string
		ws := append([]*chanWaiter(nil), t.waiters...)
		for a := range ws {
			for b := a + 1; b < len(ws); b++ {
				if ws[b].i < ws[a].i {
					ws[a], ws[b] = ws[b], ws[a]
				}
			}
		}
		for _, w := range ws {
			ops = append(ops, fmt.Sprintf("branch %d %s", w.i+1, w.what))
		}
		t.dead = strings.Join(ops, "; ")
		for _, w := range t.waiters {
			t.wake(w)
		}
		t.waiters = nil
	}
	t.cond.Broadcast()
}

func (t *chanTable) wake(w *chanWaiter) {
	w.woken = true
	t.running++
	if t.baton {
		t.want[w.i] = true
	}
}

// acquire waits for the baton: free, and this the lowest runnable
// branch. Under t.mu.
func (t *chanTable) acquire(i int) {
	if !t.baton {
		return
	}
	t.want[i] = true
	for {
		if t.holder == -1 {
			low := -1
			for j := range t.want {
				if low < 0 || j < low {
					low = j
				}
			}
			if low == i {
				break
			}
		}
		t.cond.Wait()
	}
	t.holder = i
	delete(t.want, i)
}

func (t *chanTable) release(i int) {
	if t.baton && t.holder == i {
		t.holder = -1
		t.cond.Broadcast()
	}
}

// start runs before branch i's body.
func (t *chanTable) start(i int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acquire(i)
}

// end runs when branch i ends, for any reason: each channel it sent on
// closes when it was the last sender, and each it received from loses a
// receiver. A failed branch cancels the par first, so a sender it leaves
// without a receiver wakes as cancelled, as the first failure decides.
// Closes happen in declaration order, each a chan event under a tape; on
// replay a close before any cancel waits for its recorded turn.
func (t *chanTable) end(m *machine, i int, failed bool) {
	if failed {
		t.cancel()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.order {
		ch := t.chans[c]
		switch t.sides[i][c] {
		case "send":
			// late is a close after a cancel, a deadline, or a
			// divergence: it still happens (a sender that ended always
			// closes), unordered.
			late := t.ctx.Err() != nil || t.div
			if ch.senders == 1 && t.replay && !late {
				turn := func() bool { return t.turn(i, "close", c) }
				if err := t.wait(m, "close "+c, turn, func() bool { return true }); err != nil {
					late = true
				}
			}
			ch.senders--
			if ch.senders == 0 {
				t.took(i, "close", c, chanItem{}, late)
			}
		case "recv":
			ch.receivers--
		}
	}
	t.running--
	t.release(i)
	t.changed()
}

// turn reports whether branch i's op on c may complete now: always, but
// on replay only when the next unused recorded event names it.
func (t *chanTable) turn(i int, op, c string) bool {
	if !t.replay {
		return true
	}
	for t.next < len(t.script) && t.used[t.next] {
		t.next++
	}
	if t.next >= len(t.script) {
		return false
	}
	ev := t.script[t.next]
	return ev.Branch == t.tapes[i].branch && ev.Op == op && ev.Chan == c
}

// took is a completed op of branch i on c: on a recording, the next cseq
// and a chan event; on replay, the recorded event it matched, taken from
// the par's order and the branch's own stream, its hash checked. A late
// close may match out of order. false is a divergence. Under t.mu.
func (t *chanTable) took(i int, op, c string, it chanItem, late bool) bool {
	if t.tapes == nil {
		return true
	}
	if !t.replay {
		t.cseq++
		t.tapes[i].chanEvent(op, c, t.cseq, it.enc, it.secret)
		return true
	}
	if t.div {
		return false
	}
	k := -1
	if t.turn(i, op, c) {
		k = t.next
	} else if op == "close" && late {
		// Nothing else completes after a cancel, so a late close is not
		// ordered against other ops.
		for j, ev := range t.script {
			if !t.used[j] && ev.Branch == t.tapes[i].branch && ev.Op == op && ev.Chan == c {
				k = j
				break
			}
		}
	}
	if k < 0 {
		t.diverge(i)
		return false
	}
	t.used[k] = true
	rec := t.script[k]
	ev, ok := t.tapes[i].next("chan")
	switch {
	case !ok:
		t.div = true
		t.cancel()
		return false
	case ev.Seq != rec.Seq:
		t.tapes[i].diverge(rec.Seq)
	case op == "close":
		return true
	case rec.Redacted:
		// The recorder redacts against secrets bound later in the run,
		// which replay does not know yet: a redacted event keeps no hash.
		return true
	case !it.secret && rec.Hash == sha([]byte(it.enc)):
		return true
	default:
		t.tapes[i].diverge(rec.Seq)
	}
	t.div = true
	t.cancel()
	return false
}

// diverge ends a replay that cannot go on: at the next unused recorded
// channel event, or branch i's next event when none is left. Under t.mu.
func (t *chanTable) diverge(i int) {
	t.turn(i, "", "")
	if t.next < len(t.script) {
		t.tapes[i].diverge(t.script[t.next].Seq)
	} else {
		t.tapes[i].divergeHere()
	}
	t.div = true
	t.cancel()
}

// cancelled wakes every waiter when the par's context ends.
func (t *chanTable) cancelled() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.changed()
}

// wait blocks branch i until ready holds, the par is cancelled or
// deadlocked, or the deadline passes. Under t.mu.
func (t *chanTable) wait(m *machine, what string, ready, plain func() bool) error {
	i := m.pb.idx
	for {
		switch {
		case t.div:
			return errChanDiverged
		case t.dead != "":
			return errChanDeadlock
		case t.ctx.Err() != nil:
			return errChanCancelled
		case m.pastDeadline():
			return errChanTimeout
		case ready():
			return nil
		}
		w := &chanWaiter{i: i, what: what, ready: ready, plain: plain}
		t.waiters = append(t.waiters, w)
		t.running--
		var timer *time.Timer
		if !m.deadline.IsZero() {
			timer = time.AfterFunc(time.Until(m.deadline)+time.Millisecond, func() {
				t.mu.Lock()
				defer t.mu.Unlock()
				if !w.woken {
					for k, o := range t.waiters {
						if o == w {
							t.waiters = append(t.waiters[:k], t.waiters[k+1:]...)
							break
						}
					}
					t.wake(w)
					t.cond.Broadcast()
				}
			})
		}
		if t.baton && t.holder == i {
			t.holder = -1
		}
		t.changed()
		for !w.woken {
			t.cond.Wait()
		}
		if timer != nil {
			timer.Stop()
		}
		t.acquire(i)
	}
}

// chanOf is the channel a branch's op names, with its side checked again
// at run time.
func (m *machine) chanOf(op, name string) (*chanTable, *pchan, bool) {
	if m.pb == nil || m.pb.ch == nil {
		return nil, nil, false
	}
	t := m.pb.ch
	ch := t.chans[name]
	if ch == nil || t.sides[m.pb.idx][name] != op {
		return nil, nil, false
	}
	return t, ch, true
}

// chanFail ends a channel op that did not complete.
func (m *machine) chanFail(err error) (int, bool) {
	switch err {
	case errChanDeadlock:
		// A halt: try does not catch it, so retry cannot spin on it.
		m.err.WriteString("deadlock\n")
		m.ctl = ctlHalt
		m.haltStatus = exitType
	case errChanCancelled:
		m.pb.stopped = true
		m.ctl = ctlHalt
	case errChanDiverged:
		m.ctl = ctlHalt
		m.haltStatus = exitType
	default:
		m.err.WriteString("timeout\n")
	}
	return exitType, true
}

func (m *machine) runSend(s *Send) (int, bool) {
	t, ch, ok := m.chanOf("send", s.Chan)
	if !ok {
		m.err.WriteString("type send: send outside its par\n")
		return exitType, true
	}
	// A lone $name keeps its kind, so bytes are seen as bytes.
	var v value
	if w := s.Word; len(w.Parts) == 1 && w.Parts[0].Exp {
		v = m.vars[w.Parts[0].Lit]
	}
	if v.kind != valBytes {
		m.sawSecret = false
		var err error
		if v, err = m.wordValue(s.Word); err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
	}
	if v.kind == valBytes {
		m.err.WriteString("type send: bytes cannot cross a channel; lift with lines or rows\n")
		return exitType, true
	}
	if m.sawSecret {
		// A secret spelled into a word is plain text: it would cross as
		// text nobody knows is secret. As let refuses it.
		m.err.WriteString("a secret in a word: build it inside [ ]\n")
		return exitType, true
	}
	cp, size, ok := chanCopy(v)
	if !ok || size > dataMaxBytes {
		m.err.WriteString("send " + s.Chan + ": value over 8 MiB\n")
		return exitType, true
	}
	it := chanItem{v: cp, size: size}
	for _, id := range unionProv(v.prov, m.prov) {
		if id == provOverflow || int(id) >= len(m.st.origins) {
			it.over = true
			continue
		}
		it.orig = append(it.orig, chanOrigin{o: m.st.origins[id], secret: m.st.hideHash(id)})
	}
	if t.tapes != nil {
		var b strings.Builder
		writeJSON(&b, toDatum(cp))
		it.enc, it.secret = b.String(), holdsSecret(cp.d, 0)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ch.sends++; ch.sends > m.st.loopMax {
		m.err.WriteString("loop max\n")
		m.ctl = ctlHalt
		return exitType, true
	}
	i := m.pb.idx
	ready := func() bool { return (len(ch.q) < ch.buf && t.turn(i, "send", s.Chan)) || ch.receivers == 0 }
	plain := func() bool { return len(ch.q) < ch.buf || ch.receivers == 0 }
	if err := t.wait(m, "send "+s.Chan, ready, plain); err != nil {
		return m.chanFail(err)
	}
	if ch.receivers == 0 {
		m.err.WriteString("send " + s.Chan + ": no receiver left\n")
		return 1, true
	}
	if t.bytes+size > chanParMaxBytes {
		m.err.WriteString("send " + s.Chan + ": buffers over 32 MiB\n")
		return exitType, true
	}
	if !t.took(i, "send", s.Chan, it, false) {
		t.changed()
		return m.chanFail(errChanDiverged)
	}
	ch.q = append(ch.q, it)
	t.bytes += size
	t.changed()
	return 0, false
}

// chanCopy deep-copies v, so the receiver shares no map or slice with the
// sender, and sizes it: the bytes of its texts and keys, 8 per scalar.
// ok is false past dataMaxBytes or for a kind that cannot cross.
func chanCopy(v value) (value, int, bool) {
	size := 0
	// Provenance ids do not cross: they index the sender's origins table
	// (runSend carries the records beside the copy).
	out := value{kind: v.kind, s: v.s, gcol: v.gcol}
	switch v.kind {
	case valStr:
		size = len(v.s)
	case valRow:
		out.row, size = copyRow(v.row)
	case valRows:
		out.cols = append([]string(nil), v.cols...)
		out.rows = make([]parent.Row, len(v.rows))
		for i, r := range v.rows {
			var n int
			out.rows[i], n = copyRow(r)
			if size += n; size > dataMaxBytes {
				return value{}, size, false
			}
		}
	case valData:
		d, n, ok := copyDatum(v.d, 0, 0)
		if !ok {
			return value{}, n, false
		}
		out.d, size = d, n
	default:
		return value{}, 0, false
	}
	return out, size, size <= dataMaxBytes
}

// holdsSecret reports a secret anywhere in d.
func holdsSecret(d any, depth int) bool {
	if depth > 128 {
		return true
	}
	switch d := d.(type) {
	case secretVal:
		return true
	case []any:
		for _, e := range d {
			if holdsSecret(e, depth+1) {
				return true
			}
		}
	case *omap:
		for _, k := range d.keys {
			if holdsSecret(d.m[k], depth+1) {
				return true
			}
		}
	}
	return false
}

func copyRow(r parent.Row) (parent.Row, int) {
	out := make(parent.Row, len(r))
	n := 0
	for k, v := range r {
		out[k] = v
		n += len(k) + len(v)
	}
	return out, n
}

func copyDatum(d any, size, depth int) (any, int, bool) {
	if size > dataMaxBytes || depth > 128 {
		return nil, size, false
	}
	switch d := d.(type) {
	case nil, bool, int64, float64:
		return d, size + 8, true
	case string:
		return d, size + len(d), true
	case []any:
		out := make([]any, len(d))
		for i, e := range d {
			var ok bool
			if out[i], size, ok = copyDatum(e, size, depth+1); !ok {
				return nil, size, false
			}
		}
		return out, size, true
	case secretVal:
		// The wrapper is part of the datum: a secret stays a secret.
		in, n, ok := copyDatum(d.d, size, depth+1)
		if !ok {
			return nil, n, false
		}
		return secretVal{in}, n, true
	case *omap:
		out := &omap{keys: append([]string(nil), d.keys...), m: make(map[string]any, len(d.m))}
		for _, k := range d.keys {
			var ok bool
			if out.m[k], size, ok = copyDatum(d.m[k], size+len(k), depth+1); !ok {
				return nil, size, false
			}
		}
		return out, size, true
	}
	return nil, size, false
}

// runRecv is for x in recv c { }: each value in send order on c, until c
// is closed (every sender branch ended) and empty.
func (m *machine) runRecv(s *For) (int, bool) {
	t, ch, ok := m.chanOf("recv", s.Recv)
	if !ok {
		m.err.WriteString("type recv: recv outside its par\n")
		return exitType, true
	}
	if !m.bindable(s.Name) {
		return exitType, true
	}
	saved := m.saveVars([]string{s.Name})
	defer m.restoreVars(saved)
	status, iter := 0, 0
	for {
		t.mu.Lock()
		i := m.pb.idx
		ready := func() bool {
			return (len(ch.q) > 0 && t.turn(i, "recv", s.Recv)) || (ch.senders == 0 && len(ch.q) == 0)
		}
		plain := func() bool { return len(ch.q) > 0 || ch.senders == 0 }
		err := t.wait(m, "recv "+s.Recv, ready, plain)
		var it chanItem
		got := false
		if err == nil && len(ch.q) > 0 {
			it, ch.q, got = ch.q[0], ch.q[1:], true
			t.bytes -= it.size
			if !t.took(i, "recv", s.Recv, it, false) {
				err = errChanDiverged
			}
			t.changed()
		}
		t.mu.Unlock()
		if err != nil {
			return m.chanFail(err)
		}
		if !got {
			return status, false
		}
		v := it.v
		v.prov = m.internOrigins(it)
		m.vars[s.Name] = m.withProv(v)
		st, stop, brk := m.loopBody(&s.Body, &iter)
		status = st
		if stop {
			return status, true
		}
		if brk {
			return status, false
		}
	}
}

// internOrigins adds a received value's origins to this branch's own
// table and returns their ids here: the same record gets the same id
// again, an origin the sender hides stays hidden (its id, and its hash
// for every later response), and an overflow stays overflow. Past the
// table's cap addOrigin hands out overflow, which fails closed too.
func (m *machine) internOrigins(it chanItem) []uint32 {
	var prov []uint32
	marked := false
	for _, c := range it.orig {
		id, seen := m.chanOrigins[c.o]
		if !seen {
			id = m.st.addOrigin(c.o)
			if id != provOverflow {
				if m.chanOrigins == nil {
					m.chanOrigins = map[origin]uint32{}
				}
				m.chanOrigins[c.o] = id
			}
		}
		prov = addProv(prov, id)
		if !c.secret {
			continue
		}
		if m.st.secretOrigin == nil {
			m.st.secretOrigin, m.st.secretHash, m.st.secretText = map[uint32]bool{}, map[[32]byte]bool{}, map[string]bool{}
		}
		if id != provOverflow {
			m.st.secretOrigin[id] = true
		}
		if !c.o.nohash {
			m.st.secretHash[c.o.hash] = true
		}
		marked = true
	}
	if it.over {
		prov = addProv(prov, provOverflow)
	}
	if marked {
		m.sinkSecrets()
	}
	return prov
}

// unread is a line per channel with values left at the join.
func (t *chanTable) unread() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, c := range t.order {
		if n := len(t.chans[c].q); n > 0 {
			out = append(out, fmt.Sprintf("par: chan %s: %d unread", c, n))
		}
	}
	return out
}

// doorSlot takes one of the par's door-call slots (eff >= 2), or reports
// a cancel; the caller releases it after the parent call.
func (m *machine) doorSlot() (func(), bool) {
	if m.pb == nil || m.pb.ch == nil || m.pb.ch.slots == nil {
		return func() {}, true
	}
	select {
	case m.pb.ch.slots <- struct{}{}:
		return func() { <-m.pb.ch.slots }, true
	case <-m.pb.ctx.Done():
		return nil, false
	}
}
