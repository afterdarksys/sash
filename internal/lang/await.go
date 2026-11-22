package lang

import (
	"errors"
	"fmt"
	"time"
)

var errAwaitMutation = errors.New("await: mutation refused")

// await uses Timeout's body so every static walker sees the condition.
func (p *parser) await() (Stmt, error) {
	p.i++
	take := func(want string) (Word, error) {
		if p.peek().kind != kWord || plain(p.peek().word) != want {
			return Word{}, fmt.Errorf("parse: await needs %s", want)
		}
		p.i++
		if p.peek().kind != kWord {
			return Word{}, fmt.Errorf("parse: await %s needs a duration", want)
		}
		w := p.peek().word
		p.i++
		return w, nil
	}
	d, err := take("timeout")
	if err != nil {
		return nil, err
	}
	every, err := take("every")
	if err != nil {
		return nil, err
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	if len(body.Stmts) != 1 {
		return nil, fmt.Errorf("parse: await needs one condition pipeline")
	}
	chain, ok := body.Stmts[0].(*Chain)
	if !ok || len(chain.Pipes) != 1 {
		return nil, fmt.Errorf("parse: await needs one condition pipeline")
	}
	return &Timeout{D: d, Every: &every, Body: *body}, nil
}

func (m *machine) runAwait(t *Timeout) (int, bool) {
	fail := func(msg string) (int, bool) { m.err.WriteString(msg + "\n"); return exitType, true }
	d, err := m.duration(t.D, 24*time.Hour, false)
	if err != nil {
		return fail(err.Error())
	}
	interval, err := m.duration(*t.Every, time.Hour, false)
	if err != nil {
		return fail(err.Error())
	}
	prev, polling, pollDepth := m.deadline, m.polling, m.pollDepth
	end := m.now().Add(d)
	if prev.IsZero() || end.Before(prev) {
		m.deadline = end
	}
	m.polling = true
	m.pollDepth = m.depth
	defer func() { m.deadline, m.polling, m.pollDepth = prev, polling, pollDepth }()
	pipe := t.Body.Stmts[0].(*Chain).Pipes[0]
	for attempt := 0; ; attempt++ {
		if m.ctl != ctlNone {
			return exitType, true
		}
		if m.st.HungUp() {
			m.ctl = ctlHalt
			m.haltStatus = exitHangup
			return fail("hangup")
		}
		if m.pb != nil && m.pb.ctx.Err() != nil {
			m.ctl = ctlHalt
			return fail("await: cancelled")
		}
		if m.pastDeadline() {
			return fail("timeout")
		}
		if attempt >= m.st.loopMax {
			m.ctl = ctlHalt
			return fail("loop max")
		}
		if len(t.Body.Pos) > 0 {
			m.at = t.Body.Pos[0]
		}
		outer := m.out.String()
		m.out.Reset()
		ok, st, stop := m.test(nil, pipe, true)
		m.out.Reset()
		m.out.WriteString(outer)
		if stop {
			return st, true
		}
		// An in-flight parent call cannot be forcibly interrupted. Late success
		// still fails the deadline, and no subsequent call starts.
		if m.pastDeadline() {
			return fail("timeout")
		}
		if ok && st == 0 {
			return 0, false
		}
		wait := interval
		if left := m.deadline.Sub(m.now()); left < wait {
			wait = left
		}
		// Bounded steps so a hangup or parallel cancellation is observed; one
		// recorded sleep per interval.
		m.sleepFor(wait, func() bool {
			return m.st.HungUp() || (m.pb != nil && m.pb.ctx.Err() != nil) || m.ctl != ctlNone
		})
	}
}
