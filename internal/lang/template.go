package lang

import (
	"errors"
	"fmt"
	"strings"
)

// Templates (language v4 contract). A template is parsed once from its
// file. Tags hold sash expressions, the language of [ ]. Vars are data: a
// var holding "{{ x }}" renders literally. A template runs no verb, reads no
// file, and includes nothing. Output, loops, and nesting are capped.

type tnode interface{ tnode() }

type tText struct{ s string }
type tExpr struct {
	x    Expr
	line int
}
type tIf struct {
	conds  []Expr
	bodies [][]tnode
	els    []tnode
	line   int
}
type tFor struct {
	n1, n2 string
	src    Expr
	body   []tnode
	line   int
}

func (tText) tnode() {}
func (tExpr) tnode() {}
func (tIf) tnode()   {}
func (tFor) tnode()  {}

type ttag struct {
	text      string // text before the tag
	tag       string // inside {{ }}, trimmed; "" at the end
	trimLeft  bool
	trimRight bool
	line      int
	end       bool
}

// splitTemplate cuts src into text and tags.
func splitTemplate(src string) ([]ttag, error) {
	var out []ttag
	line := 1
	for {
		i := strings.Index(src, "{{")
		if i < 0 {
			out = append(out, ttag{text: src, end: true, line: line})
			return out, nil
		}
		j := strings.Index(src[i:], "}}")
		if j < 0 {
			return nil, fmt.Errorf("template line %d: unclosed {{", line+strings.Count(src[:i], "\n"))
		}
		body := src[i+2 : i+j]
		t := ttag{text: src[:i], line: line + strings.Count(src[:i], "\n")}
		if strings.HasPrefix(body, "-") {
			t.trimLeft = true
			body = body[1:]
		}
		if strings.HasSuffix(body, "-") {
			t.trimRight = true
			body = body[:len(body)-1]
		}
		t.tag = strings.TrimSpace(body)
		out = append(out, t)
		line += strings.Count(src[:i+j+2], "\n")
		src = src[i+j+2:]
	}
}

type tparser struct {
	tags  []ttag
	i     int
	depth int
}

// parseTemplate parses a whole template.
func parseTemplate(src string) ([]tnode, error) {
	if len(src) > dataMaxBytes {
		return nil, fmt.Errorf("template too large")
	}
	tags, err := splitTemplate(src)
	if err != nil {
		return nil, err
	}
	// Apply trims to the neighboring text.
	for k := range tags {
		if tags[k].trimLeft {
			tags[k].text = strings.TrimRight(tags[k].text, " \t\r\n")
		}
		if tags[k].trimRight && k+1 < len(tags) {
			tags[k+1].text = strings.TrimLeft(tags[k+1].text, " \t\r\n")
		}
	}
	p := &tparser{tags: tags}
	nodes, stop, err := p.block()
	if err != nil {
		return nil, err
	}
	if stop != "" {
		return nil, fmt.Errorf("template line %d: %s without if or for", p.tags[p.i].line, stop)
	}
	return nodes, nil
}

func tagErr(line int, msg string) error {
	return fmt.Errorf("template line %d: %s", line, msg)
}

// block reads nodes up to else, else if, end, or the end of input. stop
// names what ended it; the parser sits on that tag.
func (p *tparser) block() ([]tnode, string, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > exprDepthMax {
		return nil, "", tagErr(p.tags[p.i].line, "too deep")
	}
	var out []tnode
	for p.i < len(p.tags) {
		t := p.tags[p.i]
		if t.text != "" {
			out = append(out, tText{t.text})
		}
		if t.end {
			p.i++
			return out, "", nil
		}
		tag := t.tag
		switch {
		case strings.HasPrefix(tag, "#"):
			p.i++
		case tag == "end", tag == "else", strings.HasPrefix(tag, "else if "):
			// The caller consumes it; clear its text, already taken.
			p.tags[p.i].text = ""
			return out, tag, nil
		case strings.HasPrefix(tag, "if "):
			n, err := p.ifNode(t)
			if err != nil {
				return nil, "", err
			}
			out = append(out, n)
		case strings.HasPrefix(tag, "for "):
			n, err := p.forNode(t)
			if err != nil {
				return nil, "", err
			}
			out = append(out, n)
		default:
			x, err := parseExpr(tag)
			if err != nil {
				return nil, "", tagErr(t.line, err.Error())
			}
			out = append(out, tExpr{x, t.line})
			p.i++
		}
	}
	return out, "", nil
}

func (p *tparser) ifNode(t ttag) (tnode, error) {
	n := tIf{line: t.line}
	cond := strings.TrimPrefix(t.tag, "if ")
	for {
		x, err := parseExpr(cond)
		if err != nil {
			return nil, tagErr(t.line, err.Error())
		}
		p.i++
		body, stop, err := p.block()
		if err != nil {
			return nil, err
		}
		n.conds = append(n.conds, x)
		n.bodies = append(n.bodies, body)
		switch {
		case stop == "end":
			p.i++
			return n, nil
		case stop == "else":
			p.i++
			els, stop2, err := p.block()
			if err != nil {
				return nil, err
			}
			if stop2 != "end" {
				return nil, tagErr(t.line, "if needs end")
			}
			p.i++
			n.els = els
			return n, nil
		case strings.HasPrefix(stop, "else if "):
			cond = strings.TrimPrefix(stop, "else ")
			cond = strings.TrimPrefix(cond, "if ")
			t = p.tags[p.i]
		default:
			return nil, tagErr(t.line, "if needs end")
		}
	}
}

func (p *tparser) forNode(t ttag) (tnode, error) {
	head := strings.TrimPrefix(t.tag, "for ")
	names, src, ok := strings.Cut(head, " in ")
	if !ok {
		return nil, tagErr(t.line, "for needs in")
	}
	n := tFor{line: t.line}
	parts := strings.Split(names, ",")
	if len(parts) > 2 {
		return nil, tagErr(t.line, "for takes one or two names")
	}
	for i, part := range parts {
		name := strings.TrimSpace(part)
		if !isNameWord(name) {
			return nil, tagErr(t.line, "for name")
		}
		if i == 0 {
			n.n1 = name
		} else {
			n.n2 = name
		}
	}
	x, err := parseExpr(src)
	if err != nil {
		return nil, tagErr(t.line, err.Error())
	}
	n.src = x
	p.i++
	body, stop, err := p.block()
	if err != nil {
		return nil, err
	}
	if stop != "end" {
		return nil, tagErr(t.line, "for needs end")
	}
	p.i++
	n.body = body
	return n, nil
}

func isNameWord(s string) bool {
	if s == "" || !isNameStart(rune(s[0])) {
		return false
	}
	for _, r := range s {
		if !isName(r) {
			return false
		}
	}
	return true
}

// trender renders nodes into out against scope, charging c.
type trender struct {
	out     strings.Builder
	c       *evalCtx
	scope   []map[string]any
	loops   int
	loopMax int
}

func (r *trender) lookup(name string) (any, bool) {
	for i := len(r.scope) - 1; i >= 0; i-- {
		if v, ok := r.scope[i][name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (r *trender) eval(x Expr, line int) (any, error) {
	d, err := evalExprCtx(x, r.c, r.lookup)
	if err != nil {
		if errors.Is(err, errSecretFault) {
			return nil, err
		}
		return nil, tagErr(line, err.Error())
	}
	return d, nil
}

func (r *trender) render(nodes []tnode) error {
	for _, n := range nodes {
		switch n := n.(type) {
		case tText:
			if err := charge(r.c, len(n.s)); err != nil {
				return fmt.Errorf("template output too large")
			}
			r.out.WriteString(n.s)
		case tExpr:
			d, err := r.eval(n.x, n.line)
			if err != nil {
				return err
			}
			if isSecret(d) {
				return tagErr(n.line, "a secret cannot leave sash")
			}
			s := textOf(d)
			if err := charge(r.c, len(s)); err != nil {
				return fmt.Errorf("template output too large")
			}
			r.out.WriteString(s)
		case tIf:
			done := false
			for i, cx := range n.conds {
				d, err := r.eval(cx, n.line)
				if err != nil {
					return err
				}
				take, derr := decide(d)
				if derr != nil {
					return tagErr(n.line, derr.Error())
				}
				if take {
					if err := r.render(n.bodies[i]); err != nil {
						return err
					}
					done = true
					break
				}
			}
			if !done && n.els != nil {
				if err := r.render(n.els); err != nil {
					return err
				}
			}
		case tFor:
			d, err := r.eval(n.src, n.line)
			if err != nil {
				return err
			}
			type pair struct{ a, b any }
			var items []pair
			switch x := d.(type) {
			case []any:
				for i, e := range x {
					items = append(items, pair{int64(i), e})
				}
			case *omap:
				for _, k := range x.keys {
					items = append(items, pair{k, x.m[k]})
				}
			default:
				return tagErr(n.line, "for needs a list or a map")
			}
			_, isMap := d.(*omap)
			for _, it := range items {
				r.loops++
				if r.loops > r.loopMax {
					return fmt.Errorf("loop max")
				}
				s := map[string]any{}
				if n.n2 == "" {
					if isMap {
						s[n.n1] = it.a
					} else {
						s[n.n1] = it.b
					}
				} else {
					s[n.n1], s[n.n2] = it.a, it.b
				}
				r.scope = append(r.scope, s)
				err := r.render(n.body)
				r.scope = r.scope[:len(r.scope)-1]
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// renderTemplate renders src with vars, a map.
func renderTemplate(src string, vars any, loopMax int) (string, error) {
	return renderTemplatePinned(src, vars, loopMax, nil)
}

// renderTemplatePinned is renderTemplate with uuid() answered by pin when
// set, so a recording pins a template's uuids as it does the script's.
func renderTemplatePinned(src string, vars any, loopMax int, pin func() (string, error)) (string, error) {
	nodes, err := parseTemplate(src)
	if err != nil {
		return "", err
	}
	o, ok := vars.(*omap)
	if !ok {
		return "", fmt.Errorf("template vars must be a map")
	}
	top := map[string]any{"vars": o}
	for _, k := range o.keys {
		top[k] = o.m[k]
	}
	r := &trender{c: &evalCtx{budget: dataMaxBytes, strict: true, uuid: pin}, scope: []map[string]any{top}, loopMax: loopMax}
	if err := r.render(nodes); err != nil {
		return "", err
	}
	return r.out.String(), nil
}
