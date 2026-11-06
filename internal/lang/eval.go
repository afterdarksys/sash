package lang

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
}

// Options carries the session prefix and the history newest-last.
// The process cwd is not this prefix. Trace prints each stage key.
type Options struct {
	Prefix  string
	History []string
	Trace   bool
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
		return Outcome{Status: exitType, Stderr: err.Error() + "\n", Prefix: opt.Prefix}
	}
	m := &machine{
		g:     g,
		p:     p,
		vars:  map[string]value{},
		fns:   map[string]*Fn{},
		trace: opt.Trace,
	}
	if opt.Prefix != "" {
		m.stack = []string{path.Clean(opt.Prefix)}
	}
	status, _ := m.runProg(prog)
	return Outcome{Status: status, Stdout: m.out.String(), Stderr: m.err.String(), Prefix: m.prefix()}
}

type value struct {
	kind int
	s    string
	b    []byte
	rows []parent.Row
	cols []string
	row  parent.Row
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
		st, stop := m.runStmt(s)
		status = st
		if stop {
			return status, true
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
		text, err := m.expand(*s.Word)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
		m.vars[s.Name] = value{kind: valStr, s: text}
		return 0, false
	}
	st, stop, v := m.runPipe(*s.Pipe)
	if stop {
		return st, true
	}
	m.vars[s.Name] = v
	return 0, false
}

func (m *machine) runIf(s *If) (int, bool) {
	st, stop, v := m.runPipe(s.Cond)
	if stop {
		return st, true
	}
	take := st == 0
	if v.kind == valRows {
		take = len(v.rows) > 0
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
	st, stop, v := m.runPipe(s.Src)
	if stop {
		return st, true
	}
	if v.kind != valRows {
		m.err.WriteString("type for\n")
		return exitType, true
	}
	status := 0
	for _, row := range v.rows {
		prev, had := m.vars[s.Name]
		m.vars[s.Name] = value{kind: valRow, row: row}
		st, stop = m.runProg(&s.Body)
		if had {
			m.vars[s.Name] = prev
		} else {
			delete(m.vars, s.Name)
		}
		status = st
		if stop {
			return status, true
		}
	}
	return status, false
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
		if exp[len(exp)-1].outPath == "" && exp[0].call == nil {
			m.out.WriteString(render(v))
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
	stop := st == exitDenied || st == exitUnknown || st == exitType
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
}

type problem struct {
	status int
	msg    string
}

func (m *machine) prepare(pipe Pipeline) ([]expStage, *problem) {
	exp := make([]expStage, len(pipe.Stages))
	for i, st := range pipe.Stages {
		argv, err := m.expandArgv(st.Argv)
		if err != nil {
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
		if info.call != nil && (len(pipe.Stages) != 1 || inPath != "" || outPath != "" || here != nil) {
			return nil, &problem{exitType, "type fn"}
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
			key: info.key, op: op, call: info.call, mover: info.mover,
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
	switch st.argv[0] {
	case "echo", "dirs", "cd", "pushd", "popd":
		return inNone, inBytes
	case "egrep", "cut":
		return inBytes, inBytes
	case "where", "select":
		return inRows, inRows
	case "rows":
		return inBytes, inRows
	case "json":
		return inAny, inBytes
	case "head", "wc":
		return inFlex, inFlex
	case "ps", "journal", "ls":
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
	if !m.trace {
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
		st, _ := m.callFn(exp[0])
		return st, value{kind: valBytes}
	}
	cur := value{kind: valBytes}
	if len(exp[0].here) > 0 {
		cur = value{kind: valBytes, b: append([]byte(nil), exp[0].here...)}
	} else if exp[0].inPath != "" {
		res, err := m.p.Do(parent.Verb{Key: "file.read", Argv: []string{"<", exp[0].inPath}, Path: exp[0].inPath})
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
		if err := m.traceStage(st); err != nil {
			return 1, cur
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
		if _, err := m.p.Do(parent.Verb{
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
	saved := map[string]value{}
	had := map[string]bool{}
	for i, name := range fn.Params {
		if old, ok := m.vars[name]; ok {
			saved[name] = old
			had[name] = true
		}
		m.vars[name] = value{kind: valStr, s: st.argv[i+1]}
	}
	status, stop := m.runProg(&fn.Body)
	for _, name := range fn.Params {
		if had[name] {
			m.vars[name] = saved[name]
		} else {
			delete(m.vars, name)
		}
	}
	return status, stop
}

func (m *machine) execStage(st expStage, in value) (value, int) {
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
			res, err := m.p.Do(parent.Verb{Key: "file.read", Argv: []string{"cat", pth}, Path: pth})
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
	res, err := m.p.Do(parent.Verb{Key: "file.read", Argv: []string{"stat", st.path}, Path: st.path})
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
	res, err := m.p.Do(parent.Verb{Key: st.key, Argv: st.argv, Path: st.path})
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
		if !part.Exp {
			b.WriteString(part.Lit)
			continue
		}
		name, field, hasField := strings.Cut(part.Lit, ".")
		v, ok := m.vars[name]
		if !ok {
			return "", fmt.Errorf("type value")
		}
		switch v.kind {
		case valStr:
			if hasField {
				return "", fmt.Errorf("type value")
			}
			b.WriteString(v.s)
		case valBytes:
			if hasField {
				return "", fmt.Errorf("type value")
			}
			b.WriteString(string(v.b))
		case valRow:
			if !hasField {
				return "", fmt.Errorf("type value")
			}
			b.WriteString(v.row[field])
		default:
			return "", fmt.Errorf("type value")
		}
	}
	return b.String(), nil
}

func fromResult(res parent.Result) value {
	if res.Kind == parent.KindRows {
		return value{kind: valRows, rows: res.Rows, cols: append([]string(nil), res.Cols...)}
	}
	return value{kind: valBytes, b: res.Bytes}
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
	default:
		return string(v.b)
	}
}

func asBytes(v value) string {
	if v.kind == valRows || v.kind == valRow || v.kind == valStr {
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

func reserved(name string) bool {
	switch name {
	case "echo", "egrep", "head", "cut", "wc", "where", "select", "rows", "json",
		"ps", "unit", "journal", "package", "sysctl", "config", "cat", "ls",
		"cd", "pushd", "popd", "dirs", "let", "if", "for", "fn", "else", "in":
		return true
	default:
		return false
	}
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
