package lang

import "fmt"

// Program is a script: the prompt, sash -c, and sash file share this tree.
type Program struct {
	Stmts []Stmt
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
}

func (*Let) stmt() {}

// If runs Cond. A row pipeline is true when it yields a row. A byte pipeline is true when its exit is 0.
type If struct {
	CondX Expr
	Cond  Pipeline
	Then Program
	Else *Program
}

func (*If) stmt() {}

// For walks the rows of Src.
type For struct {
	Name string
	Src  Pipeline
	Body Program
}

func (*For) stmt() {}

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
	tok []token
	i   int
}

func parse(src string) (*Program, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{tok: toks}
	prog, err := p.program()
	if err != nil {
		return nil, err
	}
	p.skipSep()
	if p.peek().kind != kEOF {
		return nil, fmt.Errorf("parse: trailing input")
	}
	return prog, nil
}

func (p *parser) program() (*Program, error) {
	var stmts []Stmt
	p.skipSep()
	for {
		k := p.peek().kind
		if k == kEOF || k == kRBrace {
			break
		}
		s, err := p.stmt()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, s)
		if !p.atSep() && p.peek().kind != kEOF && p.peek().kind != kRBrace {
			return nil, fmt.Errorf("parse: expected separator")
		}
		p.skipSep()
	}
	return &Program{Stmts: stmts}, nil
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
		case "let":
			return p.let()
		case "if":
			return p.iff()
		case "for":
			return p.forr()
		case "fn":
			return p.fn()
		}
	}
	return p.chain()
}

func (p *parser) let() (Stmt, error) {
	p.i++
	name, err := p.plainName("let")
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kEq {
		return nil, fmt.Errorf("parse: let needs =")
	}
	p.i++
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
	var condX Expr
	var cond Pipeline
	if t := p.peek(); t.kind == kWord && exprOf(t.word) != nil && p.i+1 < len(p.tok) && p.tok[p.i+1].kind == kLBrace {
		condX = exprOf(t.word)
		p.i++
	} else {
		var err error
		if cond, err = p.pipeline(); err != nil {
			return nil, err
		}
	}
	then, err := p.block()
	if err != nil {
		return nil, err
	}
	p.skipSep()
	var elseP *Program
	if p.peek().kind == kWord && plain(p.peek().word) == "else" {
		p.i++
		body, err := p.block()
		if err != nil {
			return nil, err
		}
		elseP = body
	}
	return &If{CondX: condX, Cond: cond, Then: *then, Else: elseP}, nil
}

func (p *parser) forr() (Stmt, error) {
	p.i++
	name, err := p.plainName("for")
	if err != nil {
		return nil, err
	}
	if plain(p.peek().word) != "in" || p.peek().kind != kWord {
		return nil, fmt.Errorf("parse: for needs in")
	}
	p.i++
	src, err := p.pipeline()
	if err != nil {
		return nil, err
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	return &For{Name: name, Src: src, Body: *body}, nil
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
