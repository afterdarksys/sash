package lang

import (
	"fmt"
	"strings"
)

// Program is a script: the prompt, sash -c, and sash file share this tree.
type Program struct {
	Stmts []Stmt
	// Pos is the source offset where each statement starts, for lint.
	Pos []int
}

// Stmt is one statement.
type Stmt interface{ stmt() }

// Chain is pipelines joined by && and ||.
type Chain struct {
	Pipes []Pipeline
	Ops   []string
}

func (*Chain) stmt() {}

// Let binds a word or a parenthesized pipeline. The value is not parsed again.
type Let struct {
	Name string
	Word *Word
	Pipe *Pipeline
	// Capture: let x = capture { pipeline } binds a map of the pipeline's
	// output, error text and statuses instead of its output.
	Capture bool
	// Path: let x = path WORD (or declare -P) binds Word as a path value
	// (path.go), checked once, here.
	Path bool
}

func (*Let) stmt() {}

// If runs Cond. A row pipeline is true when it yields a row. A byte pipeline is true when its exit is 0.
type If struct {
	CondX Expr
	Cond  Pipeline
	Then  Program
	Else  *Program
}

func (*If) stmt() {}

// For walks the rows of Src.
// For walks Src: rows from a pipeline, or a value from a [ ] expression
// (SrcX) or a $ reference (SrcRef). Name2 takes the index or the map value.
type For struct {
	Name   string
	Name2  string
	Src    Pipeline
	SrcX   Expr
	SrcRef string
	Body   Program
}

func (*For) stmt() {}

// While runs Body while the condition holds. Do runs Body once first.
type While struct {
	CondX Expr
	Cond  Pipeline
	Body  Program
	Do    bool
}

func (*While) stmt() {}

// Jump is break, continue, return, or exit. Word is the optional value.
type Jump struct {
	Kind string
	Word *Word
}

func (*Jump) stmt() {}

// Try runs Body. A failure that stops it (a denial, an unknown name, a type
// error) runs Catch with Name bound to {status, message, key}. Catch is
// required: a bare try is a parse error, so no failure is dropped silently.
type Try struct {
	Body  Program
	Name  string
	Catch *Program
	// Finally runs after the body and any catch, on every way out (v7).
	Finally *Program
}

func (*Try) stmt() {}

// Defer runs Body when the enclosing function or script ends, last in first
// out, whichever way it ends (v7).
type Defer struct {
	Body Program
}

func (*Defer) stmt() {}

// Declare sets flags on a name and may bind it: declare -r name = value.
// With no name it lists; -p name shows one value.
type Declare struct {
	Flags string
	Name  string
	Value *Let
}

func (*Declare) stmt() {}

// With runs Body with options, the prefix, or names overridden, and
// restores them on every way out.
type With struct {
	Set    []string // set arguments: -e, +u, -o name
	Caps   map[string]string
	Prefix *Word
	Lets   []*Let
	Body   Program
}

func (*With) stmt() {}

// Retry runs Body again on failure, up to N times, waiting Delay between.
type Retry struct {
	N     Word
	Delay *Word
	Body  Program
}

func (*Retry) stmt() {}

// Timeout ends Body past D: checked before each statement and verb.
type Timeout struct {
	D    Word
	Body Program
	// Every makes this an await; Body contains its single condition.
	Every *Word
}

func (*Timeout) stmt() {}

// Par runs branches concurrently, at most N at once (v5 section 8,
// docs/plans/2026-11-14-sash-mux-pipe.md): each statement of Body is a
// branch, or, when Each is set, Body is one For and each item is a branch.
// As, when set, binds the branch rows.
type Par struct {
	N    Word
	As   string
	Body Program
	Each bool
}

func (*Par) stmt() {}

// Atomic runs Body as one transaction of file writes (v6 section 3): the
// agent journals each write and restores them all unless the block
// succeeds. Deadline, when set, is the transaction's deadline.
type Atomic struct {
	Deadline *Word
	Body     Program
}

func (*Atomic) stmt() {}

// parMax is the largest par N.
const parMax = 16

// Use loads fn and declare -r definitions from a file read through
// file.read. It runs no other statement.
type Use struct{ Path Word }

func (*Use) stmt() {}

// Fn defines a command. Each call checks entitlements again.
type Fn struct {
	Name   string
	Params []string
	Body   Program
}

func (*Fn) stmt() {}

// Pipeline is stages joined by "|" or "|>".
type Pipeline struct {
	Stages []Stage
	Ops    []string
}

// Stage is one command plus redirects.
type Stage struct {
	Argv   []Word
	Redirs []Redir
}

// Redir is "<", "<<<", or ">".
type Redir struct {
	Kind string
	Path Word
}

type parser struct {
	tok   []token
	i     int
	depth int
}

// blockDepthMax bounds { } nesting, as exprDepthMax bounds [ ]: the v1
// rule is that everything is bounded by depth 64. Every walker of the
// tree (run, needs, why, the preview, lint) recurses per block, and some
// rescan a body per level, so an unbounded depth made a 1 MiB script
// quadratic. Real scripts nest a handful deep; 64 is far past that and
// the same number a [ ] expression and decoded data already meet.
const blockDepthMax = 64

func parse(src string) (*Program, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	return parseTokens(toks)
}

// parseTokens parses what lex returned. It does not change toks.
func parseTokens(toks []token) (*Program, error) {
	p := &parser{tok: toks}
	prog, err := p.program()
	if err != nil {
		return nil, err
	}
	p.skipSep()
	if p.peek().kind != kEOF {
		return nil, fmt.Errorf("parse: trailing input")
	}
	if err := checkServiceFile(prog); err != nil {
		return nil, err
	}
	return prog, nil
}

func (p *parser) program() (*Program, error) {
	var stmts []Stmt
	var pos []int
	p.skipSep()
	for {
		k := p.peek().kind
		if k == kEOF || k == kRBrace {
			break
		}
		at := p.peek().pos
		s, err := p.stmt()
		if err != nil {
			return nil, err
		}
		// A service block is first; args follows it, or is first itself.
		if _, ok := s.(*Service); ok && (p.depth != 0 || len(stmts) != 0) {
			return nil, fmt.Errorf("parse: service must be the first top-level statement")
		}
		afterService := len(stmts) == 1 && serviceOf(&Program{Stmts: stmts}) != nil
		if _, ok := s.(*Arguments); ok && (p.depth != 0 || (len(stmts) != 0 && !afterService)) {
			return nil, fmt.Errorf("parse: args must be the first top-level statement")
		}
		stmts = append(stmts, s)
		pos = append(pos, at)
		if !p.atSep() && p.peek().kind != kEOF && p.peek().kind != kRBrace {
			return nil, fmt.Errorf("parse: expected separator")
		}
		p.skipSep()
	}
	return &Program{Stmts: stmts, Pos: pos}, nil
}

func (p *parser) atSep() bool {
	k := p.peek().kind
	return k == kNL || k == kSemi
}

func (p *parser) skipSep() {
	for p.atSep() {
		p.i++
	}
}

func (p *parser) stmt() (Stmt, error) {
	if p.peek().kind == kWord && len(p.peek().word.Parts) == 1 && !p.peek().word.Parts[0].Exp {
		switch p.peek().word.Parts[0].Lit {
		case "args":
			return p.arguments()
		case "service":
			// service { } is the block; service status NAME is a verb.
			if p.i+1 < len(p.tok) && p.tok[p.i+1].kind == kLBrace {
				return p.service()
			}
		case "await":
			return p.await()
		case "let":
			return p.let()
		case "if":
			return p.iff()
		case "for":
			return p.forr()
		case "fn":
			return p.fn()
		case "while":
			return p.while()
		case "do":
			return p.doWhile()
		case "break", "continue", "return", "exit":
			return p.jump()
		case "try":
			return p.try()
		case "defer":
			return p.deferStmt()
		case "declare":
			return p.declare()
		case "with":
			return p.with()
		case "retry":
			p.i++
			if p.peek().kind != kWord {
				return nil, fmt.Errorf("parse: retry needs a count")
			}
			r := &Retry{N: p.peek().word}
			p.i++
			if p.peek().kind == kWord && plain(p.peek().word) == "delay" {
				p.i++
				if p.peek().kind != kWord {
					return nil, fmt.Errorf("parse: retry delay needs a duration")
				}
				d := p.peek().word
				r.Delay = &d
				p.i++
			}
			body, err := p.block()
			if err != nil {
				return nil, err
			}
			r.Body = *body
			return r, nil
		case "par":
			return p.par()
		case "atomic":
			return p.atomic()
		case "timeout":
			p.i++
			if p.peek().kind != kWord {
				return nil, fmt.Errorf("parse: timeout needs a duration")
			}
			t := &Timeout{D: p.peek().word}
			p.i++
			body, err := p.block()
			if err != nil {
				return nil, err
			}
			t.Body = *body
			return t, nil
		case "wasm":
			return p.parseWasm()
		case "use":
			p.i++
			if p.peek().kind != kWord {
				return nil, fmt.Errorf("parse: use needs a path")
			}
			w := p.peek().word
			p.i++
			return &Use{Path: w}, nil
		}
	}
	return p.chain()
}

// par reads par N [as r] { branches } or par N [as r] for x in src { }.
// A par inside a par is refused, and so is a break, continue, return, or
// exit that would leave a branch.
func (p *parser) par() (Stmt, error) {
	p.i++
	if p.peek().kind != kWord {
		return nil, fmt.Errorf("parse: par needs a count")
	}
	s := &Par{N: p.peek().word}
	p.i++
	if p.peek().kind == kWord && plain(p.peek().word) == "as" {
		p.i++
		name, err := p.plainName("par as")
		if err != nil {
			return nil, err
		}
		s.As = name
	}
	if p.peek().kind == kWord && plain(p.peek().word) == "for" {
		at := p.peek().pos
		f, err := p.forr()
		if err != nil {
			return nil, err
		}
		s.Body = Program{Stmts: []Stmt{f}, Pos: []int{at}}
		s.Each = true
	} else {
		body, err := p.block()
		if err != nil {
			return nil, err
		}
		s.Body = *body
	}
	branches := s.Body.Stmts
	if s.Each {
		branches = s.Body.Stmts[0].(*For).Body.Stmts
	}
	for _, b := range branches {
		if msg := parBranchCheck(b, false); msg != "" {
			return nil, fmt.Errorf("parse: %s", msg)
		}
	}
	return s, nil
}

// atomic reads atomic [deadline D] { body }. A break, continue, return,
// or exit that would leave the block is refused: the block ends only by
// running through (commit) or failing (abort).
func (p *parser) atomic() (Stmt, error) {
	p.i++
	a := &Atomic{}
	if p.peek().kind == kWord && plain(p.peek().word) == "deadline" {
		p.i++
		if p.peek().kind != kWord {
			return nil, fmt.Errorf("parse: atomic deadline needs a duration")
		}
		d := p.peek().word
		a.Deadline = &d
		p.i++
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	a.Body = *body
	for _, s := range a.Body.Stmts {
		if msg := jumpLeaves(s, false, "an atomic block"); msg != "" {
			return nil, fmt.Errorf("parse: %s", msg)
		}
	}
	return a, nil
}

// jumpLeaves refuses a jump that would leave a block: return and exit
// outside a fn body, break and continue outside a loop of the block.
func jumpLeaves(s Stmt, inLoop bool, what string) string {
	switch s := s.(type) {
	case *Jump:
		if s.Kind == "return" || s.Kind == "exit" || !inLoop {
			return s.Kind + " would leave " + what
		}
	case *For:
		return jumpsIn(&s.Body, true, what)
	case *While:
		return jumpsIn(&s.Body, true, what)
	case *If:
		if msg := jumpsIn(&s.Then, inLoop, what); msg != "" {
			return msg
		}
		return jumpsIn(s.Else, inLoop, what)
	case *Try:
		if msg := jumpsIn(&s.Body, inLoop, what); msg != "" {
			return msg
		}
		if msg := jumpsIn(s.Catch, inLoop, what); msg != "" {
			return msg
		}
		if s.Finally != nil {
			return jumpsIn(s.Finally, inLoop, what)
		}
		return ""
	case *Defer:
		return jumpsIn(&s.Body, false, what)
	case *With:
		return jumpsIn(&s.Body, inLoop, what)
	case *Retry:
		return jumpsIn(&s.Body, inLoop, what)
	case *Timeout:
		return jumpsIn(&s.Body, inLoop, what)
	case *Atomic:
		return jumpsIn(&s.Body, false, what)
	case *Par:
		return jumpsIn(&s.Body, false, what)
	}
	return ""
}

func jumpsIn(p *Program, inLoop bool, what string) string {
	if p == nil {
		return ""
	}
	for _, s := range p.Stmts {
		if msg := jumpLeaves(s, inLoop, what); msg != "" {
			return msg
		}
	}
	return ""
}

// parBranchCheck refuses, in one branch, a nested par and a jump that
// would leave the branch: return and exit anywhere outside a fn body, and
// break or continue outside a loop of the branch. A fn defined in a branch
// may return; a call that reaches exit or a par at run time is refused
// there.
func parBranchCheck(s Stmt, inLoop bool) string {
	switch s := s.(type) {
	case *Par:
		return "par inside par"
	case *Atomic:
		// A transaction serializes the session's verbs at the agent; a
		// branch cannot hold one (and atomic refuses par inside).
		return "atomic inside par"
	case *Jump:
		switch {
		case s.Kind == "return" || s.Kind == "exit":
			return s.Kind + " would leave a par branch"
		case !inLoop:
			return s.Kind + " would leave a par branch"
		}
		return ""
	case *Fn:
		var msg string
		needsEachStmt(&s.Body, func(in Stmt) {
			if _, ok := in.(*Par); ok && msg == "" {
				msg = "par inside par"
			}
		})
		return msg
	case *For:
		return parBodyCheck(&s.Body, true)
	case *While:
		return parBodyCheck(&s.Body, true)
	case *If:
		if msg := parBodyCheck(&s.Then, inLoop); msg != "" {
			return msg
		}
		return parBodyCheck(s.Else, inLoop)
	case *Try:
		if msg := parBodyCheck(&s.Body, inLoop); msg != "" {
			return msg
		}
		if msg := parBodyCheck(s.Catch, inLoop); msg != "" {
			return msg
		}
		if s.Finally != nil {
			return parBodyCheck(s.Finally, inLoop)
		}
		return ""
	case *Defer:
		return parBodyCheck(&s.Body, inLoop)
	case *With:
		return parBodyCheck(&s.Body, inLoop)
	case *Retry:
		return parBodyCheck(&s.Body, inLoop)
	case *Timeout:
		return parBodyCheck(&s.Body, inLoop)
	}
	return ""
}

func parBodyCheck(p *Program, inLoop bool) string {
	if p == nil {
		return ""
	}
	for _, s := range p.Stmts {
		if msg := parBranchCheck(s, inLoop); msg != "" {
			return msg
		}
	}
	return ""
}

func (p *parser) let() (Stmt, error) {
	p.i++
	name, err := p.plainName("let")
	if err != nil {
		return nil, err
	}
	return p.letValue(name)
}

// letValue reads "= word" or "= (pipeline)" after a name.
func (p *parser) letValue(name string) (*Let, error) {
	if p.peek().kind != kEq {
		return nil, fmt.Errorf("parse: let needs =")
	}
	p.i++
	if p.peek().kind == kWord && plain(p.peek().word) == "capture" && p.i+1 < len(p.tok) && p.tok[p.i+1].kind == kLBrace {
		p.i += 2
		for p.peek().kind == kNL {
			p.i++
		}
		pipe, err := p.pipeline()
		if err != nil {
			return nil, err
		}
		for p.peek().kind == kNL {
			p.i++
		}
		if p.peek().kind != kRBrace {
			return nil, fmt.Errorf("parse: capture holds one pipeline")
		}
		p.i++
		return &Let{Name: name, Pipe: &pipe, Capture: true}, nil
	}
	if p.peek().kind == kWord && plain(p.peek().word) == "path" && p.i+1 < len(p.tok) && p.tok[p.i+1].kind == kWord {
		p.i++
		w := p.peek().word
		p.i++
		if err := pathLiteralOK(w); err != nil {
			return nil, err
		}
		return &Let{Name: name, Word: &w, Path: true}, nil
	}
	if p.peek().kind == kLParen {
		p.i++
		pipe, err := p.pipeline()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != kRParen {
			return nil, fmt.Errorf("parse: let needs )")
		}
		p.i++
		return &Let{Name: name, Pipe: &pipe}, nil
	}
	if p.peek().kind != kWord {
		return nil, fmt.Errorf("parse: let needs a word")
	}
	w := p.peek().word
	p.i++
	return &Let{Name: name, Word: &w}, nil
}

func (p *parser) iff() (Stmt, error) {
	p.i++
	condX, cond, err := p.cond()
	if err != nil {
		return nil, err
	}
	then, err := p.block()
	if err != nil {
		return nil, err
	}
	// else may sit on the next line. Without one, the separator belongs to
	// the program, so give it back.
	save := p.i
	p.skipSep()
	var elseP *Program
	if p.peek().kind != kWord || plain(p.peek().word) != "else" {
		p.i = save
	}
	if p.peek().kind == kWord && plain(p.peek().word) == "else" {
		p.i++
		if p.peek().kind == kWord && plain(p.peek().word) == "if" {
			at := p.peek().pos
			inner, err := p.iff()
			if err != nil {
				return nil, err
			}
			elseP = &Program{Stmts: []Stmt{inner}, Pos: []int{at}}
		} else {
			body, err := p.block()
			if err != nil {
				return nil, err
			}
			elseP = body
		}
	}
	return &If{CondX: condX, Cond: cond, Then: *then, Else: elseP}, nil
}

// cond reads an if or while condition: one [ ] expression followed by {,
// or a pipeline.
func (p *parser) cond() (Expr, Pipeline, error) {
	if t := p.peek(); t.kind == kWord && exprOf(t.word) != nil && p.i+1 < len(p.tok) && p.tok[p.i+1].kind == kLBrace {
		p.i++
		return exprOf(t.word), Pipeline{}, nil
	}
	pipe, err := p.pipeline()
	return nil, pipe, err
}

func (p *parser) forr() (Stmt, error) {
	p.i++
	name, err := p.plainName("for")
	if err != nil {
		return nil, err
	}
	f := &For{Name: name}
	if p.peek().kind == kWord && plain(p.peek().word) != "in" && plain(p.peek().word) != "" {
		if f.Name2, err = p.plainName("for"); err != nil {
			return nil, err
		}
	}
	if plain(p.peek().word) != "in" || p.peek().kind != kWord {
		return nil, fmt.Errorf("parse: for needs in")
	}
	p.i++
	t := p.peek()
	next := kEOF
	if p.i+1 < len(p.tok) {
		next = p.tok[p.i+1].kind
	}
	switch {
	case t.kind == kWord && exprOf(t.word) != nil && next == kLBrace:
		f.SrcX = exprOf(t.word)
		p.i++
	case t.kind == kWord && next == kLBrace && len(t.word.Parts) == 1 && t.word.Parts[0].Exp:
		f.SrcRef = t.word.Parts[0].Lit
		p.i++
	default:
		if f.Src, err = p.pipeline(); err != nil {
			return nil, err
		}
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	f.Body = *body
	return f, nil
}

func (p *parser) while() (Stmt, error) {
	p.i++
	condX, cond, err := p.cond()
	if err != nil {
		return nil, err
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	return &While{CondX: condX, Cond: cond, Body: *body}, nil
}

// doWhile reads do { } while c. The while sits on the line of the }.
func (p *parser) doWhile() (Stmt, error) {
	p.i++
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kWord || plain(p.peek().word) != "while" {
		return nil, fmt.Errorf("parse: do needs while")
	}
	p.i++
	condX, cond, err := p.condEnd()
	if err != nil {
		return nil, err
	}
	return &While{CondX: condX, Cond: cond, Body: *body, Do: true}, nil
}

// condEnd reads a condition that ends the statement: an expression or a
// pipeline, then a separator or the end.
func (p *parser) condEnd() (Expr, Pipeline, error) {
	if t := p.peek(); t.kind == kWord && exprOf(t.word) != nil {
		p.i++
		return exprOf(t.word), Pipeline{}, nil
	}
	pipe, err := p.pipeline()
	return nil, pipe, err
}

func (p *parser) jump() (Stmt, error) {
	kind := plain(p.peek().word)
	p.i++
	j := &Jump{Kind: kind}
	if (kind == "return" || kind == "exit") && p.peek().kind == kWord {
		w := p.peek().word
		j.Word = &w
		p.i++
	}
	return j, nil
}

func (p *parser) declare() (Stmt, error) {
	p.i++
	d := &Declare{}
	for p.peek().kind == kWord && strings.HasPrefix(plain(p.peek().word), "-") {
		f := plain(p.peek().word)[1:]
		if f == "" || strings.Trim(f, "riaApsP") != "" {
			return nil, fmt.Errorf("usage declare: -r -i -a -A -P -s -p; there is no export")
		}
		d.Flags += f
		p.i++
	}
	if p.peek().kind != kWord {
		if strings.Contains(d.Flags, "p") {
			return nil, fmt.Errorf("usage declare -p name")
		}
		return d, nil
	}
	name, err := p.plainName("declare")
	if err != nil {
		return nil, err
	}
	d.Name = name
	if p.peek().kind == kEq {
		if d.Value, err = p.letValue(name); err != nil {
			return nil, err
		}
		if strings.Contains(d.Flags, "P") {
			if d.Value.Word == nil || d.Value.Capture {
				return nil, fmt.Errorf("usage declare -P: name = word")
			}
			if !d.Value.Path {
				if err := pathLiteralOK(*d.Value.Word); err != nil {
					return nil, err
				}
			}
			d.Value.Path = true
		}
	}
	return d, nil
}

// with reads items up to the block. The lexer drops commas, so each item
// starts with a keyword (set, loopmax, verbmax, prefix, env) or is
// name = value.
func (p *parser) with() (Stmt, error) {
	p.i++
	w := &With{Caps: map[string]string{}}
	for p.peek().kind != kLBrace {
		t := p.peek()
		if t.kind != kWord || plain(t.word) == "" {
			return nil, fmt.Errorf("parse: with needs items and {")
		}
		switch word := plain(t.word); word {
		case "set":
			p.i++
			n := 0
			for p.peek().kind == kWord && (strings.HasPrefix(plain(p.peek().word), "-") || strings.HasPrefix(plain(p.peek().word), "+")) {
				f := plain(p.peek().word)
				w.Set = append(w.Set, f)
				p.i++
				n++
				if f == "-o" || f == "+o" {
					name, err := p.plainName("with set -o")
					if err != nil {
						return nil, err
					}
					w.Set = append(w.Set, name)
				}
			}
			if n == 0 {
				return nil, fmt.Errorf("parse: with set needs a flag")
			}
		case "loopmax", "verbmax":
			p.i++
			n, err := p.plainName("with " + word)
			if err != nil {
				return nil, err
			}
			w.Caps[word] = n
		case "prefix":
			p.i++
			if p.peek().kind != kWord {
				return nil, fmt.Errorf("parse: with prefix needs a path")
			}
			pw := p.peek().word
			w.Prefix = &pw
			p.i++
		case "env":
			return nil, fmt.Errorf("parse: env not in this series")
		default:
			p.i++
			l, err := p.letValue(word)
			if err != nil {
				return nil, fmt.Errorf("parse: with item %s", word)
			}
			l.Name = word
			w.Lets = append(w.Lets, l)
		}
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	w.Body = *body
	return w, nil
}

func (p *parser) try() (Stmt, error) {
	p.i++
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	t := &Try{Body: *body}
	p.skipSep()
	if p.peek().kind != kWord || plain(p.peek().word) != "catch" {
		return nil, fmt.Errorf("parse: try needs catch")
	}
	p.i++
	if p.peek().kind == kWord {
		if t.Name, err = p.plainName("catch"); err != nil {
			return nil, err
		}
	}
	if t.Catch, err = p.block(); err != nil {
		return nil, err
	}
	// finally may follow on the next line.
	save := p.i
	p.skipSep()
	if p.peek().kind == kWord && plain(p.peek().word) == "finally" {
		p.i++
		if t.Finally, err = p.block(); err != nil {
			return nil, err
		}
	} else {
		p.i = save
	}
	return t, nil
}

func (p *parser) deferStmt() (Stmt, error) {
	p.i++
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	// A jump out of a defer has nowhere to go: the function has ended.
	if msg := jumpsIn(body, false, "defer"); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}
	return &Defer{Body: *body}, nil
}

func (p *parser) fn() (Stmt, error) {
	p.i++
	name, err := p.plainName("fn")
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kLParen {
		return nil, fmt.Errorf("parse: fn needs (")
	}
	p.i++
	var params []string
	for p.peek().kind != kRParen {
		if p.peek().kind == kWord && plain(p.peek().word) == "," {
			p.i++
			continue
		}
		n, err := p.plainName("fn param")
		if err != nil {
			return nil, err
		}
		params = append(params, n)
		if p.peek().kind == kWord && plain(p.peek().word) == "," {
			p.i++
		}
	}
	p.i++
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	return &Fn{Name: name, Params: params, Body: *body}, nil
}

func (p *parser) block() (*Program, error) {
	if p.peek().kind != kLBrace {
		return nil, fmt.Errorf("parse: needs {")
	}
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > blockDepthMax {
		return nil, fmt.Errorf("parse: too deep")
	}
	p.i++
	body, err := p.program()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kRBrace {
		return nil, fmt.Errorf("parse: needs }")
	}
	p.i++
	return body, nil
}

func (p *parser) chain() (Stmt, error) {
	first, err := p.pipeline()
	if err != nil {
		return nil, err
	}
	c := &Chain{Pipes: []Pipeline{first}}
	for p.peek().kind == kAnd || p.peek().kind == kOr {
		op := "&&"
		if p.peek().kind == kOr {
			op = "||"
		}
		p.i++
		next, err := p.pipeline()
		if err != nil {
			return nil, err
		}
		c.Ops = append(c.Ops, op)
		c.Pipes = append(c.Pipes, next)
	}
	return c, nil
}

func (p *parser) pipeline() (Pipeline, error) {
	st, err := p.stage()
	if err != nil {
		return Pipeline{}, err
	}
	pl := Pipeline{Stages: []Stage{st}}
	for p.peek().kind == kPipe || p.peek().kind == kRow {
		op := "|"
		if p.peek().kind == kRow {
			op = "|>"
		}
		p.i++
		next, err := p.stage()
		if err != nil {
			return Pipeline{}, err
		}
		pl.Ops = append(pl.Ops, op)
		pl.Stages = append(pl.Stages, next)
	}
	return pl, nil
}

func (p *parser) stage() (Stage, error) {
	var st Stage
	for {
		switch p.peek().kind {
		case kWord:
			st.Argv = append(st.Argv, p.peek().word)
			p.i++
		case kGT, kLT, kHere:
			// where col > 5: right after where and a column, a bare > or
			// < is the comparison. Anywhere else it is a redirect.
			if k := p.peek().kind; k != kHere && len(st.Argv) == 2 && plain(st.Argv[0]) == "where" {
				op := ">"
				if k == kLT {
					op = "<"
				}
				t := p.peek()
				st.Argv = append(st.Argv, Word{Parts: []Part{{Lit: op}}, Pos: t.pos, End: t.end})
				p.i++
				continue
			}
			kind := ">"
			if p.peek().kind == kLT {
				kind = "<"
			} else if p.peek().kind == kHere {
				kind = "<<<"
			}
			p.i++
			if p.peek().kind != kWord {
				return Stage{}, fmt.Errorf("parse: redirect needs a path")
			}
			st.Redirs = append(st.Redirs, Redir{Kind: kind, Path: p.peek().word})
			p.i++
		default:
			if len(st.Argv) == 0 {
				return Stage{}, fmt.Errorf("parse: empty command")
			}
			return st, nil
		}
	}
}

func (p *parser) plainName(what string) (string, error) {
	if p.peek().kind != kWord || plain(p.peek().word) == "" {
		return "", fmt.Errorf("parse: %s needs a name", what)
	}
	n := plain(p.peek().word)
	p.i++
	return n, nil
}

func plain(w Word) string {
	if len(w.Parts) != 1 || w.Parts[0].Exp {
		return ""
	}
	return w.Parts[0].Lit
}

func (p *parser) peek() token {
	if p.i >= len(p.tok) {
		return token{kind: kEOF}
	}
	return p.tok[p.i]
}
