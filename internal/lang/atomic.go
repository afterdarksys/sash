package lang

// atomic [deadline D] { } (docs/plans/2026-11-12-sash-v6-tier3.md section
// 3; the agent half is systemapi.io sahconnect-terminal-protocol.md,
// "sash atomic { }").
//
// Threats: a block that leaves the host half changed, and a block that runs
// unprotected. The file writes inside either all stay or are all restored
// by the agent from its own journal (sash never sees a snapshot). Before
// atomic.begin, every stage the block can reach (its body and every fn it
// calls by literal name, conditions and sources included) is checked:
// unit.restart, package.install, and sysctl.set cannot be undone, and use,
// a nested atomic, and par are refused too, all as exit 2
// atomic_irreversible, before the agent is asked anything. machine.do
// refuses those keys again inside a block (a backstop for unit $op), and
// the agent denies them as well. If begin is refused, the block does not
// run: an agent too old for atomic (denied) is a clear error, never a run
// without the journal. Any failure inside (a failed check, set -e or not,
// a non-zero status, an uncaught error, the block's deadline or a timeout,
// a cap) sends atomic.abort; only a block that ran through sends
// atomic.commit. begin, commit, and abort skip verbmax, the deadline, and
// confirm (only the writes inside ask), so an abort always goes out. A
// restore the agent made on its own (atomic_restored) or a file it had to
// leave as found (atomic_inconsistent) is reported, and try catches it
// with key atomic.

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

// irreversible are the verbs atomic cannot undo.
var irreversible = map[string]bool{"unit.restart": true, "package.install": true, "sysctl.set": true}

// atomicDeadlineMax is the agent's longest transaction.
const atomicDeadlineMax = 600 * time.Second

var errAtomicIrreversible = errors.New("atomic_irreversible")

// atomicTx is the transaction a machine's block holds.
type atomicTx struct {
	checkFailed bool
}

// atomicRefusal names the first thing in body that atomic cannot hold, or
// "". It follows every fn called by literal name, each once.
func (m *machine) atomicRefusal(body *Program) string {
	fns := map[string]*Fn{}
	for k, v := range m.fns {
		fns[k] = v
	}
	needsEachStmt(body, func(s Stmt) {
		if fn, ok := s.(*Fn); ok {
			fns[fn.Name] = fn
		}
	})
	seen := map[*Fn]bool{}
	var walk func(p *Program) string
	walk = func(p *Program) string {
		why := ""
		needsEachStmt(p, func(s Stmt) {
			if why != "" {
				return
			}
			switch s.(type) {
			case *Use:
				why = "use"
			case *Atomic:
				why = "nested atomic"
			case *Par:
				why = "par"
			}
		})
		if why != "" {
			return why
		}
		needsEachStage(p, func(st Stage) {
			if why != "" {
				return
			}
			if k := lintKey(st); irreversible[k] {
				why = k
				return
			}
			if fn := fns[plain(st.Argv[0])]; fn != nil && !seen[fn] {
				seen[fn] = true
				if inner := walk(&fn.Body); inner != "" {
					why = inner + " (in fn " + needsCell(fn.Name) + ")"
				}
			}
		})
		return why
	}
	return walk(body)
}

func (m *machine) runAtomic(s *Atomic) (int, bool) {
	fail := func(st int, msg string) (int, bool) {
		m.err.WriteString(msg + "\n")
		return st, true
	}
	if m.atomic != nil {
		return fail(exitType, "atomic_irreversible: nested atomic")
	}
	if m.pb != nil {
		return fail(exitType, "atomic_irreversible: par")
	}
	argv := []string{"atomic", "begin"}
	var local time.Time
	if s.Deadline != nil {
		d, err := m.duration(*s.Deadline, atomicDeadlineMax, false)
		if err != nil {
			return fail(exitType, "usage atomic deadline: 1s to 600s")
		}
		secs := int(math.Ceil(d.Seconds()))
		argv = append(argv, strconv.Itoa(secs))
		local = m.now().Add(time.Duration(secs) * time.Second)
	}
	if why := m.atomicRefusal(&s.Body); why != "" {
		return fail(exitType, "atomic_irreversible: "+why)
	}
	if !m.g.Has("file.write") {
		return fail(exitDenied, "denied file.write")
	}
	res, err := m.doAtomic(parent.Verb{Key: "atomic.begin", Argv: argv})
	if err != nil {
		if m.ctl == ctlHalt {
			return exitType, true
		}
		if err.Error() == "denied" {
			// Never run the block without the journal.
			return fail(exitType, "agent too old for atomic")
		}
		return fail(exitType, atomicRefused(err))
	}
	prevDeadline := m.deadline
	tighten := func(t time.Time) {
		if !t.IsZero() && (m.deadline.IsZero() || t.Before(m.deadline)) {
			m.deadline = t
		}
	}
	tighten(local)
	if len(res.Rows) == 1 {
		if n, err := strconv.ParseInt(res.Rows[0]["deadline"], 10, 64); err == nil {
			tighten(time.Unix(n, 0))
		}
	}
	tx := &atomicTx{}
	m.atomic = tx
	status, stop := 0, false
	for i, st := range s.Body.Stmts {
		pos := 0
		if i < len(s.Body.Pos) {
			pos = s.Body.Pos[i]
		}
		status, stop = m.runProg(&Program{Stmts: []Stmt{st}, Pos: []int{pos}})
		if stop || status != 0 || tx.checkFailed {
			break
		}
	}
	m.atomic = nil
	m.deadline = prevDeadline
	if !stop && status == 0 && !tx.checkFailed {
		if _, err := m.doAtomic(parent.Verb{Key: "atomic.commit", Argv: []string{"atomic", "commit"}}); err != nil {
			if m.ctl == ctlHalt {
				return exitType, true
			}
			return fail(exitType, atomicRefused(err))
		}
		return 0, false
	}
	m.err.WriteString("atomic: rolled back\n")
	if _, err := m.doAtomic(parent.Verb{Key: "atomic.abort", Argv: []string{"atomic", "abort"}}); err != nil && m.ctl != ctlHalt {
		m.err.WriteString(atomicRefused(err) + "\n")
	}
	if status == 0 {
		status = exitType
	}
	return status, true
}

// atomicRefused is the line for an atomic.* error from the agent: try
// catches it with key atomic.
func atomicRefused(err error) string {
	code := err.Error()
	switch code {
	case "atomic_restored":
		return "refused atomic: atomic_restored (the agent restored every file at the transaction's deadline; the block's writes are undone)"
	case "atomic_inconsistent":
		return "refused atomic: atomic_inconsistent (some files were left as found because another writer changed them; the agent kept its journal for an operator)"
	case "atomic_open":
		return "refused atomic: atomic_open (a transaction is already open, or the agent restored one this session has not yet been told about)"
	case "atomic_busy":
		return "refused atomic: atomic_busy (another verb of the session was in flight)"
	case "atomic_unavailable":
		return "refused atomic: atomic_unavailable (the agent has no journal or recording for this session)"
	case "atomic_none":
		return "refused atomic: atomic_none (the agent holds no transaction)"
	}
	return "refused atomic: " + needsCell(code)
}

// doAtomic sends one atomic.* verb: through the tape when there is one,
// never asking under confirm and never stopped by verbmax or a deadline,
// so an abort always goes out. Plan mode prints it and calls nothing. It
// records no origin: provenance is about values, and these carry none.
func (m *machine) doAtomic(v parent.Verb) (parent.Result, error) {
	if m.st.plan {
		m.err.WriteString("plan: would " + strings.TrimPrefix(v.Key, "atomic.") + " atomic\n")
		if m.tape != nil {
			m.tape.plan(v, false, m.secretTexts())
			if err := m.tape.Err(); err != nil {
				m.ctl = ctlHalt
				return parent.Result{}, err
			}
		}
		return parent.Result{Kind: parent.KindBytes}, nil
	}
	if m.tape != nil {
		return m.tapeDo(v)
	}
	return m.p.Do(v)
}
