package lang

// Lint is sash --lint (docs/plans/2026-11-12-sash-language-v5.md section 2).
//
// Threats: lint parses the script the way Needs does and reads nothing
// else. It runs nothing, calls no parent, opens no file, and does not
// follow use, so a hostile script cannot make it touch the host. It is
// advisory: a clean lint never means safe, and run-time taint (secretVal,
// evalCtx.tainted, machine.sawSecret, decide) stays the only enforcement.
// The dangerous failure is a missed secret sink, so secret-to-sink is
// flow-insensitive and over-reports: a name bound from anything that
// references a secret is secret everywhere in the file. Every printed cell
// goes through needsCell, and what names bindings and commands, never a
// value, so no tainted literal is printed. Errors cannot be suppressed.

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
)

const (
	lintInfo    = "info"
	lintWarning = "warning"
	lintError   = "error"
)

// lintRules is every rule and its severity. incomplete-needs is an error
// for command_from_value and a warning otherwise.
var lintRules = map[string]string{
	"unused-binding":    lintInfo,
	"unreachable":       lintWarning,
	"constant-check":    lintWarning,
	"retry-on-mutation": lintWarning,
	"secret-to-sink":    lintError,
	"silent-catch":      lintWarning,
	"incomplete-needs":  lintWarning,
	"shadowed-secret":   lintWarning,
}

// lintFnDepth bounds how far retry-on-mutation follows fn calls.
const lintFnDepth = 8

// lintPropagates are the stages that only carry a value along a value
// pipe (let, declare, with): a secret through them taints
// the binding and is not a sink there. Every other stage a secret reaches
// in a value pipe is a sink, and so is every stage in a pipe whose output
// is shown or decides a branch.
var lintPropagates = map[string]bool{
	"echo": true, "head": true, "cut": true, "wc": true, "select": true, "rows": true,
	"json": true, "sort": true, "uniq": true, "group": true, "count": true, "sum": true,
	"avg": true, "min": true, "max": true, "first": true, "last": true, "skip": true,
	"each": true, "lines": true, "parse": true, "sub": true, "from": true, "to": true,
}

type lintFinding struct {
	line           int
	rule, sev, why string
}

type lintBinding struct {
	name, kind string
	at         int
	top        bool
	readonly   bool
	secret     bool // declare -s binds it
}

type linter struct {
	n        *needsRun
	prog     *Program
	fns      map[string]*Fn
	bindings []lintBinding
	reads    map[string]bool
	secrets  map[string]bool // names bound or marked by declare -s
	tainted  map[string]bool
	deps     map[string][]string // taint edges: a name to the names it taints
	mutated  map[string]string   // retry-on-mutation per fn and depth
	hasUse   bool
	tries    int
	out      []lintFinding
}

// Lint reports the rules in the table for src. Exit 0 is none or info
// only, 1 is any warning or error, 2 is a usage or parse error with
// nothing on stdout.
func Lint(src string) Outcome {
	var comments []comment
	n, _, prog, err := needsParse(src, &comments)
	if err != nil {
		return Outcome{Status: exitType, Stderr: safeText(err.Error()) + "\n"}
	}
	l := &linter{
		n: n, prog: prog, fns: n.fns, reads: map[string]bool{}, secrets: map[string]bool{},
		tainted: map[string]bool{}, deps: map[string][]string{}, mutated: map[string]string{},
	}
	l.collect(prog, true, "")
	l.taint()
	l.sinks(prog)
	l.unused()
	l.shadowed()
	l.blocks(prog)
	l.needs()
	return l.report(comments)
}

func (l *linter) add(at int, rule, why string) {
	l.addSev(at, rule, lintRules[rule], why)
}

func (l *linter) addSev(at int, rule, sev, why string) {
	l.out = append(l.out, lintFinding{line: l.n.line(at), rule: rule, sev: sev, why: why})
}

// lintEach calls fn on every statement at any depth with its offset and
// whether it is top level. Fn bodies are included: lint reads every line.
func lintEach(p *Program, top bool, fn func(s Stmt, at int, top bool)) {
	if p == nil {
		return
	}
	for i, s := range p.Stmts {
		at := 0
		if i < len(p.Pos) {
			at = p.Pos[i]
		}
		fn(s, at, top)
		for _, body := range lintBodies(s) {
			lintEach(body, false, fn)
		}
	}
}

func lintBodies(s Stmt) []*Program {
	switch s := s.(type) {
	case *If:
		return []*Program{&s.Then, s.Else}
	case *For:
		return []*Program{&s.Body}
	case *While:
		return []*Program{&s.Body}
	case *Try:
		return []*Program{&s.Body, s.Catch}
	case *With:
		return []*Program{&s.Body}
	case *Retry:
		return []*Program{&s.Body}
	case *Timeout:
		return []*Program{&s.Body}
	case *Fn:
		return []*Program{&s.Body}
	}
	return nil
}

// Name references: $name words (the name before any access path), names
// in [ ] expressions, and for's $ source.

func exprRefs(x Expr, out map[string]bool) {
	switch x := x.(type) {
	case exprRef:
		out[x.name] = true
	case exprList:
		for _, it := range x.items {
			exprRefs(it, out)
		}
	case exprMap:
		for _, v := range x.vals {
			exprRefs(v, out)
		}
	case exprUnary:
		exprRefs(x.x, out)
	case exprBinary:
		exprRefs(x.l, out)
		exprRefs(x.r, out)
	case exprIndex:
		exprRefs(x.x, out)
		exprRefs(x.key, out)
	case exprRange:
		exprRefs(x.lo, out)
		exprRefs(x.hi, out)
	case exprCall:
		for _, a := range x.args {
			exprRefs(a, out)
		}
	}
}

func wordRefs(w Word, out map[string]bool) {
	for _, p := range w.Parts {
		if p.X != nil {
			exprRefs(p.X, out)
		} else if p.Exp {
			name, _ := pathSteps(p.Lit)
			out[name] = true
		}
	}
}

func stageRefs(st Stage, out map[string]bool) {
	for _, w := range st.Argv {
		wordRefs(w, out)
	}
	for _, r := range st.Redirs {
		wordRefs(r.Path, out)
	}
}

func pipeRefs(p Pipeline, out map[string]bool) {
	for _, st := range p.Stages {
		stageRefs(st, out)
	}
}

func letRefs(l *Let, out map[string]bool) {
	if l.Word != nil {
		wordRefs(*l.Word, out)
	}
	if l.Pipe != nil {
		pipeRefs(*l.Pipe, out)
	}
}

// ownRefs are the names a statement reads itself, not in nested bodies.
func ownRefs(s Stmt, out map[string]bool) {
	switch s := s.(type) {
	case *Chain:
		for _, p := range s.Pipes {
			pipeRefs(p, out)
		}
	case *Let:
		letRefs(s, out)
	case *Declare:
		if s.Value != nil {
			letRefs(s.Value, out)
		}
		if strings.Contains(s.Flags, "p") && s.Name != "" {
			out[s.Name] = true
		}
	case *If:
		exprRefs(s.CondX, out)
		pipeRefs(s.Cond, out)
	case *For:
		exprRefs(s.SrcX, out)
		if s.SrcRef != "" {
			name, _ := pathSteps(s.SrcRef)
			out[name] = true
		}
		pipeRefs(s.Src, out)
	case *While:
		exprRefs(s.CondX, out)
		pipeRefs(s.Cond, out)
	case *Jump:
		if s.Word != nil {
			wordRefs(*s.Word, out)
		}
	case *With:
		if s.Prefix != nil {
			wordRefs(*s.Prefix, out)
		}
		for _, l := range s.Lets {
			letRefs(l, out)
		}
	case *Retry:
		wordRefs(s.N, out)
		if s.Delay != nil {
			wordRefs(*s.Delay, out)
		}
	case *Timeout:
		wordRefs(s.D, out)
	case *Use:
		wordRefs(s.Path, out)
	}
}

// ownWords calls w for every word and x for every bare [ ] condition or
// source a statement evaluates itself, not in nested bodies; for's $
// source is passed as a word.
func ownWords(s Stmt, w func(Word), x func(Expr)) {
	pipe := func(p Pipeline) {
		for _, st := range p.Stages {
			for _, a := range st.Argv {
				w(a)
			}
			for _, r := range st.Redirs {
				w(r.Path)
			}
		}
	}
	let := func(l *Let) {
		if l == nil {
			return
		}
		if l.Word != nil {
			w(*l.Word)
		}
		if l.Pipe != nil {
			pipe(*l.Pipe)
		}
	}
	switch s := s.(type) {
	case *Chain:
		for _, p := range s.Pipes {
			pipe(p)
		}
	case *Let:
		let(s)
	case *Declare:
		let(s.Value)
	case *If:
		if s.CondX != nil {
			x(s.CondX)
		}
		pipe(s.Cond)
	case *For:
		if s.SrcX != nil {
			x(s.SrcX)
		}
		if s.SrcRef != "" {
			w(Word{Parts: []Part{{Lit: s.SrcRef, Exp: true}}})
		}
		pipe(s.Src)
	case *While:
		if s.CondX != nil {
			x(s.CondX)
		}
		pipe(s.Cond)
	case *Jump:
		if s.Word != nil {
			w(*s.Word)
		}
	case *With:
		if s.Prefix != nil {
			w(*s.Prefix)
		}
		for _, l := range s.Lets {
			let(l)
		}
	case *Retry:
		w(s.N)
		if s.Delay != nil {
			w(*s.Delay)
		}
	case *Timeout:
		w(s.D)
	case *Use:
		w(s.Path)
	}
}

// exprMayFail reports whether evaluating x can fail on a value's type or
// shape: an index, a call, an operator, or a range anywhere in it. A
// literal, a bare reference, and a list or map of those cannot.
func exprMayFail(x Expr) bool {
	switch x := x.(type) {
	case exprIndex, exprCall, exprBinary, exprUnary, exprRange:
		return true
	case exprList:
		for _, it := range x.items {
			if exprMayFail(it) {
				return true
			}
		}
	case exprMap:
		for _, v := range x.vals {
			if exprMayFail(v) {
				return true
			}
		}
	}
	return false
}

// faults is the secret a statement reads where an evaluation can fail: a
// run ends the line on such an error (errSecretFault, exit 2), which
// try cannot catch, so lint counts it as a refusal it must report. An
// access path ($tok.a, $tok[0]) or any [ ] that may fail and reads a
// secret qualifies.
func (l *linter) faults(s Stmt) string {
	found := ""
	note := func(name string) {
		if found == "" || name < found {
			found = name
		}
	}
	expr := func(x Expr) {
		if exprMayFail(x) {
			if name := l.anyTainted(refsOf(func(o map[string]bool) { exprRefs(x, o) })); name != "" {
				note(name)
			}
		}
	}
	ownWords(s, func(w Word) {
		for _, p := range w.Parts {
			if p.X != nil {
				expr(p.X)
			} else if p.Exp {
				if name, steps := pathSteps(p.Lit); len(steps) > 0 && l.tainted[name] {
					note(name)
				}
			}
		}
	}, expr)
	return found
}

func refsOf(f func(map[string]bool)) map[string]bool {
	out := map[string]bool{}
	f(out)
	return out
}

// collect finds every binding, every read, and the secret names, and
// records each taint edge once: a name read by a binding's value, and
// every name a try body reads, to its catch name. A try with a catch name
// is a node: a read in its body taints the node, the node taints the
// catch name and the node of the try around it. tryNode is the innermost
// such node around p, so a body is walked once and a read adds one edge
// however many tries enclose it.
func (l *linter) collect(p *Program, top bool, tryNode string) {
	if p == nil {
		return
	}
	for i, s := range p.Stmts {
		at := 0
		if i < len(p.Pos) {
			at = p.Pos[i]
		}
		own := map[string]bool{}
		ownRefs(s, own)
		for r := range own {
			l.reads[r] = true
			if tryNode != "" {
				l.dep(r, tryNode)
			}
		}
		bind := func(name, kind string, refs map[string]bool) *lintBinding {
			l.bindings = append(l.bindings, lintBinding{name: name, kind: kind, at: at, top: top})
			for r := range refs {
				l.dep(r, name)
			}
			return &l.bindings[len(l.bindings)-1]
		}
		switch s := s.(type) {
		case *Use:
			l.hasUse = true
		case *Let:
			bind(s.Name, "let", refsOf(func(o map[string]bool) { letRefs(s, o) }))
		case *Declare:
			if s.Name != "" && strings.Contains(s.Flags, "s") {
				l.secrets[s.Name] = true
			}
			if s.Name != "" && s.Value != nil {
				b := bind(s.Name, "declare", refsOf(func(o map[string]bool) { letRefs(s.Value, o) }))
				b.readonly = strings.Contains(s.Flags, "r")
				b.secret = strings.Contains(s.Flags, "s")
			}
		case *For:
			bind(s.Name, "for", own)
			if s.Name2 != "" {
				bind(s.Name2, "for", own)
			}
		case *Try:
			inner := tryNode
			if s.Name != "" {
				// A failure message can carry what the body read. The
				// node name has a NUL, so it is never a script name.
				bind(s.Name, "catch", nil)
				l.tries++
				inner = fmt.Sprintf("\x00try%d", l.tries)
				l.dep(inner, s.Name)
				if tryNode != "" {
					l.dep(inner, tryNode)
				}
			}
			l.collect(&s.Body, false, inner)
			l.collect(s.Catch, false, tryNode)
			continue
		case *With:
			for _, w := range s.Lets {
				bind(w.Name, "with", refsOf(func(o map[string]bool) { letRefs(w, o) }))
			}
		case *Fn:
			for _, prm := range s.Params {
				// Params are tainted from call sites in taint.
				bind(prm, "parameter", nil)
			}
		}
		for _, body := range lintBodies(s) {
			l.collect(body, false, tryNode)
		}
	}
}

// dep records that a taint on from taints to.
func (l *linter) dep(from, to string) {
	l.deps[from] = append(l.deps[from], to)
}

func (l *linter) anyTainted(refs map[string]bool) string {
	names := make([]string, 0, len(refs))
	for n := range refs {
		if l.tainted[n] {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0]
}

// taint is the flow-insensitive fixpoint: declare -s names, then any name
// bound from something that references a tainted name, and any fn
// parameter a call passes a tainted word to. It is a worklist over the
// edges collect and the calls record: each name is taken once and each
// edge followed once, so a long chain in any order is linear.
func (l *linter) taint() {
	lintEach(l.prog, true, func(s Stmt, _ int, _ bool) {
		lintEachPipe(s, func(p Pipeline, _ string) {
			for _, st := range p.Stages {
				fn, ok := l.fns[plain(st.Argv[0])]
				if !ok {
					continue
				}
				for i, w := range st.Argv[1:] {
					if i >= len(fn.Params) {
						break
					}
					for r := range refsOf(func(o map[string]bool) { wordRefs(w, o) }) {
						l.dep(r, fn.Params[i])
					}
				}
			}
		})
	})
	var work []string
	for n := range l.secrets {
		l.tainted[n] = true
		work = append(work, n)
	}
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		for _, d := range l.deps[n] {
			if !l.tainted[d] {
				l.tainted[d] = true
				work = append(work, d)
			}
		}
	}
}

// Pipe roles: a shown pipe prints or decides, so every stage a secret
// reaches is a sink; a value pipe binds a name, so only stages outside
// lintPropagates are.
const (
	pipeShown = "shown"
	pipeValue = "value"
	pipeCond  = "cond"
)

func lintEachPipe(s Stmt, fn func(p Pipeline, role string)) {
	switch s := s.(type) {
	case *Chain:
		for _, p := range s.Pipes {
			fn(p, pipeShown)
		}
	case *Let:
		if s.Pipe != nil {
			fn(*s.Pipe, pipeValue)
		}
	case *Declare:
		if s.Value != nil && s.Value.Pipe != nil {
			fn(*s.Value.Pipe, pipeValue)
		}
	case *If:
		if s.CondX == nil {
			fn(s.Cond, pipeCond)
		}
	case *While:
		if s.CondX == nil {
			fn(s.Cond, pipeCond)
		}
	case *For:
		// A run refuses to iterate a secret (type for secret): the
		// source decides how often the body runs.
		if s.SrcX == nil && s.SrcRef == "" {
			fn(s.Src, pipeCond)
		}
	case *With:
		for _, w := range s.Lets {
			if w.Pipe != nil {
				fn(*w.Pipe, pipeValue)
			}
		}
	}
}

func stageName(st Stage) string {
	if name := plain(st.Argv[0]); name != "" {
		return name
	}
	return "a command from a value"
}

// sinks is secret-to-sink.
func (l *linter) sinks(p *Program) {
	lintEach(p, true, func(s Stmt, at int, _ bool) {
		lintEachPipe(s, func(pl Pipeline, role string) {
			flowing := ""
			for _, st := range pl.Stages {
				if name := l.anyTainted(refsOf(func(o map[string]bool) { stageRefs(st, o) })); name != "" && flowing == "" {
					flowing = name
				}
				if flowing == "" {
					continue
				}
				redir := l.anyTainted(refsOf(func(o map[string]bool) {
					for _, r := range st.Redirs {
						wordRefs(r.Path, o)
					}
				}))
				switch {
				case redir != "":
					l.add(st.Argv[0].Pos, "secret-to-sink", "secret "+redir+" reaches a redirect path")
				case role == pipeValue && lintPropagates[plain(st.Argv[0])] && len(st.Redirs) == 0:
				case role == pipeCond:
					l.add(st.Argv[0].Pos, "secret-to-sink", "secret "+flowing+" decides a condition through "+stageName(st))
				default:
					l.add(st.Argv[0].Pos, "secret-to-sink", "secret "+flowing+" reaches "+stageName(st))
				}
			}
		})
		if name := l.faults(s); name != "" {
			l.add(at, "secret-to-sink", "secret "+name+" is in an evaluation that may fail and end the line")
		}
		own := func(what string, f func(map[string]bool)) {
			if name := l.anyTainted(refsOf(f)); name != "" {
				l.add(at, "secret-to-sink", "secret "+name+" "+what)
			}
		}
		// A word that is not one $name or one [ ] makes text, and a run
		// refuses a secret in it.
		word := func(lt *Let) {
			if lt == nil || lt.Word == nil {
				return
			}
			if w := *lt.Word; len(w.Parts) == 1 && (w.Parts[0].Exp || w.Parts[0].X != nil) {
				return
			}
			own("is built into a word", func(o map[string]bool) { wordRefs(*lt.Word, o) })
		}
		switch s := s.(type) {
		case *Let:
			word(s)
		case *If:
			own("decides an if", func(o map[string]bool) { exprRefs(s.CondX, o) })
		case *While:
			kind := "a while"
			if s.Do {
				kind = "a do"
			}
			own("decides "+kind, func(o map[string]bool) { exprRefs(s.CondX, o) })
		case *For:
			own("decides a for", func(o map[string]bool) {
				exprRefs(s.SrcX, o)
				if s.SrcRef != "" {
					name, _ := pathSteps(s.SrcRef)
					o[name] = true
				}
			})
		case *Jump:
			if s.Word != nil {
				own("reaches "+s.Kind, func(o map[string]bool) { wordRefs(*s.Word, o) })
			}
		case *Declare:
			word(s.Value)
			if strings.Contains(s.Flags, "p") && l.tainted[s.Name] {
				l.add(at, "secret-to-sink", "declare -p prints secret "+s.Name)
			}
		case *Use:
			own("reaches a use path", func(o map[string]bool) { wordRefs(s.Path, o) })
		case *With:
			if s.Prefix != nil {
				own("reaches a with prefix", func(o map[string]bool) { wordRefs(*s.Prefix, o) })
			}
			for _, lt := range s.Lets {
				word(lt)
			}
		case *Retry:
			own("reaches a retry count or delay", func(o map[string]bool) {
				wordRefs(s.N, o)
				if s.Delay != nil {
					wordRefs(*s.Delay, o)
				}
			})
		case *Timeout:
			own("reaches a timeout", func(o map[string]bool) { wordRefs(s.D, o) })
		}
	})
}

func (l *linter) unused() {
	for _, b := range l.bindings {
		if l.reads[b.name] || strings.HasPrefix(b.name, "_") || b.readonly || (b.top && l.hasUse) {
			continue
		}
		l.add(b.at, "unused-binding", b.kind+" "+b.name+" is never read")
	}
}

func (l *linter) shadowed() {
	for _, b := range l.bindings {
		if l.secrets[b.name] && !b.secret {
			l.add(b.at, "shadowed-secret", "declare -s "+b.name+" is also bound by "+b.kind)
		}
	}
}

// blocks is unreachable, constant-check, retry-on-mutation, and
// silent-catch.
func (l *linter) blocks(p *Program) {
	var walk func(p *Program)
	walk = func(p *Program) {
		if p == nil {
			return
		}
		for i, s := range p.Stmts {
			if j, ok := s.(*Jump); ok && i+1 < len(p.Stmts) && i+1 < len(p.Pos) {
				l.add(p.Pos[i+1], "unreachable", "statement after "+j.Kind)
				break
			}
		}
		for _, s := range p.Stmts {
			for _, body := range lintBodies(s) {
				walk(body)
			}
		}
	}
	walk(p)
	lintEach(p, true, func(s Stmt, at int, _ bool) {
		switch s := s.(type) {
		case *If:
			if s.CondX != nil && len(refsOf(func(o map[string]bool) { exprRefs(s.CondX, o) })) == 0 {
				l.add(at, "constant-check", "if [ ] reads no name")
			}
		case *While:
			if s.CondX != nil && len(refsOf(func(o map[string]bool) { exprRefs(s.CondX, o) })) == 0 && !lintBreaks(&s.Body) {
				l.add(at, "constant-check", "while [ ] reads no name and has no break")
			}
		case *Retry:
			if what := l.mutates(&s.Body, 0); what != "" {
				l.add(at, "retry-on-mutation", "retry repeats "+what)
			}
		case *Try:
			if s.Catch != nil && len(s.Catch.Stmts) == 0 {
				l.add(at, "silent-catch", "empty catch drops the failure")
			}
		}
		lintEachPipe(s, func(pl Pipeline, _ string) {
			for _, st := range pl.Stages {
				last := len(st.Argv) - 1
				if plain(st.Argv[0]) != "check" || last < 1 || exprOf(st.Argv[last]) == nil {
					continue
				}
				if len(refsOf(func(o map[string]bool) { exprRefs(exprOf(st.Argv[last]), o) })) == 0 {
					l.add(st.Argv[0].Pos, "constant-check", "check [ ] reads no name")
				}
			}
		})
	})
}

func lintBreaks(p *Program) bool {
	found := false
	lintEach(p, false, func(s Stmt, _ int, _ bool) {
		if j, ok := s.(*Jump); ok && j.Kind == "break" {
			found = true
		}
	})
	return found
}

// lintKey is the verb key a stage may call. A subcommand from a value may
// be the mutating one, so it is.
func lintKey(st Stage) string {
	argv := make([]string, len(st.Argv))
	for i, w := range st.Argv {
		argv[i] = plain(w)
		if argv[i] == "" && i > 1 {
			argv[i] = "$"
		}
	}
	if argv[0] == "" {
		return ""
	}
	if len(argv) > 1 && argv[1] == "" {
		switch argv[0] {
		case "unit":
			return "unit.restart"
		case "package":
			return "package.install"
		case "sysctl":
			return "sysctl.set"
		}
	}
	return knownKey(argv)
}

// mutates names the first mutation body may reach: a mutating verb, a
// template_file write, or a > redirect, through fns called by literal
// name up to depth 8. A try body is skipped: its catch keeps the failure
// from reaching the retry. The catch itself is not.
func (l *linter) mutates(p *Program, depth int) string {
	if p == nil {
		return ""
	}
	for _, s := range p.Stmts {
		what := ""
		lintEachPipe(s, func(pl Pipeline, _ string) {
			for _, st := range pl.Stages {
				if what != "" {
					return
				}
				for _, r := range st.Redirs {
					if r.Kind == ">" {
						what = "a > redirect"
						return
					}
				}
				if key := lintKey(st); mutating[key] {
					what = key
					return
				}
				if inner := l.fnMutates(plain(st.Argv[0]), depth+1); inner != "" {
					what = inner
				}
			}
		})
		if what != "" {
			return what
		}
		var bodies []*Program
		switch s := s.(type) {
		case *Try:
			bodies = []*Program{s.Catch}
		case *Fn:
		default:
			bodies = lintBodies(s)
		}
		for _, b := range bodies {
			if what := l.mutates(b, depth); what != "" {
				return what
			}
		}
	}
	return ""
}

// fnMutates is mutates of fn name's body at depth, memoized per name and
// depth: each body is scanned at most once per depth however many retries
// call it, and depth (at most 8) ends a cycle.
func (l *linter) fnMutates(name string, depth int) string {
	fn, ok := l.fns[name]
	if !ok || depth > lintFnDepth {
		return ""
	}
	key := fmt.Sprint(depth, " ", name)
	if what, ok := l.mutated[key]; ok {
		return what
	}
	l.mutated[key] = ""
	what := l.mutates(&fn.Body, depth)
	if what != "" {
		what += " through " + name
	}
	l.mutated[key] = what
	return what
}

// needs is incomplete-needs, from the Needs rows themselves.
func (l *linter) needs() {
	for _, r := range l.n.sorted() {
		if r.note == "" {
			continue
		}
		sev := lintWarning
		if r.note == noteCommandFromValue {
			sev = lintError
		}
		lines := make([]int, 0, len(r.lines))
		for ln := range r.lines {
			lines = append(lines, ln)
		}
		sort.Ints(lines)
		for _, ln := range lines {
			l.out = append(l.out, lintFinding{line: ln, rule: "incomplete-needs", sev: sev, why: r.key + " " + r.target + ": " + r.note})
		}
	}
}

// lintAllows reads #lint:allow <rule> <reason> comments: the lines each
// allows a rule on. A reason is required.
func (l *linter) lintAllows(comments []comment) map[int]map[string]bool {
	out := map[int]map[string]bool{}
	for _, c := range comments {
		f := strings.Fields(strings.TrimPrefix(c.text, "#"))
		if len(f) < 3 || f[0] != "lint:allow" || lintRules[f[1]] == "" {
			continue
		}
		ln := l.n.line(c.pos)
		if out[ln] == nil {
			out[ln] = map[string]bool{}
		}
		out[ln][f[1]] = true
	}
	return out
}

func (l *linter) report(comments []comment) Outcome {
	allows := l.lintAllows(comments)
	seen := map[lintFinding]bool{}
	var kept []lintFinding
	for _, f := range l.out {
		if seen[f] || (f.sev != lintError && allows[f.line][f.rule]) {
			continue
		}
		seen[f] = true
		kept = append(kept, f)
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.line != b.line {
			return a.line < b.line
		}
		if a.rule != b.rule {
			return a.rule < b.rule
		}
		return a.why < b.why
	})
	out := Outcome{}
	if len(kept) == 0 {
		return out
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "line\trule\tseverity\twhat")
	for _, f := range kept {
		cells := []string{fmt.Sprint(f.line), f.rule, f.sev, f.why}
		for i := range cells {
			cells[i] = needsCell(cells[i])
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
		if f.sev != lintInfo {
			out.Status = 1
		}
	}
	if err := tw.Flush(); err != nil {
		return Outcome{Status: exitType, Stderr: "lint: " + safeText(err.Error()) + "\n"}
	}
	out.Stdout = b.String()
	return out
}
