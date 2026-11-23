package lang

// The service block and the rules of a service file
// (docs/plans/2026-11-21-sash-init-contract.md).
//
// Threats: a service file is read by the init with sash --describe, which
// runs nothing, and run with sash --session --entry, which runs the top
// level and then one function. A stray top-level statement would run on
// --entry before the function, so the parser refuses anything but
// service, args, fn, literal let and declare, and use (service_top_level).
// The block takes literal values only, checked here, so --describe can
// print it without running a word. Nothing here reads the host.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
)

// Service is the static service { } block: what the init reads without
// running anything.
type Service struct {
	Name        string
	Description string
	Requires    []string
	After       []string
	Wants       []string
	User        string
	Group       string
	// Run is the program and its arguments for a purely declarative
	// service; empty when the file has none.
	Run     []string
	Ready   *ServiceReady
	Stop    *ServiceStop
	Restart *ServiceRestart
}

func (*Service) stmt() {}

// ServiceReady is ready KIND [TARGET] [timeout D]: tcp HOST:PORT, unix
// PATH, file PATH, notify, or none.
type ServiceReady struct {
	Kind    string
	Target  string
	Timeout time.Duration
}

// ServiceStop is stop signal SIG timeout D: the signal, and the wait before
// the init sends KILL.
type ServiceStop struct {
	Signal  string
	Timeout time.Duration
}

// ServiceRestart is restart never|on-failure|always [delay D] [burst N
// within D].
type ServiceRestart struct {
	Policy string
	Delay  time.Duration
	Burst  int
	Within time.Duration
}

// lifecycleEntries are the functions --entry calls. run is implicit (a
// service with run and no fn start); it cannot be defined.
var lifecycleEntries = map[string]bool{"start": true, "stop": true, "reload": true, "check": true}

// unixName is a user or group name.
var unixName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// spawnSignals are the signals stop, signal and the service block name.
var spawnSignals = map[string]bool{
	"HUP": true, "INT": true, "QUIT": true, "KILL": true, "USR1": true, "USR2": true,
	"TERM": true, "CONT": true, "STOP": true, "WINCH": true,
}

// serviceDuration is a literal Go duration in (0, 24h].
func serviceDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || d > 24*time.Hour {
		return 0, fmt.Errorf("parse: service duration %q: up to 24h", s)
	}
	return d, nil
}

// service reads service { key values... }. Every value is a literal word;
// each key at most once.
func (p *parser) service() (Stmt, error) {
	p.i++
	if p.peek().kind != kLBrace {
		return nil, fmt.Errorf("parse: service needs {")
	}
	p.i++
	p.skipSep()
	s := &Service{}
	seen := map[string]bool{}
	for p.peek().kind != kRBrace {
		if p.peek().kind == kEOF {
			return nil, fmt.Errorf("parse: service needs }")
		}
		key, err := p.plainName("service key")
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, fmt.Errorf("parse: service key %s given twice", key)
		}
		seen[key] = true
		var vals []string
		for !p.atSep() && p.peek().kind != kRBrace && p.peek().kind != kEOF {
			t := p.peek()
			if t.kind != kWord || len(t.word.Parts) != 1 || t.word.Parts[0].Exp || t.word.Parts[0].X != nil {
				return nil, fmt.Errorf("parse: service needs literal values")
			}
			// Expand would glob or brace-expand them before a run, as for args.
			if strings.ContainsAny(t.word.Parts[0].Lit, "*{}") {
				return nil, fmt.Errorf("parse: service needs literal values without * { }")
			}
			vals = append(vals, t.word.Parts[0].Lit)
			p.i++
		}
		if err := s.set(key, vals); err != nil {
			return nil, err
		}
		p.skipSep()
	}
	p.i++
	if s.Name == "" {
		return nil, fmt.Errorf("parse: service needs a name")
	}
	return s, nil
}

func (s *Service) set(key string, v []string) error {
	bad := func() error { return fmt.Errorf("parse: service %s: bad value", key) }
	one := func() (string, error) {
		if len(v) != 1 {
			return "", bad()
		}
		return v[0], nil
	}
	var err error
	switch key {
	case "name":
		if s.Name, err = one(); err != nil || !grant.ServiceName.MatchString(s.Name) {
			return bad()
		}
	case "description":
		if s.Description, err = one(); err != nil || s.Description == "" || len(s.Description) > 256 || needsCell(s.Description) != s.Description {
			return bad()
		}
	case "requires", "after", "wants":
		if len(v) == 0 {
			return bad()
		}
		dup := map[string]bool{}
		for _, n := range v {
			if !grant.ServiceName.MatchString(n) || dup[n] {
				return bad()
			}
			dup[n] = true
		}
		switch key {
		case "requires":
			s.Requires = v
		case "after":
			s.After = v
		default:
			s.Wants = v
		}
	case "user", "group":
		n, err := one()
		if err != nil || !unixName.MatchString(n) {
			return bad()
		}
		if key == "user" {
			s.User = n
		} else {
			s.Group = n
		}
	case "run":
		if len(v) == 0 || spawnProgram(v[0]) != "" {
			return bad()
		}
		for _, a := range v[1:] {
			if strings.ContainsRune(a, 0) || len(a) > 4096 {
				return bad()
			}
		}
		s.Run = v
	case "ready":
		r, err := serviceReady(v)
		if err != nil {
			return bad()
		}
		s.Ready = r
	case "stop":
		if len(v) != 4 || v[0] != "signal" || !spawnSignals[v[1]] || v[2] != "timeout" {
			return bad()
		}
		d, err := serviceDuration(v[3])
		if err != nil {
			return bad()
		}
		s.Stop = &ServiceStop{Signal: v[1], Timeout: d}
	case "restart":
		r, err := serviceRestart(v)
		if err != nil {
			return bad()
		}
		s.Restart = r
	default:
		return fmt.Errorf("parse: unknown service key %s", key)
	}
	return nil
}

func serviceReady(v []string) (*ServiceReady, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("ready")
	}
	r := &ServiceReady{Kind: v[0]}
	rest := v[1:]
	switch r.Kind {
	case "none":
		if len(rest) != 0 {
			return nil, fmt.Errorf("ready")
		}
		return r, nil
	case "notify":
	case "tcp":
		if len(rest) == 0 || probeTCP(rest[0], true) != "" {
			return nil, fmt.Errorf("ready")
		}
		r.Target, rest = rest[0], rest[1:]
	case "unix", "file":
		if len(rest) == 0 {
			return nil, fmt.Errorf("ready")
		}
		if _, err := makePath("", rest[0]); err != nil {
			return nil, fmt.Errorf("ready")
		}
		r.Target, rest = rest[0], rest[1:]
	default:
		return nil, fmt.Errorf("ready")
	}
	if len(rest) != 2 || rest[0] != "timeout" {
		return nil, fmt.Errorf("ready")
	}
	d, err := serviceDuration(rest[1])
	if err != nil {
		return nil, err
	}
	r.Timeout = d
	return r, nil
}

func serviceRestart(v []string) (*ServiceRestart, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("restart")
	}
	r := &ServiceRestart{Policy: v[0]}
	switch r.Policy {
	case "never":
		if len(v) != 1 {
			return nil, fmt.Errorf("restart")
		}
		return r, nil
	case "on-failure", "always":
	default:
		return nil, fmt.Errorf("restart")
	}
	rest := v[1:]
	if len(rest) >= 2 && rest[0] == "delay" {
		d, err := serviceDuration(rest[1])
		if err != nil {
			return nil, err
		}
		r.Delay, rest = d, rest[2:]
	}
	if len(rest) >= 4 && rest[0] == "burst" && rest[2] == "within" {
		n, err := strconv.Atoi(rest[1])
		if err != nil || n < 1 || n > 1000 {
			return nil, fmt.Errorf("restart")
		}
		d, err := serviceDuration(rest[3])
		if err != nil {
			return nil, err
		}
		r.Burst, r.Within, rest = n, d, rest[4:]
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("restart")
	}
	return r, nil
}

// serviceOf is the program's service block, or nil.
func serviceOf(prog *Program) *Service {
	if prog == nil || len(prog.Stmts) == 0 {
		return nil
	}
	s, _ := prog.Stmts[0].(*Service)
	return s
}

// checkServiceFile holds a file with a service block to its rules: the top
// level only defines (service_top_level), lifecycle functions take no
// parameters, run is implicit, and run with fn start is
// service_run_and_start.
func checkServiceFile(prog *Program) error {
	svc := serviceOf(prog)
	if svc == nil {
		return nil
	}
	top := func(what string) error {
		return fmt.Errorf("parse: service_top_level: %s at the top level of a service file; only service, args, fn, let and declare of a literal, and use", what)
	}
	start := false
	for _, s := range prog.Stmts[1:] {
		switch s := s.(type) {
		case *Arguments, *Use:
		case *Fn:
			if s.Name == "run" {
				return fmt.Errorf("parse: fn run: run is the implicit entry of a service file")
			}
			if lifecycleEntries[s.Name] && len(s.Params) != 0 {
				return fmt.Errorf("parse: fn %s: a lifecycle function takes no parameters", s.Name)
			}
			if s.Name == "start" {
				start = true
			}
		case *Let:
			if !literalLet(s) {
				return top("let of a value")
			}
		case *Declare:
			if s.Name == "" || (s.Value != nil && !literalLet(s.Value)) {
				return top("declare of a value")
			}
		default:
			return top("a statement")
		}
	}
	if start && len(svc.Run) > 0 {
		return fmt.Errorf("parse: service_run_and_start: a service with fn start has no run")
	}
	return nil
}

// literalLet is a let whose value is a literal word or a [ ] of literals.
func literalLet(l *Let) bool {
	if l.Pipe != nil || l.Capture || l.Word == nil {
		return false
	}
	w := *l.Word
	if len(w.Parts) != 1 || w.Parts[0].Exp {
		return false
	}
	if w.Parts[0].X != nil {
		return literalExpr(w.Parts[0].X, 0)
	}
	return !strings.ContainsAny(w.Parts[0].Lit, "*{}")
}

func literalExpr(x Expr, depth int) bool {
	if depth > exprDepthMax {
		return false
	}
	switch x := x.(type) {
	case exprLit:
		return true
	case exprList:
		for _, it := range x.items {
			if !literalExpr(it, depth+1) {
				return false
			}
		}
		return true
	case exprMap:
		for _, v := range x.vals {
			if !literalExpr(v, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

// entryRefused checks an entry before anything runs, expansion included:
// the file has a service block, its name is the grant's service (when a
// grant was delivered), and a boot grant never runs without an entry.
func entryRefused(src string, g grant.Grant, opt Options) (Outcome, bool) {
	fail := func(msg string) (Outcome, bool) {
		return Outcome{Status: exitType, Stderr: msg + "\n", Prefix: opt.Prefix}, true
	}
	if opt.Entry == "" {
		if g.Purpose == grant.PurposeService {
			return fail("entry_missing: a boot grant runs only a service entry")
		}
		return Outcome{}, false
	}
	if !lifecycleEntries[opt.Entry] && opt.Entry != "run" {
		return fail("entry_missing: " + needsCell(opt.Entry))
	}
	svc, err := leadingService(src)
	if err != nil {
		return fail(safeText(err.Error()))
	}
	if svc == nil {
		return fail("entry_missing: the file has no service block")
	}
	if g.V != "" {
		if err := grant.CheckService(g, svc.Name); err != nil {
			return fail(err.Error())
		}
		if err := grant.CheckIdentity(g, svc.User, svc.Group); err != nil {
			return fail(err.Error())
		}
	}
	return Outcome{}, false
}

// msgServiceGlob refuses a glob or a brace pattern in a service file: a run
// expands them before the top level, so they would read the host at load.
const msgServiceGlob = "service_glob: a service file has no globs or brace patterns"

// serviceGlob is a problem when src is a service file holding a glob or a
// brace pattern anywhere. It runs before Expand and reads nothing.
func serviceGlob(src string) *ExpandProblem {
	if svc, err := leadingService(src); err != nil || svc == nil {
		return nil
	}
	refuse := &ExpandProblem{Status: exitType, Msg: msgServiceGlob}
	if out, prob := expandBraces(src); prob != nil || out != src {
		return refuse
	}
	if _, prob := globWords(src, func(string) (string, *ExpandProblem) { return "", refuse }); prob != nil {
		return refuse
	}
	return nil
}

// leadingService parses only the service block at the top of src.
func leadingService(src string) (*Service, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{tok: toks}
	p.skipSep()
	if p.peek().kind != kWord || plain(p.peek().word) != "service" || p.i+1 >= len(p.tok) || p.tok[p.i+1].kind != kLBrace {
		return nil, nil
	}
	s, err := p.service()
	if err != nil {
		return nil, err
	}
	return s.(*Service), nil
}

// runEntry calls the lifecycle function after the top level ran. check runs
// under the await rules: every mutation is refused, plan mode or not. run
// is the implicit entry: spawn main with the block's run, user and group.
func (m *machine) runEntry(entry string) (int, bool) {
	missing := func() (int, bool) {
		m.err.WriteString("entry_missing: " + entry + "\n")
		return exitType, true
	}
	var fn *Fn
	switch entry {
	case "check":
		fn = m.checkFn
	case "run":
		if m.svc == nil || len(m.svc.Run) == 0 || m.fns["start"] != nil {
			return missing()
		}
		return m.implicitRun()
	default:
		fn = m.fns[entry]
	}
	if fn == nil {
		return missing()
	}
	if entry == "check" {
		polling, depth := m.polling, m.pollDepth
		m.polling, m.pollDepth = true, -1
		defer func() { m.polling, m.pollDepth = polling, depth }()
	}
	return m.callFn(expStage{argv: []string{entry}, call: fn})
}

// implicitRun is spawn main <run...>, checked as a spawn statement is.
func (m *machine) implicitRun() (int, bool) {
	argv := append([]string{"spawn", "main"}, m.svc.Run...)
	info, prob, _ := classifySvc(argv)
	if prob == nil && (!m.g.Has(info.key) || !m.g.Boot()) {
		prob = &problem{exitDenied, "denied " + info.key}
	}
	st := expStage{argv: argv, key: info.key}
	if prob == nil {
		_, prob = m.bindSvc(&st)
	}
	if prob != nil {
		m.err.WriteString(prob.msg + "\n")
		return prob.status, true
	}
	_, code := m.runSpawn(st)
	return code, code != 0
}

// entryStatus is a run's status as the init reads it: 0 done, 1 failed, 2
// a configuration or grant error (a denial, a usage or type error, an
// unknown name; the init does not restart), 3 start not ready in time.
func entryStatus(entry string, st int, awaitTimedOut bool) int {
	switch {
	case st == 0:
		return 0
	case entry == "start" && awaitTimedOut:
		return 3
	case st == exitType || st == exitDenied || st == exitUnknown:
		return exitType
	}
	return 1
}
