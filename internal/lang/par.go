package lang

// par N { } and par N for x in src { } (v5 section 8, and
// docs/plans/2026-11-14-sash-mux-pipe.md section 4).
//
// Threats: a branch writing another branch's or the session's state (each
// branch runs on its own machine over its own State copy, and bindings are
// discarded at the join); a race on a shared count letting branches pass
// verbmax (one atomic counter, compare-and-swap); output interleaving or a
// secret crossing between branches (each branch buffers its own output,
// already redacted by render, emitted in branch order after the join); a
// failure leaving siblings running mutations (the first failure cancels
// the rest at statement and verb boundaries, and a queued request is
// cancelled at the agent; a started verb finishes and, if it mutated, is
// reported); a hung verb holding the line (a second interrupt stops the
// wait and names every mutating verb still outstanding as outcome
// unknown); and nested or detached work (no &, par inside par refused,
// every branch joined before par returns, except after a second
// interrupt, when the agent audit holds the real outcome). Under confirm
// par runs one branch at a time and sends no hello, so prompts arrive in
// source order. Replay runs one branch at a time from each branch's own
// recorded stream.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/afterdarksys/sash/internal/parent"
)

// errParCancelled is a door call a cancelled branch did not make.
var errParCancelled = errors.New("par cancelled")

// exitInterrupted is par's status when an interrupt cancelled it.
const exitInterrupted = 130

// parBranch is what one branch machine shares with its par.
type parBranch struct {
	idx int
	ctx context.Context
	// stopped is set (by the branch's own goroutine) when it stopped at a
	// boundary because the par was cancelled.
	stopped bool
	mu      sync.Mutex
	// inflight is the door call under way, for the second interrupt.
	inflight *parent.Verb
	late     []string
	// done is set by the branch's goroutine when it ends; abandoned by
	// par when a second interrupt left it running. Under mu, exactly one
	// side handles the finished branch: par if done was set first, else
	// the goroutine, through the State's late queue.
	done      bool
	abandoned bool
	// sink is the par's shared secret sink (below).
	sink *secretSink
}

// secretSink is where a par's branches put every secret text and hash
// they mark, at the moment they mark it. It is the one structure branches
// and their par share for secrets, under its own lock; par alone merges it
// into the session State, at the join and at a second interrupt, so a
// secret a branch bound before it was abandoned is known to the session
// before its next statement, never only when the branch ends.
type secretSink struct {
	mu    sync.Mutex
	texts map[string]bool
	hash  map[[32]byte]bool
}

func (s *secretSink) add(st *State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t := range st.secretText {
		s.texts[t] = true
	}
	for h := range st.secretHash {
		s.hash[h] = true
	}
}

// merge copies the sink into m's State. Only the par's own machine calls
// it.
func (m *machine) mergeSink(s *secretSink) {
	b := &State{secretText: map[string]bool{}, secretHash: map[[32]byte]bool{}}
	s.mu.Lock()
	for t := range s.texts {
		b.secretText[t] = true
	}
	for h := range s.hash {
		b.secretHash[h] = true
	}
	s.mu.Unlock()
	m.mergeSecrets(b)
}

// sinkSecrets hands a branch's secret sets to its par's sink. It runs on
// the branch's goroutine, which alone writes the branch State it reads.
func (m *machine) sinkSecrets() {
	if m.pb != nil && m.pb.sink != nil {
		m.pb.sink.add(m.st)
	}
}

// lateQueue holds what branches left running by a second interrupt
// reported when they ended: their secret texts and hashes, and their
// late outcome lines. The branch goroutine only appends, under mu; the
// session's own machine drains it, under mu, at the start of each top-level
// statement of a later run, and merges into State maps it alone touches.
// No other goroutine ever writes the session State, so the next line's
// evaluation never races a late branch.
type lateQueue struct {
	mu    sync.Mutex
	texts []string
	hash  [][32]byte
	lines []string
}

func (q *lateQueue) push(b *State, lines []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for t := range b.secretText {
		q.texts = append(q.texts, t)
	}
	for h := range b.secretHash {
		q.hash = append(q.hash, h)
	}
	q.lines = append(q.lines, lines...)
}

// drainLate merges what late branches left into this machine's State and
// prints their late outcomes. Only the machine of a Run calls it.
func (m *machine) drainLate() {
	q := m.st.late
	if q == nil {
		return
	}
	q.mu.Lock()
	texts, hashes, lines := q.texts, q.hash, q.lines
	q.texts, q.hash, q.lines = nil, nil, nil
	q.mu.Unlock()
	if len(texts) == 0 && len(hashes) == 0 && len(lines) == 0 {
		return
	}
	b := &State{secretText: map[string]bool{}, secretHash: map[[32]byte]bool{}}
	for _, t := range texts {
		b.secretText[t] = true
	}
	for _, h := range hashes {
		b.secretHash[h] = true
	}
	m.mergeSecrets(b)
	for _, l := range lines {
		m.err.WriteString("late: " + l + "\n")
	}
}

func (pb *parBranch) begin(v parent.Verb) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.inflight = &v
}

// end clears the call and, when the par was cancelled while a mutating
// verb ran, records that it finished anyway.
func (pb *parBranch) end(v parent.Verb, res parent.Result, err error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.inflight = nil
	if pb.ctx.Err() == nil || !mutating[v.Key] || errors.Is(err, parent.ErrCancelled) {
		return
	}
	what := fmt.Sprintf("(exit %d)", res.Exit)
	if err != nil {
		what = "(" + needsCell(err.Error()) + ")"
	}
	pb.late = append(pb.late, fmt.Sprintf("branch %d: %s finished after cancel %s", pb.idx+1, verbLabel(v), what))
}

func (pb *parBranch) outstanding() *parent.Verb {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return pb.inflight
}

func (pb *parBranch) lateLines() []string {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return append([]string(nil), pb.late...)
}

// verbLabel is "<key> <target>": the path, or the unit, package, or
// sysctl name, through needsCell.
func verbLabel(v parent.Verb) string {
	target := v.Path
	if target == "" && len(v.Argv) > 2 && (v.Argv[0] == "unit" || v.Argv[0] == "package" || v.Argv[0] == "sysctl") {
		target = v.Argv[2]
	}
	if target == "" {
		return needsCell(v.Key)
	}
	return needsCell(v.Key) + " " + needsCell(target)
}

// ctxParent runs a branch's door calls under its par's context, so a
// cancel reaches the agent as a cancel frame.
type ctxParent struct {
	ctx context.Context
	p   parent.ContextParent
}

func (c ctxParent) Do(v parent.Verb) (parent.Result, error) { return c.p.DoContext(c.ctx, v) }

// concurrency is a parent that can run verbs at once (proto.Client).
type concurrency interface {
	Concurrency(n int) (int, error)
}

// branchClone is a State a branch may write without touching s: every
// map and slice a run writes is copied.
func (s *State) branchClone() *State {
	c := s.clone()
	c.checks = append([]parent.Row(nil), s.checks...)
	c.origins = append([]origin(nil), s.origins...)
	c.wasmMods = copyMap(s.wasmMods)
	c.fnSrc = copyMap(s.fnSrc)
	c.secretOrigin = copyMap(s.secretOrigin)
	c.secretHash = copyMap(s.secretHash)
	c.secretText = copyMap(s.secretText)
	return c
}

func copyMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (m *machine) branchMachine(ctx context.Context, i int) *machine {
	bm := newMachine(m.g, m.p, m.st.branchClone())
	bm.verbs = m.verbs
	bm.stack = append([]string(nil), m.stack...)
	bm.deadline = m.deadline
	bm.trace, bm.confirm = m.trace, m.confirm
	// A branch sees the same is_tty(); term itself is refused in par.
	bm.term = m.term
	bm.nl, bm.provSrc, bm.at = m.nl, m.provSrc, m.at
	bm.depth = m.depth
	bm.secretPipe = m.secretPipe
	bm.scoped = copyMap(m.scoped)
	bm.pb = &parBranch{idx: i, ctx: ctx}
	if cp, ok := m.p.(parent.ContextParent); ok {
		bm.p = ctxParent{ctx: ctx, p: cp}
	}
	return bm
}

type parResult struct {
	i          int
	st         int
	stop       bool
	ctl        ctl
	halt       int
	cancelled  bool // the par was cancelled when the branch ended
	notStarted bool
}

// failed is a branch failure that escapes: a stop, a non-zero status, or
// an exit that would leave the branch. A branch that ended after the par
// was cancelled is cancelled, not the failure.
func (r parResult) failed() bool {
	return !r.notStarted && !r.cancelled && (r.stop || r.st != 0 || r.ctl != ctlNone)
}

func (m *machine) runPar(s *Par) (int, bool) {
	fail := func(msg string) (int, bool) {
		m.err.WriteString(msg + "\n")
		return exitType, true
	}
	if m.pb != nil {
		return fail("type par: par inside par")
	}
	text, err := m.expand(s.N)
	if err != nil {
		return fail(err.Error())
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > parMax {
		return fail(fmt.Sprintf("usage par: N is 1 to %d", parMax))
	}
	if s.As != "" && !m.bindable(s.As) {
		return exitType, true
	}
	type branch struct {
		body *Program
		item string
		bind func(bm *machine)
	}
	var branches []branch
	if s.Each {
		f := s.Body.Stmts[0].(*For)
		items, isMap, st, stop := m.forItems(f)
		if stop {
			return st, true
		}
		if len(items) > m.st.loopMax {
			m.err.WriteString("loop max\n")
			m.ctl = ctlHalt
			return exitType, true
		}
		if !m.bindable(f.Name, f.Name2) {
			return exitType, true
		}
		for _, it := range items {
			item := it.b
			if isMap || f.Name2 != "" {
				item = it.a
			}
			branches = append(branches, branch{body: &f.Body, item: strings.TrimSuffix(display(item), "\n"),
				bind: func(bm *machine) { bm.bindForItem(f, it, isMap) }})
		}
	} else {
		for i, st := range s.Body.Stmts {
			if br, ok := st.(*Branch); ok {
				branches = append(branches, branch{body: &br.Body})
				continue
			}
			branches = append(branches, branch{body: &Program{Stmts: []Stmt{st}, Pos: []int{s.Body.Pos[i]}}})
		}
	}
	if len(branches) == 0 {
		m.bindParRows(s.As, nil)
		return 0, false
	}
	eff := min(n, len(branches))
	switch {
	case m.st.confirm, m.tape != nil && m.tape.replay:
		// Confirm prompts in source order and sends no hello; replay
		// answers each branch from its own stream.
		eff = 1
	default:
		if c, ok := m.p.(concurrency); ok {
			got, err := c.Concurrency(eff)
			if err != nil {
				return fail("par: " + needsCell(err.Error()))
			}
			eff = got
		} else {
			eff = 1
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tapePar := 0
	if m.tape != nil {
		tapePar = m.tape.newPar()
	}
	machines := make([]*machine, len(branches))
	sink := &secretSink{texts: map[string]bool{}, hash: map[[32]byte]bool{}}
	defer m.mergeSink(sink)
	for i, b := range branches {
		bm := m.branchMachine(ctx, i)
		bm.pb.sink = sink
		if m.tape != nil {
			bm.tape = m.tape.branchView(tapePar, i)
		}
		if b.bind != nil {
			b.bind(bm)
		}
		machines[i] = bm
	}
	results := make(chan parResult, len(branches))
	go func() {
		sem := make(chan struct{}, eff)
		for i := range branches {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				results <- parResult{i: i, notStarted: true}
				continue
			}
			go func(i int) {
				bm := machines[i]
				st, stop := bm.runProg(branches[i].body)
				bm.pb.mu.Lock()
				bm.pb.done = true
				abandoned := bm.pb.abandoned
				late := append([]string(nil), bm.pb.late...)
				bm.pb.mu.Unlock()
				if abandoned && m.st.late != nil {
					m.st.late.push(bm.st, late)
				}
				<-sem
				results <- parResult{i: i, st: st, stop: stop, ctl: bm.ctl, halt: bm.haltStatus, cancelled: ctx.Err() != nil}
			}(i)
		}
	}()

	var intr <-chan struct{}
	if m.interrupts != nil && !m.st.confirm {
		ch, stopIntr := m.interrupts()
		defer stopIntr()
		intr = ch
	}
	out := make([]parResult, len(branches))
	finished := make([]bool, len(branches))
	first, interrupted := -1, false
	for done := 0; done < len(branches); {
		select {
		case r := <-results:
			done++
			out[r.i], finished[r.i] = r, true
			if first < 0 && !interrupted && r.failed() {
				first = r.i
				cancel()
			}
		case <-intr:
			if !interrupted {
				interrupted = true
				cancel()
				continue
			}
			// A second interrupt stops the wait. What still runs is
			// named, never assumed done or undone; the agent audit
			// holds the real outcome. A branch whose goroutine has
			// ended is handled here; any other is marked abandoned, and
			// reports through the State's late queue when it ends.
			done := make([]bool, len(branches))
			var unknown []string
			for i, bm := range machines {
				bm.pb.mu.Lock()
				done[i] = bm.pb.done
				if !done[i] {
					bm.pb.abandoned = true
					if v := bm.pb.inflight; v != nil && mutating[v.Key] {
						unknown = append(unknown, "outcome unknown: "+verbLabel(*v))
					}
				}
				bm.pb.mu.Unlock()
			}
			for i, bm := range machines {
				if !done[i] {
					continue
				}
				m.emitBranch(bm)
				for _, l := range bm.pb.lateLines() {
					m.err.WriteString(l + "\n")
				}
				m.mergeSecrets(bm.st)
			}
			for _, l := range unknown {
				m.err.WriteString(l + "\n")
			}
			m.err.WriteString("par: interrupted\n")
			m.ctl = ctlHalt
			m.haltStatus = exitInterrupted
			return exitInterrupted, true
		}
	}

	rows := make([]parent.Row, len(branches))
	var halt *parResult
	base := len(m.st.checks)
	for i, bm := range machines {
		r := out[i]
		m.emitBranch(bm)
		late := bm.pb.lateLines()
		for _, l := range late {
			m.err.WriteString(l + "\n")
		}
		m.st.checks = append(m.st.checks, bm.st.checks[base:]...)
		m.mergeSecrets(bm.st)
		msg := firstLine(bm.err.String())
		status := strconv.Itoa(r.st)
		switch {
		case r.notStarted:
			status, msg = "", "not started (cancelled)"
		case r.ctl == ctlExit:
			status, msg = strconv.Itoa(exitType), "exit would leave a par branch"
		case r.cancelled && !r.failed() && (r.stop || bm.pb.stopped):
			msg = "cancelled"
		}
		if len(late) > 0 {
			msg = strings.Join(late, "; ")
		}
		rows[i] = parent.Row{"branch": strconv.Itoa(i + 1), "item": branches[i].item, "status": status, "message": msg, "key": failKey(msg)}
		if r.ctl == ctlHalt && !r.cancelled && halt == nil {
			rr := r
			halt = &rr
		}
	}
	m.bindParRows(s.As, rows)
	if halt != nil {
		// A cap or a secret fault in a branch ends the line, as it would
		// outside par.
		m.ctl = ctlHalt
		m.haltStatus = halt.halt
		return exitType, true
	}
	if interrupted {
		m.err.WriteString("par: interrupted\n")
		return exitInterrupted, true
	}
	if first >= 0 {
		r := out[first]
		st := r.st
		if r.ctl == ctlExit {
			m.err.WriteString("type par: exit would leave a par branch\n")
			st = exitType
		}
		if st == 0 {
			st = exitType
		}
		m.err.WriteString(fmt.Sprintf("par: branch %d failed (exit %d)\n", first+1, st))
		return st, true
	}
	return 0, false
}

// emitBranch appends one branch's buffered output, already redacted.
func (m *machine) emitBranch(bm *machine) {
	m.out.WriteString(bm.out.String())
	m.err.WriteString(bm.err.String())
}

// mergeSecrets keeps every secret text and hash a branch bound: those
// sets only grow, so a value a branch marked secret stays hidden. Branch
// origins are not kept, so neither are their ids.
func (m *machine) mergeSecrets(b *State) {
	if len(b.secretText) == 0 && len(b.secretHash) == 0 {
		return
	}
	if m.st.secretOrigin == nil {
		m.st.secretOrigin, m.st.secretHash, m.st.secretText = map[uint32]bool{}, map[[32]byte]bool{}, map[string]bool{}
	}
	for k := range b.secretText {
		m.st.secretText[k] = true
	}
	for k := range b.secretHash {
		m.st.secretHash[k] = true
	}
}

func (m *machine) bindParRows(name string, rows []parent.Row) {
	if name == "" {
		return
	}
	m.vars[name] = m.withProv(value{kind: valRows, rows: rows, cols: []string{"branch", "item", "status", "message", "key"}})
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

// failKey is the verb key a failure message names, as try reports it:
// denied <key>, declined <key>, or refused <key>: ...
func failKey(msg string) string {
	first, _, _ := strings.Cut(msg, "\n")
	switch {
	case strings.HasPrefix(first, "denied "):
		return strings.TrimPrefix(first, "denied ")
	case strings.HasPrefix(first, "declined "):
		return strings.TrimPrefix(first, "declined ")
	}
	if rest, ok := strings.CutPrefix(first, "refused "); ok {
		key, _, _ := strings.Cut(rest, ":")
		return key
	}
	// An atomic block's outcome is reported after the output of what
	// failed inside it.
	for _, l := range strings.Split(msg, "\n") {
		if strings.HasPrefix(l, "refused atomic:") {
			return "atomic"
		}
	}
	return ""
}
