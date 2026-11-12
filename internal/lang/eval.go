package lang

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

const (
	exitType    = 2
	exitDenied  = 126
	exitUnknown = 127
)

// fnDepth matches globDepth. A deeper call exits 2 and does not run.
const fnDepth = globDepth

const (
	valStr = iota
	valBytes
	valRows
	valRow
	valData
)

const (
	inNone = iota
	inBytes
	inRows
	inFlex
	inAny
)

// Outcome is the result of one script, one -c string, or one file.
type Outcome struct {
	Status int
	Stdout string
	Stderr string
	Prefix string
	// Exit is true when the line ran exit: the prompt ends the session.
	Exit bool
}

// Loop and verb caps (language v2 contract). set raises them up to capCeil.
const (
	loopMaxDefault = 100000
	verbMaxDefault = 10000
	capCeil        = 10000000
)

// ctl is a jump in flight. It travels on the stop path: a loop takes
// break and continue, a call takes return, Run takes exit. try never
// catches one.
type ctl int

const (
	ctlNone ctl = iota
	ctlBreak
	ctlContinue
	ctlReturn
	ctlExit
	// ctlHalt is a cap (loop max, verb max). It ends the line, and try
	// does not catch it: a cap caught in a loop would multiply.
	ctlHalt
)

// Options carries the session prefix and the history newest-last.
// The process cwd is not this prefix. Trace prints each stage key.
type Options struct {
	Prefix  string
	History []string
	Trace   bool
	// State carries names, functions, and set options from line to line.
	// nil is a fresh State: a script, or sash -c.
	State *State
}

// Run expands src, then parses and executes it. Toolkit names call p.
// Filters stay in process. A denied or mistyped pipeline performs no parent call.
func Run(src string, g grant.Grant, p parent.Parent, opt Options) Outcome {
	expanded, prob := Expand(src, g, p, opt)
	if prob != nil {
		return Outcome{Status: prob.Status, Stderr: prob.Msg + "\n", Prefix: opt.Prefix}
	}
	prog, err := parse(expanded)
	if err != nil {
		return Outcome{Status: exitType, Stderr: safeText(err.Error()) + "\n", Prefix: opt.Prefix}
	}
	m := newMachine(g, p, opt.State)
	m.trace = opt.Trace
	if opt.Prefix != "" {
		m.stack = []string{path.Clean(opt.Prefix)}
	}
	status, _ := m.runProg(prog)
	if m.ctl == ctlHalt {
		status = exitType
	}
	out := Outcome{Status: status, Stdout: m.out.String(), Stderr: safeText(m.err.String()), Prefix: m.prefix()}
	if m.ctl == ctlExit {
		out.Status = m.exitCode
		out.Exit = true
	}
	return out
}

type value struct {
	kind int
	s    string
	b    []byte
	rows []parent.Row
	cols []string
	row  parent.Row
	d    any
	// groups is set by group by: the rows of each group, in the order of
	// rows, so count and the other aggregates can follow.
	groups [][]parent.Row
	gcol   string
}

type machine struct {
	g     grant.Grant
	p     parent.Parent
	stack []string
	vars  map[string]value
	fns   map[string]*Fn
	out   strings.Builder
	err   strings.Builder
	trace bool
	depth int

	ctl       ctl
	retVal    *value
	exitCode  int
	loopDepth int
	verbs     int
	st        *State
	// sawSecret is set when a word expanded a secret, so prepare can keep
	// it from reaching a verb or a file.
	sawSecret bool
	// deadline is the innermost timeout; zero is none.
	deadline time.Time
	// scoped counts the loop, catch, and parameter bindings in force. A
	// scope restores its name on the way out, so declare -r there would be
	// undone; it is refused.
	scoped map[string]int
}

var (
	errVerbMax = fmt.Errorf("verb max")
	errTimeout = fmt.Errorf("timeout")
	// nowFn and sleepFn are the clock; tests replace them.
	nowFn   = time.Now
	sleepFn = time.Sleep
)

func (m *machine) pastDeadline() bool {
	return !m.deadline.IsZero() && nowFn().After(m.deadline)
}

// duration reads a retry delay or a timeout: a Go duration in (0, max].
func (m *machine) duration(w Word, max time.Duration, zeroOK bool) (time.Duration, error) {
	text, err := m.expand(w)
	if err != nil {
		return 0, err
	}
	d, err := time.ParseDuration(text)
	if err != nil || d < 0 || (d == 0 && !zeroOK) || d > max {
		return 0, fmt.Errorf("usage duration: up to %s", max)
	}
	return d, nil
}

func (m *machine) runTimeout(t *Timeout) (int, bool) {
	d, err := m.duration(t.D, 24*time.Hour, false)
	if err != nil {
		m.err.WriteString(err.Error() + "\n")
		return exitType, true
	}
	prev := m.deadline
	end := nowFn().Add(d)
	if prev.IsZero() || end.Before(prev) {
		m.deadline = end
	}
	defer func() { m.deadline = prev }()
	return m.runProg(&t.Body)
}

// runRetry runs the body until it succeeds, up to N times. A failure is
// what try catches, or a non-zero status. Only the last attempt's stderr is
// kept. A jump or a cap passes through at once.
func (m *machine) runRetry(r *Retry) (int, bool) {
	fail := func(msg string) (int, bool) {
		m.err.WriteString(msg + "\n")
		return exitType, true
	}
	text, err := m.expand(r.N)
	if err != nil {
		return fail(err.Error())
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > 100 {
		return fail("usage retry: 1-100")
	}
	var delay time.Duration
	if r.Delay != nil {
		if delay, err = m.duration(*r.Delay, time.Hour, true); err != nil {
			return fail(err.Error())
		}
	}
	mark := m.err.Len()
	var st int
	var stop bool
	for attempt := 1; attempt <= n; attempt++ {
		if attempt > 1 {
			all := m.err.String()
			m.err.Reset()
			m.err.WriteString(all[:mark])
			wait := delay
			if !m.deadline.IsZero() {
				if left := m.deadline.Sub(nowFn()); left < wait {
					wait = left
				}
			}
			if wait > 0 {
				sleepFn(wait)
			}
		}
		st, stop = m.runProg(&r.Body)
		if stop && m.ctl != ctlNone {
			return st, true
		}
		if !stop && st == 0 {
			return 0, false
		}
		if m.pastDeadline() {
			break
		}
	}
	return st, stop
}

// mutating names the verbs plan mode stops.
var mutating = map[string]bool{"unit.restart": true, "package.install": true, "sysctl.set": true, "file.write": true}

// do is the one door to the parent. It counts verbs against verbMax, and in
// plan mode a mutating verb stops here: it prints what it would do and
// succeeds without a call.
func (m *machine) do(v parent.Verb) (parent.Result, error) {
	if m.verbs >= m.st.verbMax {
		return parent.Result{}, errVerbMax
	}
	if m.pastDeadline() {
		return parent.Result{}, errTimeout
	}
	m.verbs++
	if m.st.plan && mutating[v.Key] {
		what := strings.Join(v.Argv, " ")
		if v.Key == "file.write" {
			what = fmt.Sprintf("write %s (%d bytes)", v.Path, len(v.Body))
		}
		m.err.WriteString("plan: would " + what + "\n")
		return parent.Result{Kind: parent.KindBytes}, nil
	}
	return m.p.Do(v)
}

func newMachine(g grant.Grant, p parent.Parent, st *State) *machine {
	if st == nil {
		st = NewState()
	}
	return &machine{g: g, p: p, vars: st.vars, fns: st.fns, st: st, scoped: map[string]int{}}
}

func (m *machine) prefix() string {
	if len(m.stack) == 0 {
		return ""
	}
	return m.stack[len(m.stack)-1]
}

func (m *machine) runProg(prog *Program) (int, bool) {
	status := 0
	for _, s := range prog.Stmts {
		if m.pastDeadline() {
			m.err.WriteString("timeout\n")
			return exitType, true
		}
		st, stop := m.runStmt(s)
		status = st
		if stop {
			return status, true
		}
		// set -e: a failed command or let ends the script. A condition is
		// not a command: if, while, and the left of && or || are spared.
		if st != 0 && m.st.errexit {
			switch s.(type) {
			case *Chain, *Let:
				return status, true
			}
		}
	}
	return status, false
}

func (m *machine) runStmt(s Stmt) (int, bool) {
	switch s := s.(type) {
	case *Let:
		return m.runLet(s)
	case *If:
		return m.runIf(s)
	case *For:
		return m.runFor(s)
	case *While:
		return m.runWhile(s)
	case *Jump:
		return m.runJump(s)
	case *Try:
		return m.runTry(s)
	case *Declare:
		return m.runDeclare(s)
	case *Use:
		return m.runUse(s)
	case *With:
		return m.runWith(s)
	case *Retry:
		return m.runRetry(s)
	case *Timeout:
		return m.runTimeout(s)
	case *Fn:
		return m.defFn(s)
	case *Chain:
		return m.runChain(s)
	default:
		m.err.WriteString("type stmt\n")
		return exitType, true
	}
}

func (m *machine) defFn(fn *Fn) (int, bool) {
	if reserved(fn.Name) {
		m.err.WriteString("usage fn\n")
		return exitType, true
	}
	m.fns[fn.Name] = fn
	return 0, false
}

func (m *machine) runLet(s *Let) (int, bool) {
	if s.Word != nil {
		if x := exprOf(*s.Word); x != nil {
			d, err := m.evalX(x)
			if err != nil {
				m.err.WriteString(err.Error() + "\n")
				return exitType, true
			}
			return m.bindLet(s.Name, fromDatum(d))
		}
		// A lone $name keeps its value, so a secret stays a secret.
		if w := *s.Word; len(w.Parts) == 1 && w.Parts[0].Exp {
			if d, err := m.ref(w.Parts[0].Lit); err == nil && isSecret(d) {
				return m.bindLet(s.Name, value{kind: valData, d: d})
			}
		}
		m.sawSecret = false
		text, err := m.expand(*s.Word)
		if err == nil && m.sawSecret {
			err = fmt.Errorf("a secret in a word: build it inside [ ]")
		}
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
		return m.bindLet(s.Name, value{kind: valStr, s: text})
	}
	st, stop, v := m.runPipe(*s.Pipe)
	if stop {
		return st, true
	}
	if code, stop := m.bindLet(s.Name, v); stop {
		return code, true
	}
	return st, false
}

func (m *machine) runDeclare(d *Declare) (int, bool) {
	fail := func(err error) (int, bool) {
		m.err.WriteString(err.Error() + "\n")
		return exitType, true
	}
	if d.Name == "" {
		v, err := m.st.declareRows("")
		if err != nil {
			return fail(err)
		}
		m.out.WriteString(display(v))
		return 0, false
	}
	if strings.Contains(d.Flags, "p") {
		v, err := m.st.declareRows(d.Name)
		if err != nil {
			return fail(err)
		}
		m.out.WriteString(display(v))
		return 0, false
	}
	if err := m.st.checkBind(d.Name); err != nil {
		return fail(err)
	}
	if m.scoped[d.Name] > 0 && strings.ContainsAny(d.Flags, "riaA") {
		return fail(fmt.Errorf("usage declare: %s is a loop, catch, or parameter name here", d.Name))
	}
	typ := ""
	for _, f := range d.Flags {
		t := map[rune]string{'i': "number", 'a': "list", 'A': "map"}[f]
		if t == "" {
			continue
		}
		if typ != "" && typ != t {
			return fail(fmt.Errorf("usage declare: one of -i -a -A"))
		}
		typ = t
	}
	prevType, hadType := m.st.types[d.Name]
	if typ != "" {
		m.st.types[d.Name] = typ
	}
	restore := func() {
		if hadType {
			m.st.types[d.Name] = prevType
		} else {
			delete(m.st.types, d.Name)
		}
	}
	if d.Value != nil {
		if st, stop := m.runLet(d.Value); stop {
			restore()
			return st, true
		}
	} else if v, ok := m.vars[d.Name]; ok && typ != "" {
		conv, err := m.st.typed(d.Name, v)
		if err != nil {
			restore()
			return fail(err)
		}
		m.vars[d.Name] = conv
	}
	if strings.Contains(d.Flags, "s") {
		v, ok := m.vars[d.Name]
		if !ok {
			restore()
			return fail(fmt.Errorf("usage declare -s: needs a value"))
		}
		if dv := toDatum(v); !isSecret(dv) {
			m.vars[d.Name] = value{kind: valData, d: secretVal{dv}}
		}
	}
	if strings.Contains(d.Flags, "r") {
		if _, ok := m.vars[d.Name]; !ok {
			restore()
			return fail(fmt.Errorf("usage declare -r: needs a value"))
		}
		m.st.readonly[d.Name] = true
	}
	return 0, false
}

// runWith applies the overrides, runs the body, and restores every one of
// them on any way out: normal end, a failure, a jump, exit, or a cap.
// Nothing here touches the grant. noglob is refused: globs expand for the
// whole line before it runs, so a block cannot scope them.
func (m *machine) runWith(w *With) (int, bool) {
	fail := func(status int, msg string) (int, bool) {
		m.err.WriteString(msg + "\n")
		return status, true
	}
	saved := *m.st
	stack := append([]string(nil), m.stack...)
	var names []string
	for _, l := range w.Lets {
		names = append(names, l.Name)
	}
	if !m.bindable(names...) {
		return exitType, true
	}
	vars := m.saveVars(names)
	defer func() {
		m.st.errexit, m.st.nounset, m.st.xtrace, m.st.noglob = saved.errexit, saved.nounset, saved.xtrace, saved.noglob
		m.st.loopMax, m.st.verbMax = saved.loopMax, saved.verbMax
		m.stack = stack
		m.restoreVars(vars)
	}()
	if len(w.Set) > 0 {
		for _, a := range w.Set {
			short := (a[0] == '-' || a[0] == '+') && a[1:] != "o" && strings.ContainsRune(a[1:], 'f')
			if a == "noglob" || short {
				return fail(exitType, "usage with: noglob is per line; use set -f")
			}
		}
		if _, err := m.st.runSet(append([]string{"set"}, w.Set...)); err != nil {
			return fail(exitType, err.Error())
		}
	}
	for _, k := range []string{"loopmax", "verbmax"} {
		if n, ok := w.Caps[k]; ok {
			if _, err := m.st.runSet([]string{"set", k, n}); err != nil {
				return fail(exitType, err.Error())
			}
		}
	}
	if w.Prefix != nil {
		p, err := m.expand(*w.Prefix)
		if err != nil {
			return fail(exitType, err.Error())
		}
		st := expStage{argv: []string{"pushd", p}, key: "file.read"}
		if !m.g.Has("file.read") {
			return fail(exitDenied, "denied file.read")
		}
		if prob := m.needRead(&st, p); prob != nil {
			return fail(prob.status, prob.msg)
		}
		if _, code := m.move(st); code != 0 {
			return fail(code, "with prefix: cannot stat")
		}
	}
	for _, l := range w.Lets {
		if st, stop := m.runLet(l); stop {
			return st, true
		}
	}
	return m.runProg(&w.Body)
}

// runUse reads a file through the file helper and loads its definitions.
// Only fn and declare -r with a word or [ ] value are allowed, so loading
// runs no verb. The whole file is checked before anything is defined. The
// file is not expanded: no history, braces, or globs.
func (m *machine) runUse(u *Use) (int, bool) {
	fail := func(status int, msg string) (int, bool) {
		m.err.WriteString(msg + "\n")
		return status, true
	}
	p, err := m.expand(u.Path)
	if err != nil {
		return fail(exitType, err.Error())
	}
	if !m.g.Has("file.read") {
		return fail(exitDenied, "denied file.read")
	}
	full, err := resolvePath(m.prefix(), p)
	if err != nil {
		return fail(exitType, "prefix_unset")
	}
	if !m.g.AllowsRead(full) {
		return fail(exitDenied, "denied file.read")
	}
	res, err := m.do(parent.Verb{Key: "file.read", Argv: []string{"cat", full}, Path: full})
	if err != nil || res.Exit != 0 {
		return fail(1, "use: cannot read")
	}
	if len(res.Bytes) > dataMaxBytes {
		return fail(exitType, "use: too large")
	}
	prog, err := parse(string(res.Bytes))
	if err != nil {
		return fail(exitType, "use: "+err.Error())
	}
	for _, s := range prog.Stmts {
		switch s := s.(type) {
		case *Fn:
			if reserved(s.Name) {
				return fail(exitType, "use: usage fn")
			}
		case *Declare:
			if !strings.Contains(s.Flags, "r") || s.Value == nil || s.Value.Pipe != nil {
				return fail(exitType, "use: only fn and declare -r name = value")
			}
		default:
			return fail(exitType, "use: only fn and declare -r name = value")
		}
	}
	for _, s := range prog.Stmts {
		if st, stop := m.runStmt(s); stop {
			return st, true
		}
	}
	return 0, false
}

func (m *machine) bindLet(name string, v value) (int, bool) {
	if err := m.st.assign(name, v); err != nil {
		m.err.WriteString(err.Error() + "\n")
		return exitType, true
	}
	return 0, false
}

// bindable refuses a loop, catch, or parameter name that is read-only.
func (m *machine) bindable(names ...string) bool {
	for _, n := range names {
		if n == "" {
			continue
		}
		if err := m.st.checkBind(n); err != nil {
			m.err.WriteString(err.Error() + "\n")
			return false
		}
		// A scope binds without the type check, so a typed name is not
		// one a scope may take.
		if _, typed := m.st.types[n]; typed {
			m.err.WriteString("typed " + n + "\n")
			return false
		}
	}
	return true
}

func (m *machine) runIf(s *If) (int, bool) {
	var take bool
	if s.CondX != nil {
		d, err := m.evalX(s.CondX)
		if err == nil {
			take, err = decide(d)
		}
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
	} else {
		st, stop, v := m.runPipe(s.Cond)
		if stop {
			return st, true
		}
		take = st == 0
		if v.kind == valRows {
			take = len(v.rows) > 0
		}
	}
	var branch *Program
	if take {
		branch = &s.Then
	} else {
		branch = s.Else
	}
	if branch == nil {
		return 0, false
	}
	return m.runProg(branch)
}

func (m *machine) runFor(s *For) (int, bool) {
	type item struct{ a, b value }
	var items []item
	isMap := false
	switch {
	case s.SrcX != nil || s.SrcRef != "":
		var d any
		var err error
		if s.SrcX != nil {
			d, err = m.evalX(s.SrcX)
		} else {
			d, err = m.ref(s.SrcRef)
		}
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
		switch x := d.(type) {
		case []any:
			for i, e := range x {
				items = append(items, item{fromDatum(int64(i)), fromDatum(e)})
			}
		case *omap:
			isMap = true
			for _, k := range x.keys {
				items = append(items, item{fromDatum(k), fromDatum(x.m[k])})
			}
		default:
			m.err.WriteString("type for " + typeName(d) + "\n")
			return exitType, true
		}
	default:
		st, stop, v := m.runPipe(s.Src)
		if stop {
			return st, true
		}
		switch {
		case v.kind == valRows:
			for i, row := range v.rows {
				items = append(items, item{fromDatum(int64(i)), value{kind: valRow, row: row}})
			}
		case v.kind == valData:
			// from json and the like: walk the value as a [ ] would.
			switch x := v.d.(type) {
			case []any:
				for i, e := range x {
					items = append(items, item{fromDatum(int64(i)), fromDatum(e)})
				}
			case *omap:
				isMap = true
				for _, k := range x.keys {
					items = append(items, item{fromDatum(k), fromDatum(x.m[k])})
				}
			default:
				m.err.WriteString("type for " + typeName(v.d) + "\n")
				return exitType, true
			}
		default:
			m.err.WriteString("type for\n")
			return exitType, true
		}
	}
	names := []string{s.Name}
	if s.Name2 != "" {
		names = append(names, s.Name2)
	}
	if !m.bindable(names...) {
		return exitType, true
	}
	saved := m.saveVars(names)
	defer m.restoreVars(saved)
	status, iter := 0, 0
	for _, it := range items {
		// One name takes the item: a list element, a map key, or a row.
		// Two names take index and element, or key and value.
		if s.Name2 == "" {
			if isMap {
				m.vars[s.Name] = it.a
			} else {
				m.vars[s.Name] = it.b
			}
		} else {
			m.vars[s.Name] = it.a
			m.vars[s.Name2] = it.b
		}
		st, stop, brk := m.loopBody(&s.Body, &iter)
		status = st
		if stop {
			return status, true
		}
		if brk {
			break
		}
	}
	return status, false
}

type savedVar struct {
	name string
	v    value
	had  bool
}

func (m *machine) saveVars(names []string) []savedVar {
	out := make([]savedVar, len(names))
	for i, n := range names {
		m.scoped[n]++
		v, had := m.vars[n]
		out[i] = savedVar{n, v, had}
	}
	return out
}

func (m *machine) restoreVars(saved []savedVar) {
	for _, s := range saved {
		if m.scoped[s.name]--; m.scoped[s.name] <= 0 {
			delete(m.scoped, s.name)
		}
		if s.had {
			m.vars[s.name] = s.v
		} else {
			delete(m.vars, s.name)
		}
	}
}

// loopBody runs one iteration under loopMax. brk is a taken break.
func (m *machine) loopBody(body *Program, iter *int) (status int, stop, brk bool) {
	*iter++
	if *iter > m.st.loopMax {
		m.err.WriteString("loop max\n")
		m.ctl = ctlHalt
		return exitType, true, false
	}
	m.loopDepth++
	st, stop := m.runProg(body)
	m.loopDepth--
	if stop {
		switch m.ctl {
		case ctlBreak:
			m.ctl = ctlNone
			return st, false, true
		case ctlContinue:
			m.ctl = ctlNone
			return st, false, false
		}
		return st, true, false
	}
	return st, false, false
}

// test evaluates an if or while condition.
func (m *machine) test(x Expr, pipe Pipeline) (bool, int, bool) {
	if x != nil {
		d, err := m.evalX(x)
		var ok bool
		if err == nil {
			ok, err = decide(d)
		}
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return false, exitType, true
		}
		return ok, 0, false
	}
	st, stop, v := m.runPipe(pipe)
	if stop {
		return false, st, true
	}
	if v.kind == valRows {
		return len(v.rows) > 0, st, false
	}
	return st == 0, st, false
}

func (m *machine) runWhile(s *While) (int, bool) {
	status, iter := 0, 0
	first := s.Do
	for {
		if !first {
			ok, st, stop := m.test(s.CondX, s.Cond)
			if stop {
				return st, true
			}
			if !ok {
				return status, false
			}
		}
		first = false
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

func (m *machine) runJump(j *Jump) (int, bool) {
	switch j.Kind {
	case "break", "continue":
		if m.loopDepth == 0 {
			m.err.WriteString("usage " + j.Kind + "\n")
			return exitType, true
		}
		if j.Kind == "break" {
			m.ctl = ctlBreak
		} else {
			m.ctl = ctlContinue
		}
		return 0, true
	case "return":
		if m.depth == 0 {
			m.err.WriteString("usage return\n")
			return exitType, true
		}
		m.retVal = nil
		if j.Word != nil {
			v, err := m.wordValue(*j.Word)
			if err != nil {
				m.err.WriteString(err.Error() + "\n")
				return exitType, true
			}
			m.retVal = &v
		}
		m.ctl = ctlReturn
		return 0, true
	}
	code := 0
	if j.Word != nil {
		v, err := m.wordValue(*j.Word)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
		n, ok := number(toDatum(v))
		i, isInt := n.(int64)
		if !ok || !isInt || i < 0 || i > 255 {
			m.err.WriteString("usage exit\n")
			return exitType, true
		}
		code = int(i)
	}
	m.ctl = ctlExit
	m.exitCode = code
	return code, true
}

// wordValue is a word as a value: an expression keeps its datum, any other
// word is its expanded text.
func (m *machine) wordValue(w Word) (value, error) {
	if x := exprOf(w); x != nil {
		d, err := m.evalX(x)
		if err != nil {
			return value{}, err
		}
		return fromDatum(d), nil
	}
	if len(w.Parts) == 1 && w.Parts[0].Exp {
		d, err := m.ref(w.Parts[0].Lit)
		if err != nil {
			return value{}, fmt.Errorf("type value")
		}
		return fromDatum(d), nil
	}
	text, err := m.expand(w)
	if err != nil {
		return value{}, err
	}
	return value{kind: valStr, s: text}, nil
}

// runTry catches a failure that stops Body. Its stderr text moves into
// the caught map and out of the session's stderr. A jump passes through.
func (m *machine) runTry(t *Try) (int, bool) {
	mark := m.err.Len()
	st, stop := m.runProg(&t.Body)
	if !stop || m.ctl != ctlNone {
		return st, stop
	}
	all := m.err.String()
	msg := strings.TrimSpace(all[mark:])
	m.err.Reset()
	m.err.WriteString(all[:mark])
	if t.Catch == nil {
		return st, false
	}
	var saved []savedVar
	if !m.bindable(t.Name) {
		return exitType, true
	}
	if t.Name != "" {
		saved = m.saveVars([]string{t.Name})
		e := newOmap()
		e.set("status", int64(st))
		e.set("message", msg)
		key := ""
		if first, _, _ := strings.Cut(msg, "\n"); strings.HasPrefix(first, "denied ") {
			key = strings.TrimPrefix(first, "denied ")
		}
		e.set("key", key)
		m.vars[t.Name] = value{kind: valData, d: e}
	}
	st, stop = m.runProg(t.Catch)
	if saved != nil {
		m.restoreVars(saved)
	}
	return st, stop
}

func (m *machine) runChain(c *Chain) (int, bool) {
	prepared := make([][]expStage, len(c.Pipes))
	for i, pipe := range c.Pipes {
		exp, prob := m.prepare(pipe)
		if prob != nil {
			m.err.WriteString(prob.msg + "\n")
			return prob.status, true
		}
		prepared[i] = exp
	}
	status := 0
	for i, exp := range prepared {
		if i > 0 && c.Ops[i-1] == "&&" && status != 0 {
			break
		}
		if i > 0 && c.Ops[i-1] == "||" && status == 0 {
			break
		}
		st, v := m.execPrepared(exp)
		status = st
		if m.ctl != ctlNone {
			return st, true
		}
		if exp[len(exp)-1].outPath == "" && (exp[0].call == nil || v.kind != valBytes || len(v.b) > 0) {
			m.out.WriteString(display(v))
		}
		if st == exitDenied || st == exitUnknown || st == exitType {
			return st, true
		}
	}
	return status, false
}

func (m *machine) runPipe(pipe Pipeline) (int, bool, value) {
	exp, prob := m.prepare(pipe)
	if prob != nil {
		m.err.WriteString(prob.msg + "\n")
		return prob.status, true, value{}
	}
	st, v := m.execPrepared(exp)
	stop := st == exitDenied || st == exitUnknown || st == exitType || m.ctl != ctlNone
	return st, stop, v
}

type expStage struct {
	argv    []string
	inPath  string
	outPath string
	here    []byte
	paths   []string
	path    string
	key     string
	op      string
	call    *Fn
	mover   bool
	// x is the [ ] of filter or each, kept unevaluated for each row.
	x Expr
}

// msgCommandFromValue refuses a stage whose command name is not a literal.
const msgCommandFromValue = "command_from_value"

type problem struct {
	status int
	msg    string
}

func (m *machine) prepare(pipe Pipeline) ([]expStage, *problem) {
	exp := make([]expStage, len(pipe.Stages))
	for i, st := range pipe.Stages {
		// A command name is a literal on the line, never a value: the
		// verb a line runs is the verb its reader sees. This is checked
		// before any expansion, classify, or parent call.
		if len(st.Argv) == 0 || plain(st.Argv[0]) == "" {
			return nil, &problem{exitType, msgCommandFromValue}
		}
		m.sawSecret = false
		var rowX Expr
		var argv []string
		var err error
		name := plainFirst(st)
		last := len(st.Argv) - 1
		if (name == "filter" || name == "each" || name == "check" || name == "log") && last >= 1 && exprOf(st.Argv[last]) != nil {
			// The [ ] keeps its value: per row for filter and each, a
			// datum for check and log.
			rowX = exprOf(st.Argv[last])
			if argv, err = m.expandArgv(st.Argv[:last]); err != nil {
				return nil, &problem{exitType, err.Error()}
			}
			argv = append(argv, exprMark)
		} else if argv, err = m.expandArgv(st.Argv); err != nil {
			return nil, &problem{exitType, err.Error()}
		}
		type redirText struct {
			kind string
			text string
		}
		var texts []redirText
		reads, heres := 0, 0
		for _, r := range st.Redirs {
			text, err := m.expand(r.Path)
			if err != nil {
				return nil, &problem{exitType, err.Error()}
			}
			texts = append(texts, redirText{r.Kind, text})
			switch r.Kind {
			case "<":
				reads++
			case "<<<":
				heres++
			}
		}
		if reads+heres > 1 || (i != 0 && reads+heres > 0) {
			return nil, &problem{exitType, "usage <"}
		}
		var inPath, outPath string
		var here []byte
		for _, r := range texts {
			if r.kind == "<<<" {
				here = []byte(r.text + "\n")
				continue
			}
			if r.kind == "<" {
				full, perr := resolvePath(m.prefix(), r.text)
				if perr != nil {
					return nil, &problem{exitType, "prefix_unset"}
				}
				if !m.g.AllowsRead(full) {
					return nil, &problem{exitDenied, "denied file.read"}
				}
				inPath = full
				continue
			}
			full, perr := resolvePath(m.prefix(), r.text)
			if perr != nil {
				return nil, &problem{exitType, "prefix_unset"}
			}
			if i != len(pipe.Stages)-1 || outPath != "" {
				return nil, &problem{exitType, "usage >"}
			}
			if !m.g.AllowsWrite(full) {
				return nil, &problem{exitDenied, "denied file.write"}
			}
			outPath = full
		}
		info, prob := classify(argv, m.fns)
		if prob != nil {
			return nil, prob
		}
		// A secret prints as ***, but it does not leave sash: not as a verb
		// argument, a path, or a function argument.
		if m.sawSecret && (info.key != "" || info.call != nil || inPath != "" || outPath != "" || len(st.Redirs) > 0) {
			return nil, &problem{exitType, "a secret cannot leave sash"}
		}
		if info.call != nil && (len(pipe.Stages) != 1 || inPath != "" || outPath != "" || here != nil) {
			return nil, &problem{exitType, "type fn"}
		}
		if argv[0] == "template_file" && (len(pipe.Stages) != 1 || inPath != "" || outPath != "" || here != nil) {
			return nil, &problem{exitType, "type template_file"}
		}
		if info.mover && (len(pipe.Stages) != 1 || inPath != "" || outPath != "" || here != nil) {
			return nil, &problem{exitType, "type " + argv[0]}
		}
		op := ""
		if i > 0 {
			op = pipe.Ops[i-1]
		}
		exp[i] = expStage{
			argv: argv, inPath: inPath, outPath: outPath, here: here,
			key: info.key, op: op, call: info.call, mover: info.mover, x: rowX,
		}
	}
	if prob := typecheck(exp); prob != nil {
		return nil, prob
	}
	for i := range exp {
		if exp[i].key != "" && !m.g.Has(exp[i].key) {
			return nil, &problem{exitDenied, "denied " + exp[i].key}
		}
	}
	for i := range exp {
		if prob := m.bindPaths(&exp[i]); prob != nil {
			return nil, prob
		}
	}
	return exp, nil
}

type classified struct {
	key   string
	in    int
	out   int
	call  *Fn
	mover bool
}

func classify(argv []string, fns map[string]*Fn) (classified, *problem) {
	if len(argv) == 0 {
		return classified{}, &problem{exitType, "usage empty"}
	}
	name := argv[0]
	if rowTools[name] {
		if prob := classifyRowTool(argv); prob != nil {
			return classified{}, prob
		}
		return classified{in: inRows, out: inRows}, nil
	}
	if fn, ok := fns[name]; ok {
		if len(argv)-1 != len(fn.Params) {
			return classified{}, &problem{exitType, "usage " + name}
		}
		return classified{call: fn, in: inNone, out: inBytes}, nil
	}
	switch name {
	case "echo", "dirs":
		if name == "dirs" && len(argv) != 1 {
			return classified{}, &problem{exitType, "usage dirs"}
		}
		return classified{in: inNone, out: inBytes}, nil
	case "egrep":
		if len(argv) != 2 {
			return classified{}, &problem{exitType, "usage egrep"}
		}
		if _, err := regexp.Compile(argv[1]); err != nil {
			return classified{}, &problem{exitType, "usage egrep"}
		}
		return classified{in: inBytes, out: inBytes}, nil
	case "cut":
		if !hasFlag(argv, "-f") {
			return classified{}, &problem{exitType, "usage cut"}
		}
		return classified{in: inBytes, out: inBytes}, nil
	case "head", "wc":
		return classified{in: inFlex, out: inFlex}, nil
	case "where":
		if len(argv) != 4 || !isOp(argv[2]) {
			return classified{}, &problem{exitType, "usage where"}
		}
		return classified{in: inRows, out: inRows}, nil
	case "rows":
		seen := map[string]bool{}
		for _, name := range argv[1:] {
			if !rowName(name) || seen[name] {
				return classified{}, &problem{exitType, "usage rows"}
			}
			seen[name] = true
		}
		return classified{in: inBytes, out: inRows}, nil
	case "json":
		if len(argv) != 1 {
			return classified{}, &problem{exitType, "usage json"}
		}
		return classified{in: inAny, out: inBytes}, nil
	case "builtin", "set", "shopt":
		return classified{in: inNone, out: inRows}, nil
	case "lines":
		if len(argv) != 1 {
			return classified{}, &problem{exitType, "usage lines"}
		}
		return classified{in: inBytes, out: inRows}, nil
	case "check":
		if len(argv) != 3 || argv[2] != exprMark || len(argv[1]) > 256 {
			return classified{}, &problem{exitType, "usage check \"name\" [ expr ]"}
		}
		return classified{in: inNone, out: inBytes}, nil
	case "checks":
		if len(argv) != 1 {
			return classified{}, &problem{exitType, "usage checks"}
		}
		return classified{in: inNone, out: inRows}, nil
	case "log":
		n := len(argv)
		if argv[n-1] == exprMark {
			n--
		}
		if n != 3 || !logLevels[argv[1]] {
			return classified{}, &problem{exitType, "usage log debug|info|warn|error msg [ {map} ]"}
		}
		return classified{in: inNone, out: inBytes}, nil
	case "template_file":
		return classifyTemplateFile(argv)
	case "template":
		if len(argv) != 2 {
			return classified{}, &problem{exitType, "usage template tpl"}
		}
		return classified{key: "file.read", in: inAny, out: inBytes}, nil
	case "parse":
		if prob := classifyParse(argv); prob != nil {
			return classified{}, prob
		}
		return classified{in: inBytes, out: inRows}, nil
	case "sub":
		if prob := classifySub(argv); prob != nil {
			return classified{}, prob
		}
		return classified{in: inRows, out: inRows}, nil
	case "from", "to":
		noheader := name == "from" && len(argv) == 3 && argv[2] == "--noheader" && (argv[1] == "csv" || argv[1] == "tsv")
		if (len(argv) != 2 && !noheader) || !formatNames[argv[1]] {
			return classified{}, &problem{exitType, "usage " + name}
		}
		// from declares rows so |> may follow; it keeps the whole value,
		// and a list of maps becomes rows only where |> needs them.
		if name == "from" {
			return classified{in: inAny, out: inRows}, nil
		}
		return classified{in: inAny, out: inBytes}, nil
	case "select":
		if len(argv) < 2 {
			return classified{}, &problem{exitType, "usage select"}
		}
		return classified{in: inRows, out: inRows}, nil
	case "cd", "pushd":
		if len(argv) != 2 {
			return classified{}, &problem{exitType, "usage " + name}
		}
		return classified{key: "file.read", mover: true, in: inNone, out: inBytes}, nil
	case "popd":
		if len(argv) != 1 {
			return classified{}, &problem{exitType, "usage popd"}
		}
		return classified{mover: true, in: inNone, out: inBytes}, nil
	case "ps":
		return classified{key: "ps", in: inNone, out: inRows}, nil
	case "journal":
		return classified{key: "journal", in: inNone, out: inRows}, nil
	case "ls":
		if len(argv) > 2 {
			return classified{}, &problem{exitType, "usage ls"}
		}
		return classified{key: "file.read", in: inNone, out: inRows}, nil
	case "cat":
		if len(argv) == 1 {
			return classified{in: inBytes, out: inBytes}, nil
		}
		return classified{key: "file.read", in: inNone, out: inBytes}, nil
	case "unit":
		sub := "status"
		if len(argv) > 1 {
			sub = argv[1]
		}
		switch sub {
		case "status":
			return classified{key: "unit.status", in: inNone, out: inRows}, nil
		case "restart":
			if len(argv) != 3 {
				return classified{}, &problem{exitType, "usage unit"}
			}
			return classified{key: "unit.restart", in: inNone, out: inBytes}, nil
		default:
			return classified{}, &problem{exitType, "usage unit"}
		}
	case "package":
		if len(argv) < 2 {
			return classified{}, &problem{exitType, "usage package"}
		}
		switch argv[1] {
		case "query":
			return classified{key: "package.query", in: inNone, out: inRows}, nil
		case "install":
			if len(argv) != 3 {
				return classified{}, &problem{exitType, "usage package"}
			}
			return classified{key: "package.install", in: inNone, out: inBytes}, nil
		default:
			return classified{}, &problem{exitType, "usage package"}
		}
	case "sysctl":
		if len(argv) < 2 {
			return classified{}, &problem{exitType, "usage sysctl"}
		}
		switch argv[1] {
		case "get":
			if len(argv) != 3 {
				return classified{}, &problem{exitType, "usage sysctl"}
			}
			return classified{key: "sysctl.get", in: inNone, out: inBytes}, nil
		case "set":
			if len(argv) != 4 {
				return classified{}, &problem{exitType, "usage sysctl"}
			}
			return classified{key: "sysctl.set", in: inNone, out: inBytes}, nil
		default:
			return classified{}, &problem{exitType, "usage sysctl"}
		}
	case "config":
		if len(argv) != 3 || argv[1] != "read" {
			return classified{}, &problem{exitType, "usage config"}
		}
		return classified{key: "config.read", in: inNone, out: inBytes}, nil
	default:
		return classified{}, &problem{exitUnknown, "unknown " + name}
	}
}

func typecheck(exp []expStage) *problem {
	kind := inNone
	for i, st := range exp {
		in, out := stageShape(st)
		if i == 0 {
			if in == inRows {
				return &problem{exitType, "type rows"}
			}
			if in == inFlex {
				out = inBytes
			}
			kind = out
			continue
		}
		if st.op == "|>" {
			if kind != inRows || in == inBytes {
				return &problem{exitType, "type |>"}
			}
			if in == inFlex {
				out = inRows
			}
		} else {
			if in == inRows {
				return &problem{exitType, "type |"}
			}
			if in == inFlex {
				out = inBytes
			}
		}
		kind = out
	}
	return nil
}

func stageShape(st expStage) (int, int) {
	if st.call != nil {
		return inNone, inBytes
	}
	if rowTools[st.argv[0]] {
		return inRows, inRows
	}
	switch st.argv[0] {
	case "echo", "dirs", "cd", "pushd", "popd":
		return inNone, inBytes
	case "egrep", "cut":
		return inBytes, inBytes
	case "where", "select":
		return inRows, inRows
	case "rows":
		return inBytes, inRows
	case "json", "to":
		return inAny, inBytes
	case "from":
		return inAny, inRows
	case "lines", "parse":
		return inBytes, inRows
	case "template_file", "check", "log":
		return inNone, inBytes
	case "checks":
		return inNone, inRows
	case "template":
		return inAny, inBytes
	case "sub":
		return inRows, inRows
	case "head", "wc":
		return inFlex, inFlex
	case "ps", "journal", "ls", "builtin", "set", "shopt":
		return inNone, inRows
	case "unit":
		if len(st.argv) > 1 && st.argv[1] == "restart" {
			return inNone, inBytes
		}
		return inNone, inRows
	case "package":
		if len(st.argv) > 1 && st.argv[1] == "query" {
			return inNone, inRows
		}
		return inNone, inBytes
	case "cat":
		if len(st.argv) == 1 {
			return inBytes, inBytes
		}
		return inNone, inBytes
	default:
		return inNone, inBytes
	}
}

func (m *machine) bindPaths(st *expStage) *problem {
	switch st.argv[0] {
	case "cat":
		for _, a := range st.argv[1:] {
			if prob := m.needRead(st, a); prob != nil {
				return prob
			}
			st.paths = append(st.paths, st.path)
		}
	case "ls":
		p := m.prefix()
		if len(st.argv) == 2 {
			p = st.argv[1]
		}
		return m.needRead(st, p)
	case "config":
		return m.needRead(st, st.argv[2])
	case "cd", "pushd":
		return m.needRead(st, st.argv[1])
	case "template":
		return m.needRead(st, st.argv[1])
	case "template_file":
		if !m.g.Has("file.read") {
			return &problem{exitDenied, "denied file.read"}
		}
		for _, a := range st.argv[1:3] {
			if prob := m.needRead(st, a); prob != nil {
				return prob
			}
			st.paths = append(st.paths, st.path)
		}
		full, err := resolvePath(m.prefix(), st.argv[3])
		if err != nil {
			return &problem{exitType, "prefix_unset"}
		}
		if len(st.argv) == 5 {
			if !m.g.AllowsRead(full) {
				return &problem{exitDenied, "denied file.read"}
			}
			st.paths = append(st.paths, full)
			return nil
		}
		// out is written by the > door in execPrepared.
		if !m.g.AllowsWrite(full) {
			return &problem{exitDenied, "denied file.write"}
		}
		st.paths = append(st.paths, full)
		st.outPath = full
	}
	return nil
}

func (m *machine) needRead(st *expStage, p string) *problem {
	full, err := resolvePath(m.prefix(), p)
	if err != nil {
		return &problem{exitType, "prefix_unset"}
	}
	if !m.g.AllowsRead(full) {
		return &problem{exitDenied, "denied file.read"}
	}
	st.path = full
	return nil
}

func (m *machine) traceStage(st expStage) error {
	if !m.trace && !m.st.xtrace {
		return nil
	}
	label := st.key
	if label == "" && len(st.argv) > 0 {
		label = st.argv[0]
	}
	if label == "" {
		return nil
	}
	_, err := m.err.WriteString("trace " + label + "\n")
	return err
}

func (m *machine) execPrepared(exp []expStage) (int, value) {
	if len(exp) == 1 && exp[0].call != nil {
		if err := m.traceStage(exp[0]); err != nil {
			return 1, value{}
		}
		st, stop := m.callFn(exp[0])
		if stop && m.ctl == ctlNone && st == 0 {
			st = exitType
		}
		if m.retVal != nil {
			v := *m.retVal
			m.retVal = nil
			return st, v
		}
		return st, value{kind: valBytes}
	}
	cur := value{kind: valBytes}
	if len(exp[0].here) > 0 {
		cur = value{kind: valBytes, b: append([]byte(nil), exp[0].here...)}
	} else if exp[0].inPath != "" {
		res, err := m.do(parent.Verb{Key: "file.read", Argv: []string{"<", exp[0].inPath}, Path: exp[0].inPath})
		if err != nil {
			m.err.WriteString("parent file.read\n")
			return 1, value{}
		}
		cur = fromResult(res)
	}
	exits := make([]int, len(exp))
	for i, st := range exp {
		if st.op == "|" && cur.kind == valRows {
			cur = value{kind: valBytes, b: []byte(render(cur))}
		}
		if st.op == "|>" && cur.kind == valData {
			rows, ok := datumRows(cur.d)
			if !ok {
				m.err.WriteString("type |> " + typeName(cur.d) + "\n")
				return exitType, value{}
			}
			cur = rows
		}
		if err := m.traceStage(st); err != nil {
			return 1, cur
		}
		if (st.key != "" || st.outPath != "") && m.pastDeadline() {
			m.err.WriteString("timeout\n")
			return exitType, value{}
		}
		if (st.key != "" || st.outPath != "") && m.verbs >= m.st.verbMax {
			m.err.WriteString("verb max\n")
			m.ctl = ctlHalt
			return exitType, value{}
		}
		v, code := m.execStage(st, cur)
		exits[i] = code
		cur = v
		if code == exitType || code == exitDenied || code == exitUnknown {
			return code, cur
		}
	}
	status := 0
	for i := len(exits) - 1; i >= 0; i-- {
		if exits[i] != 0 {
			status = exits[i]
			break
		}
	}
	if exp[len(exp)-1].outPath != "" {
		body := render(cur)
		if _, err := m.do(parent.Verb{
			Key:  "file.write",
			Argv: []string{">", exp[len(exp)-1].outPath},
			Path: exp[len(exp)-1].outPath,
			Body: []byte(body),
		}); err != nil {
			m.err.WriteString("parent file.write\n")
			return 1, cur
		}
	}
	return status, cur
}

func (m *machine) callFn(st expStage) (int, bool) {
	if m.depth >= fnDepth {
		m.err.WriteString("fn depth\n")
		return exitType, true
	}
	m.depth++
	defer func() { m.depth-- }()
	fn := st.call
	if !m.bindable(fn.Params...) {
		return exitType, true
	}
	saved := m.saveVars(fn.Params)
	for i, name := range fn.Params {
		m.vars[name] = value{kind: valStr, s: st.argv[i+1]}
	}
	// A loop outside the call is not the call's: break there is usage.
	loops := m.loopDepth
	m.loopDepth = 0
	m.retVal = nil
	status, stop := m.runProg(&fn.Body)
	m.loopDepth = loops
	if stop && m.ctl == ctlReturn {
		m.ctl = ctlNone
		stop = false
		status = 0
	}
	m.restoreVars(saved)
	return status, stop
}

func (m *machine) execStage(st expStage, in value) (value, int) {
	if rowTools[st.argv[0]] {
		return m.runRowTool(st, in)
	}
	switch st.argv[0] {
	case "echo":
		return value{kind: valBytes, b: []byte(strings.Join(st.argv[1:], " ") + "\n")}, 0
	case "egrep":
		return runEgrep(asBytes(in), st.argv[1])
	case "head":
		return m.runHead(st.argv, in)
	case "wc":
		if in.kind == valRows {
			return value{kind: valBytes, b: []byte(strconv.Itoa(len(in.rows)) + "\n")}, 0
		}
		return value{kind: valBytes, b: []byte(strconv.Itoa(len(splitLines(asBytes(in)))) + "\n")}, 0
	case "cut":
		return value{kind: valBytes, b: []byte(runCut(asBytes(in), st.argv[1:]))}, 0
	case "where":
		rows, err := runWhere(in, st.argv[1], st.argv[2], st.argv[3])
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return value{}, exitType
		}
		return value{kind: valRows, rows: rows, cols: in.cols}, 0
	case "rows":
		return runRows(asBytes(in), st.argv[1:]), 0
	case "builtin":
		return runBuiltin(st.argv)
	case "lines":
		return runRows(asBytes(in), nil), 0
	case "parse":
		return m.runParse(st.argv, in)
	case "template_file":
		return m.runTemplateFile(st)
	case "check":
		return m.runCheck(st)
	case "checks":
		return value{kind: valRows, rows: append([]parent.Row(nil), m.st.checks...), cols: []string{"check", "result"}}, 0
	case "log":
		return m.runLog(st)
	case "template":
		return m.runTemplate(st, in)
	case "sub":
		if in.kind != valRows {
			m.err.WriteString("type sub\n")
			return value{}, exitType
		}
		return m.runSub(st.argv, in)
	case "from", "to":
		run := runFrom
		if st.argv[0] == "to" {
			run = runTo
		} else if len(st.argv) == 3 {
			run = func(f string, v value) (value, error) { return runFromOpt(f, v, false) }
		}
		v, err := run(st.argv[1], in)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return value{}, exitType
		}
		return v, 0
	case "set", "shopt":
		v, err := m.st.runSet(st.argv)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return value{}, exitType
		}
		return v, 0
	case "json":
		b, err := encodeJSON(in)
		if err != nil {
			m.err.WriteString("json utf8\n")
			return value{}, exitType
		}
		return value{kind: valBytes, b: b}, 0
	case "select":
		return value{kind: valRows, rows: in.rows, cols: append([]string(nil), st.argv[1:]...)}, 0
	case "dirs":
		var b strings.Builder
		for _, d := range m.stack {
			b.WriteString(d)
			b.WriteByte('\n')
		}
		return value{kind: valBytes, b: []byte(b.String())}, 0
	case "popd":
		if len(m.stack) < 2 {
			m.err.WriteString("usage popd\n")
			return value{}, exitType
		}
		m.stack = m.stack[:len(m.stack)-1]
		return value{kind: valBytes}, 0
	case "cd", "pushd":
		return m.move(st)
	case "cat":
		if len(st.paths) == 0 {
			return value{kind: valBytes, b: []byte(asBytes(in))}, 0
		}
		var b []byte
		for _, pth := range st.paths {
			res, err := m.do(parent.Verb{Key: "file.read", Argv: []string{"cat", pth}, Path: pth})
			if err != nil {
				m.err.WriteString("parent file.read\n")
				return value{}, 1
			}
			if res.Exit != 0 {
				return fromResult(res), res.Exit
			}
			b = append(b, res.Bytes...)
		}
		return value{kind: valBytes, b: b}, 0
	default:
		return m.toolkit(st)
	}
}

func (m *machine) move(st expStage) (value, int) {
	res, err := m.do(parent.Verb{Key: "file.read", Argv: []string{"stat", st.path}, Path: st.path})
	if err != nil {
		m.err.WriteString("parent file.read\n")
		return value{}, 1
	}
	if res.Exit != 0 {
		return value{}, res.Exit
	}
	if st.argv[0] == "pushd" {
		m.stack = append(m.stack, st.path)
	} else if len(m.stack) == 0 {
		m.stack = []string{st.path}
	} else {
		m.stack[len(m.stack)-1] = st.path
	}
	return value{kind: valBytes}, 0
}

func (m *machine) toolkit(st expStage) (value, int) {
	res, err := m.do(parent.Verb{Key: st.key, Argv: st.argv, Path: st.path})
	if err != nil {
		m.err.WriteString("parent " + st.key + "\n")
		return value{}, 1
	}
	return fromResult(res), res.Exit
}

func (m *machine) runHead(argv []string, in value) (value, int) {
	n, err := headCount(argv)
	if err != nil {
		m.err.WriteString("usage head\n")
		return value{}, exitType
	}
	if in.kind == valRows {
		if n > len(in.rows) {
			n = len(in.rows)
		}
		if n < 0 {
			n = 0
		}
		return value{kind: valRows, rows: in.rows[:n], cols: in.cols}, 0
	}
	return value{kind: valBytes, b: []byte(headLines(asBytes(in), n))}, 0
}

func (m *machine) expandArgv(words []Word) ([]string, error) {
	out := make([]string, len(words))
	for i, w := range words {
		s, err := m.expand(w)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func (m *machine) expand(w Word) (string, error) {
	var b strings.Builder
	for _, part := range w.Parts {
		if part.X != nil {
			d, err := m.evalX(part.X)
			if err != nil {
				return "", err
			}
			if isSecret(d) {
				m.sawSecret = true
			}
			b.WriteString(textOf(d))
			if b.Len() > dataMaxBytes {
				return "", fmt.Errorf("type value too large")
			}
			continue
		}
		if !part.Exp {
			b.WriteString(part.Lit)
			continue
		}
		// A whole row or row set is not one word, as in v1.
		if v, ok := m.vars[part.Lit]; ok && (v.kind == valRow || v.kind == valRows) {
			return "", fmt.Errorf("type value")
		}
		d, err := m.ref(part.Lit)
		if err != nil {
			return "", fmt.Errorf("type value")
		}
		if isSecret(d) {
			m.sawSecret = true
		}
		b.WriteString(textOf(d))
		if b.Len() > dataMaxBytes {
			return "", fmt.Errorf("type value too large")
		}
	}
	return b.String(), nil
}

// ref resolves a $ reference with its access path. A row is a map of its
// fields. A missing key is null.
func (m *machine) ref(lit string) (any, error) {
	name, steps := pathSteps(lit)
	v, ok := m.vars[name]
	if !ok {
		return nil, fmt.Errorf("type value")
	}
	d := toDatum(v)
	for _, s := range steps {
		if o, ok := d.(*omap); ok && m.st.nounset {
			if _, has := o.get(fmt.Sprint(s)); !has {
				return nil, fmt.Errorf("unset %s", lit)
			}
		}
		var err error
		if d, err = indexData(d, s); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// evalX computes a [ ] expression against the current names.
func (m *machine) evalX(x Expr) (any, error) {
	d, err := m.evalXRaw(x)
	if errors.Is(err, errSecretFault) {
		m.ctl = ctlHalt
	}
	return d, err
}

func (m *machine) evalXRaw(x Expr) (any, error) {
	return evalExprStrict(x, m.st.nounset, func(name string) (any, bool) {
		v, ok := m.vars[name]
		if !ok {
			return nil, false
		}
		return toDatum(v), true
	})
}

// exprOf is the expression when w is exactly one [ ] expression.
func exprOf(w Word) Expr {
	if len(w.Parts) == 1 {
		return w.Parts[0].X
	}
	return nil
}

func fromResult(res parent.Result) value {
	if res.Kind == parent.KindRows {
		return value{kind: valRows, rows: res.Rows, cols: append([]string(nil), res.Cols...)}
	}
	return value{kind: valBytes, b: res.Bytes}
}

// display is render for the terminal: a row table's cells go through
// needsCell, since rows carry host data (ps, journal, config) and session
// values (declare -p). Bytes, strings, and data stay byte-faithful, as
// echo and cat are; a pipe or a redirect gets render, unescaped.
func display(v value) string {
	if v.kind != valRows {
		return render(v)
	}
	cells := v
	cells.rows = make([]parent.Row, len(v.rows))
	for i, row := range v.rows {
		r := make(parent.Row, len(row))
		for k, c := range row {
			r[k] = needsCell(c)
		}
		cells.rows[i] = r
	}
	return render(cells)
}

func render(v value) string {
	switch v.kind {
	case valRows:
		cols := v.cols
		if len(cols) == 0 && len(v.rows) > 0 {
			for k := range v.rows[0] {
				cols = append(cols, k)
			}
			sort.Strings(cols)
		}
		var b strings.Builder
		for _, row := range v.rows {
			for i, c := range cols {
				if i > 0 {
					b.WriteString("  ")
				}
				b.WriteString(row[c])
			}
			b.WriteByte('\n')
		}
		return b.String()
	case valStr:
		return v.s
	case valData:
		return textOf(v.d) + "\n"
	default:
		return string(v.b)
	}
}

func asBytes(v value) string {
	if v.kind == valRows || v.kind == valRow || v.kind == valStr || v.kind == valData {
		return render(v)
	}
	return string(v.b)
}

// encodeJSON records a pipeline value. Rows become one JSON array, in column
// order. Bytes become one JSON string. > of that text is the record. > of a
// row value stays the text table.
func encodeJSON(v value) ([]byte, error) {
	if v.kind != valRows {
		raw, err := marshalJSONString(asBytes(v))
		if err != nil {
			return nil, err
		}
		return append(raw, '\n'), nil
	}
	cols := append([]string(nil), v.cols...)
	if len(cols) == 0 && len(v.rows) > 0 {
		for k := range v.rows[0] {
			cols = append(cols, k)
		}
		sort.Strings(cols)
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, row := range v.rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('{')
		for j, col := range cols {
			if j > 0 {
				b.WriteByte(',')
			}
			key, err := marshalJSONString(col)
			if err != nil {
				return nil, err
			}
			val, err := marshalJSONString(row[col])
			if err != nil {
				return nil, err
			}
			b.Write(key)
			b.WriteByte(':')
			b.Write(val)
		}
		b.WriteByte('}')
	}
	b.WriteString("]\n")
	return b.Bytes(), nil
}

func marshalJSONString(s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, fmt.Errorf("json utf8")
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func runEgrep(text, pat string) (value, int) {
	re := regexp.MustCompile(pat)
	var b strings.Builder
	n := 0
	for _, line := range splitLines(text) {
		if re.MatchString(line) {
			b.WriteString(line)
			b.WriteByte('\n')
			n++
		}
	}
	if n == 0 {
		return value{kind: valBytes}, 1
	}
	return value{kind: valBytes, b: []byte(b.String())}, 0
}

func runWhere(in value, field, op, want string) ([]parent.Row, error) {
	if in.kind != valRows {
		return nil, fmt.Errorf("type |")
	}
	var keep []parent.Row
	for _, row := range in.rows {
		ok, err := compare(row[field], op, want)
		if err != nil {
			return nil, err
		}
		if ok {
			keep = append(keep, row)
		}
	}
	return keep, nil
}

func compare(got, op, want string) (bool, error) {
	gi, gerr := strconv.Atoi(got)
	wi, werr := strconv.Atoi(want)
	if gerr == nil && werr == nil {
		switch op {
		case "==":
			return gi == wi, nil
		case "!=":
			return gi != wi, nil
		case ">":
			return gi > wi, nil
		case "<":
			return gi < wi, nil
		case ">=":
			return gi >= wi, nil
		case "<=":
			return gi <= wi, nil
		}
	}
	switch op {
	case "==":
		return got == want, nil
	case "!=":
		return got != want, nil
	case ">":
		return got > want, nil
	case "<":
		return got < want, nil
	case ">=":
		return got >= want, nil
	case "<=":
		return got <= want, nil
	default:
		return false, fmt.Errorf("usage where")
	}
}

func runCut(text string, args []string) string {
	delim := ""
	field := 1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d":
			if i+1 < len(args) {
				delim = args[i+1]
				i++
			}
		case "-f":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					field = n
				}
				i++
			}
		}
	}
	var b strings.Builder
	for _, line := range splitLines(text) {
		var parts []string
		if delim == "" {
			parts = strings.Fields(line)
		} else {
			parts = strings.Split(line, delim)
		}
		if field >= 1 && field <= len(parts) {
			b.WriteString(parts[field-1])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func headCount(argv []string) (int, error) {
	switch len(argv) {
	case 1:
		return 10, nil
	case 2:
		return strconv.Atoi(argv[1])
	case 3:
		if argv[1] != "-n" {
			return 0, fmt.Errorf("usage head")
		}
		return strconv.Atoi(argv[2])
	default:
		return 0, fmt.Errorf("usage head")
	}
}

func headLines(text string, n int) string {
	lines := splitLines(text)
	if n > len(lines) {
		n = len(lines)
	}
	if n < 0 {
		n = 0
	}
	if n == 0 {
		return ""
	}
	return strings.Join(lines[:n], "\n") + "\n"
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func resolvePath(prefix, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty")
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	if prefix == "" {
		return "", fmt.Errorf("prefix_unset")
	}
	return path.Clean(path.Join(prefix, p)), nil
}

// reservedNames cannot name a function. Every one is in the builtin table.
var reservedNames = map[string]bool{}

func init() {
	for _, n := range []string{"echo", "egrep", "head", "cut", "wc", "where", "select", "rows", "json",
		"ps", "unit", "journal", "package", "sysctl", "config", "cat", "ls",
		"cd", "pushd", "popd", "dirs", "let", "if", "for", "fn", "else", "in",
		"while", "do", "break", "continue", "return", "exit", "try", "catch", "why", "builtin",
		"set", "shopt", "declare", "from", "to", "use", "lines", "parse", "sub", "with", "template", "template_file",
		"retry", "timeout", "check", "checks", "log"} {
		reservedNames[n] = true
	}
}

func reserved(name string) bool { return reservedNames[name] || rowTools[name] }

// plainFirst is a stage's command name when it is a literal word.
func plainFirst(st Stage) string {
	if len(st.Argv) == 0 {
		return ""
	}
	return plain(st.Argv[0])
}

// runRows is the only lift from text to rows. No names keeps each line,
// spaces included, in the column line. Names split on whitespace. The last
// name keeps the remainder of the line, so a command with spaces stays one
// field. A short line leaves the missing fields empty.
func runRows(text string, names []string) value {
	lines := splitLines(text)
	if len(names) == 0 {
		rows := make([]parent.Row, len(lines))
		for i, line := range lines {
			rows[i] = parent.Row{"line": line}
		}
		return value{kind: valRows, rows: rows, cols: []string{"line"}}
	}
	cols := append([]string(nil), names...)
	rows := make([]parent.Row, len(lines))
	for i, line := range lines {
		rows[i] = splitNamed(line, cols)
	}
	return value{kind: valRows, rows: rows, cols: cols}
}

func splitNamed(line string, cols []string) parent.Row {
	row := make(parent.Row, len(cols))
	fields := strings.Fields(line)
	last := len(cols) - 1
	for i, col := range cols {
		if i == last {
			if i < len(fields) {
				row[col] = fieldRemainder(line, i)
			}
			break
		}
		if i < len(fields) {
			row[col] = fields[i]
		}
	}
	return row
}

func fieldRemainder(line string, n int) string {
	seen := 0
	i := 0
	for i < len(line) {
		r, w := utf8.DecodeRuneInString(line[i:])
		if unicode.IsSpace(r) {
			i += w
			continue
		}
		if seen == n {
			return line[i:]
		}
		for i < len(line) {
			r, w = utf8.DecodeRuneInString(line[i:])
			if unicode.IsSpace(r) {
				break
			}
			i += w
		}
		seen++
	}
	return ""
}

func rowName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !isNameStart(r) {
				return false
			}
			continue
		}
		if !isName(r) {
			return false
		}
	}
	return true
}

func isOp(s string) bool {
	switch s {
	case "==", "!=", ">", "<", ">=", "<=":
		return true
	default:
		return false
	}
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}
