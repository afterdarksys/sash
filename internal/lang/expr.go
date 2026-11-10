package lang

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A [ ] expression is parsed once, at parse time, from source text. Its
// strings are data and are never parsed again. It calls no verb: a verb runs
// only as a pipeline stage, where the grant check and the audit are.

// Expr is one parsed expression node.
type Expr interface{ expr() }

type exprLit struct{ d any }
type exprRef struct{ name string }
type exprList struct{ items []Expr }
type exprMap struct {
	keys []string
	vals []Expr
}
type exprUnary struct {
	op string
	x  Expr
}
type exprBinary struct {
	op   string
	l, r Expr
}
type exprIndex struct {
	x   Expr
	key Expr
}
type exprRange struct{ lo, hi Expr }

func (exprLit) expr()    {}
func (exprRef) expr()    {}
func (exprList) expr()   {}
func (exprMap) expr()    {}
func (exprUnary) expr()  {}
func (exprBinary) expr() {}
func (exprIndex) expr()  {}
func (exprRange) expr()  {}

// rangeMax bounds a materialized range: past it the list is over the
// 8 MiB value cap anyway.
const rangeMax = dataMaxBytes / 25

type etok struct {
	k   string // num, str, ref, id, op, eof
	s   string
	d   any
	pos int
}

// exprSpans finds every [ ] expression of src in one pass, so a run of
// unclosed brackets costs linear time. An expression is one line: a newline
// closes nothing, so history, which works a line at a time, never sees half
// of one. Quotes inside are skipped with the same rules the parser and the
// expanders use, and brackets nest.
type exprSpans struct{ end map[int]int }

func newExprSpans(src string) exprSpans {
	ix := exprSpans{end: map[int]int{}}
	var stack []int
	for j := 0; j < len(src); j++ {
		switch src[j] {
		case '\n':
			stack = stack[:0]
		case '[':
			stack = append(stack, j)
		case ']':
			if len(stack) > 0 {
				ix.end[stack[len(stack)-1]] = j
				stack = stack[:len(stack)-1]
			}
		case '"', '\'':
			n := scanExprQuote(src[j:])
			nl := strings.IndexByte(src[j:], '\n')
			if n == 0 || (nl >= 0 && nl < n) {
				// Unclosed on this line: nothing open before it closes.
				stack = stack[:0]
				if nl < 0 {
					return ix
				}
				j += nl
				continue
			}
			j += n - 1
		}
	}
	return ix
}

// span is the length of the [ ] expression at src[i], or 0. A "[" opens one
// only at the start of the line or after whitespace, and only when a space
// or tab follows, so `egrep [0-9]+` stays a word.
func (ix exprSpans) span(src string, i int) int {
	if i >= len(src) || src[i] != '[' {
		return 0
	}
	if i > 0 && !isSpaceByte(src[i-1]) {
		return 0
	}
	if i+1 >= len(src) || (src[i+1] != ' ' && src[i+1] != '\t') {
		return 0
	}
	end, ok := ix.end[i]
	if !ok {
		return 0
	}
	return end - i + 1
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// scanExprQuote is the length of a quoted string at s[0], or 0 when unclosed.
func scanExprQuote(s string) int {
	q := s[0]
	for j := 1; j < len(s); j++ {
		if q == '"' && s[j] == '\\' {
			j++
			continue
		}
		if s[j] == q {
			return j + 1
		}
	}
	return 0
}

func exprLex(src string) ([]etok, error) {
	var out []etok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case isSpaceByte(c):
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			isFloat := false
			if j+1 < len(src) && src[j] == '.' && src[j+1] >= '0' && src[j+1] <= '9' {
				isFloat = true
				j++
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			text := src[i:j]
			var d any
			if isFloat {
				f, err := strconv.ParseFloat(text, 64)
				if err != nil {
					return nil, fmt.Errorf("type expr number")
				}
				d = f
			} else {
				n, err := strconv.ParseInt(text, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("type expr number")
				}
				d = n
			}
			out = append(out, etok{k: "num", d: d, pos: i})
			i = j
		case c == '"' || c == '\'':
			s, n, err := exprString(src[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, etok{k: "str", d: s, pos: i})
			i += n
		case c == '$':
			j := i + 1
			if j >= len(src) || !isNameStart(rune(src[j])) {
				return nil, fmt.Errorf("type expr bad $")
			}
			for j < len(src) && isName(rune(src[j])) {
				j++
			}
			out = append(out, etok{k: "ref", s: src[i+1 : j], pos: i})
			i = j
		case isNameStart(rune(c)):
			j := i
			for j < len(src) && isName(rune(src[j])) {
				j++
			}
			out = append(out, etok{k: "id", s: src[i:j], pos: i})
			i = j
		default:
			op := ""
			for _, o := range []string{"..", "==", "!=", "<=", ">=", "&&", "||"} {
				if strings.HasPrefix(src[i:], o) {
					op = o
					break
				}
			}
			if op == "" && strings.ContainsRune("+-*/%<>!()[]{},:.", rune(c)) {
				op = string(c)
			}
			if op == "" {
				return nil, fmt.Errorf("type expr unexpected %q", string(c))
			}
			out = append(out, etok{k: "op", s: op, pos: i})
			i += len(op)
		}
	}
	return append(out, etok{k: "eof", pos: len(src)}), nil
}

func exprString(s string) (string, int, error) {
	q := s[0]
	var b strings.Builder
	for j := 1; j < len(s); j++ {
		c := s[j]
		if c == q {
			return b.String(), j + 1, nil
		}
		if q == '"' && c == '\\' && j+1 < len(s) {
			j++
			switch s[j] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\', '"':
				b.WriteByte(s[j])
			default:
				return "", 0, fmt.Errorf("type expr escape")
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", 0, fmt.Errorf("type expr unclosed quote")
}

type exprParser struct {
	t     []etok
	i     int
	depth int
}

// parseExpr parses the text between [ and ].
func parseExpr(src string) (Expr, error) {
	toks, err := exprLex(src)
	if err != nil {
		return nil, err
	}
	p := &exprParser{t: toks}
	if p.peek().k == "eof" {
		return nil, fmt.Errorf("type expr empty")
	}
	x, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.peek().k != "eof" {
		return nil, fmt.Errorf("type expr trailing input")
	}
	return x, nil
}

func (p *exprParser) peek() etok { return p.t[p.i] }

func (p *exprParser) isOp(s string) bool {
	t := p.peek()
	return t.k == "op" && t.s == s
}

func (p *exprParser) isID(s string) bool {
	t := p.peek()
	return t.k == "id" && t.s == s
}

func (p *exprParser) expect(s string) error {
	if !p.isOp(s) {
		return fmt.Errorf("type expr expected %s", s)
	}
	p.i++
	return nil
}

func (p *exprParser) enter() error {
	p.depth++
	if p.depth > exprDepthMax {
		return fmt.Errorf("type expr too deep")
	}
	return nil
}

func (p *exprParser) or() (Expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	l, err := p.and()
	for err == nil && p.isOp("||") {
		p.i++
		var r Expr
		if r, err = p.and(); err == nil {
			l = exprBinary{"||", l, r}
		}
	}
	return l, err
}

func (p *exprParser) and() (Expr, error) {
	l, err := p.not()
	for err == nil && p.isOp("&&") {
		p.i++
		var r Expr
		if r, err = p.not(); err == nil {
			l = exprBinary{"&&", l, r}
		}
	}
	return l, err
}

func (p *exprParser) not() (Expr, error) {
	if p.isOp("!") {
		p.i++
		if err := p.enter(); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return exprUnary{"!", x}, nil
	}
	return p.compare()
}

func (p *exprParser) compare() (Expr, error) {
	l, err := p.rangeExpr()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		op := ""
		if t.k == "op" {
			switch t.s {
			case "==", "!=", "<", "<=", ">", ">=":
				op = t.s
			}
		} else if t.k == "id" && t.s == "in" {
			op = "in"
		}
		if op == "" {
			return l, nil
		}
		p.i++
		r, err := p.rangeExpr()
		if err != nil {
			return nil, err
		}
		l = exprBinary{op, l, r}
	}
}

func (p *exprParser) rangeExpr() (Expr, error) {
	l, err := p.sum()
	if err != nil || !p.isOp("..") {
		return l, err
	}
	p.i++
	r, err := p.sum()
	if err != nil {
		return nil, err
	}
	return exprRange{l, r}, nil
}

func (p *exprParser) sum() (Expr, error) {
	l, err := p.product()
	for err == nil && (p.isOp("+") || p.isOp("-")) {
		op := p.peek().s
		p.i++
		var r Expr
		if r, err = p.product(); err == nil {
			l = exprBinary{op, l, r}
		}
	}
	return l, err
}

func (p *exprParser) product() (Expr, error) {
	l, err := p.unary()
	for err == nil && (p.isOp("*") || p.isOp("/") || p.isOp("%")) {
		op := p.peek().s
		p.i++
		var r Expr
		if r, err = p.unary(); err == nil {
			l = exprBinary{op, l, r}
		}
	}
	return l, err
}

func (p *exprParser) unary() (Expr, error) {
	if p.isOp("-") {
		p.i++
		if err := p.enter(); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return exprUnary{"-", x}, nil
	}
	return p.postfix()
}

func (p *exprParser) postfix() (Expr, error) {
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOp("."):
			p.i++
			t := p.peek()
			if t.k != "id" {
				return nil, fmt.Errorf("type expr bad field")
			}
			p.i++
			x = exprIndex{x, exprLit{t.s}}
		case p.isOp("["):
			p.i++
			key, err := p.or()
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			x = exprIndex{x, key}
		default:
			return x, nil
		}
	}
}

func (p *exprParser) primary() (Expr, error) {
	t := p.peek()
	switch t.k {
	case "num", "str":
		p.i++
		return exprLit{t.d}, nil
	case "ref":
		p.i++
		return exprRef{t.s}, nil
	case "id":
		p.i++
		if p.isOp("(") {
			return p.call(t.s)
		}
		switch t.s {
		case "true":
			return exprLit{true}, nil
		case "false":
			return exprLit{false}, nil
		case "null":
			return exprLit{nil}, nil
		}
		return nil, fmt.Errorf("type expr unknown name %s", t.s)
	case "op":
		switch t.s {
		case "(":
			p.i++
			x, err := p.or()
			if err != nil {
				return nil, err
			}
			return x, p.expect(")")
		case "[":
			return p.list()
		case "{":
			return p.mapLit()
		}
	}
	if t.k == "eof" {
		return nil, fmt.Errorf("type expr incomplete")
	}
	return nil, fmt.Errorf("type expr unexpected %s", t.s)
}

// call reads name(args). The name must be an expression function: a user
// fn is a command, and a command may call a verb where an expression may not.
func (p *exprParser) call(name string) (Expr, error) {
	f, ok := exprFuncs[name]
	if !ok {
		return nil, fmt.Errorf("type expr unknown function %s", name)
	}
	p.i++
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	var args []Expr
	for !p.isOp(")") {
		a, err := p.or()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.isOp(",") {
			p.i++
			continue
		}
		if !p.isOp(")") {
			return nil, fmt.Errorf("type expr expected )")
		}
	}
	p.i++
	if len(args) < f.min || (f.max >= 0 && len(args) > f.max) {
		return nil, fmt.Errorf("type expr usage %s", name)
	}
	return exprCall{name: name, args: args}, nil
}

func (p *exprParser) list() (Expr, error) {
	p.i++
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	var items []Expr
	for !p.isOp("]") {
		x, err := p.or()
		if err != nil {
			return nil, err
		}
		items = append(items, x)
		if p.isOp(",") {
			p.i++
			continue
		}
		if !p.isOp("]") {
			return nil, fmt.Errorf("type expr expected ]")
		}
	}
	p.i++
	return exprList{items}, nil
}

func (p *exprParser) mapLit() (Expr, error) {
	p.i++
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	var m exprMap
	seen := map[string]bool{}
	for !p.isOp("}") {
		t := p.peek()
		if t.k != "str" && t.k != "id" {
			return nil, fmt.Errorf("type expr map key")
		}
		key := t.s
		if t.k == "str" {
			key = t.d.(string)
		}
		if seen[key] {
			return nil, fmt.Errorf("type expr duplicate key")
		}
		seen[key] = true
		p.i++
		if err := p.expect(":"); err != nil {
			return nil, err
		}
		v, err := p.or()
		if err != nil {
			return nil, err
		}
		m.keys = append(m.keys, key)
		m.vals = append(m.vals, v)
		if p.isOp(",") {
			p.i++
			continue
		}
		if !p.isOp("}") {
			return nil, fmt.Errorf("type expr expected }")
		}
	}
	p.i++
	return m, nil
}

// evalExpr computes an expression. lookup resolves $name.
func evalExpr(x Expr, lookup func(string) (any, bool)) (any, error) {
	return evalExprStrict(x, false, lookup)
}

// evalExprStrict is evalExpr; strict (set -u) makes a missing map key an
// error instead of null.
func evalExprStrict(x Expr, strict bool, lookup func(string) (any, bool)) (any, error) {
	c := &evalCtx{budget: dataMaxBytes, strict: strict}
	d, err := evalNode(x, lookup, c)
	if err != nil {
		return nil, err
	}
	if dataSize(d, 0) > dataMaxBytes {
		return nil, fmt.Errorf("type expr too large")
	}
	return d, nil
}

// charge spends the evaluation budget before an allocation, so a value is
// refused before it is built, not after.
func charge(c *evalCtx, n int) error {
	if n < 0 || n > c.budget {
		c.budget = -1
		return fmt.Errorf("type expr too large")
	}
	c.budget -= n
	return nil
}

// evalCtx is one evaluation: its allocation budget, and set -u.
type evalCtx struct {
	budget int
	strict bool
}

func evalNode(x Expr, lookup func(string) (any, bool), c *evalCtx) (any, error) {
	switch x := x.(type) {
	case exprLit:
		return x.d, nil
	case exprRef:
		d, ok := lookup(x.name)
		if !ok {
			return nil, fmt.Errorf("unset %s", x.name)
		}
		return d, nil
	case exprList:
		if err := charge(c, 16*len(x.items)); err != nil {
			return nil, err
		}
		out := make([]any, 0, len(x.items))
		for _, it := range x.items {
			d, err := evalNode(it, lookup, c)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, nil
	case exprMap:
		if err := charge(c, 32*len(x.keys)); err != nil {
			return nil, err
		}
		o := newOmap()
		for i, k := range x.keys {
			d, err := evalNode(x.vals[i], lookup, c)
			if err != nil {
				return nil, err
			}
			o.set(k, d)
		}
		return o, nil
	case exprUnary:
		d, err := evalNode(x.x, lookup, c)
		if err != nil {
			return nil, err
		}
		if x.op == "!" {
			return !truthy(d), nil
		}
		n, ok := number(d)
		if !ok {
			return nil, fmt.Errorf("type expr - %s", typeName(d))
		}
		if i, ok := n.(int64); ok {
			if i == math.MinInt64 {
				return nil, fmt.Errorf("type expr overflow")
			}
			return -i, nil
		}
		return -n.(float64), nil
	case exprIndex:
		base, err := evalNode(x.x, lookup, c)
		if err != nil {
			return nil, err
		}
		key, err := evalNode(x.key, lookup, c)
		if err != nil {
			return nil, err
		}
		if o, ok := base.(*omap); ok && c.strict {
			if k, isStr := key.(string); isStr {
				if _, has := o.get(k); !has {
					return nil, fmt.Errorf("unset key %s", k)
				}
			}
		}
		return indexData(base, key)
	case exprRange:
		return evalRange(x, lookup, c)
	case exprCall:
		return evalCall(x, lookup, c)
	case exprBinary:
		l, err := evalNode(x.l, lookup, c)
		if err != nil {
			return nil, err
		}
		switch x.op {
		case "&&":
			if !truthy(l) {
				return false, nil
			}
			r, err := evalNode(x.r, lookup, c)
			if err != nil {
				return nil, err
			}
			return truthy(r), nil
		case "||":
			if truthy(l) {
				return true, nil
			}
			r, err := evalNode(x.r, lookup, c)
			if err != nil {
				return nil, err
			}
			return truthy(r), nil
		}
		r, err := evalNode(x.r, lookup, c)
		if err != nil {
			return nil, err
		}
		return binary(x.op, l, r, c)
	}
	return nil, fmt.Errorf("type expr")
}

// indexData reads list[int], map[string], or string[int]. A missing map key
// is null; an index out of range is an error.
func indexData(base, key any) (any, error) {
	switch b := base.(type) {
	case *omap:
		k, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("type expr map key %s", typeName(key))
		}
		v, _ := b.get(k)
		return v, nil
	case []any:
		i, err := intIndex(key, len(b))
		if err != nil {
			return nil, err
		}
		return b[i], nil
	}
	return nil, fmt.Errorf("type expr index %s", typeName(base))
}

func intIndex(key any, n int) (int, error) {
	k, ok := number(key)
	i, isInt := k.(int64)
	if !ok || !isInt {
		return 0, fmt.Errorf("type expr index %s", typeName(key))
	}
	if i < 0 {
		i += int64(n)
	}
	if i < 0 || i >= int64(n) {
		return 0, fmt.Errorf("type expr index range")
	}
	return int(i), nil
}

func evalRange(x exprRange, lookup func(string) (any, bool), c *evalCtx) (any, error) {
	var ends [2]int64
	for i, e := range []Expr{x.lo, x.hi} {
		d, err := evalNode(e, lookup, c)
		if err != nil {
			return nil, err
		}
		n, ok := number(d)
		v, isInt := n.(int64)
		if !ok || !isInt {
			return nil, fmt.Errorf("type expr range %s", typeName(d))
		}
		ends[i] = v
	}
	lo, hi := ends[0], ends[1]
	step := int64(1)
	if hi < lo {
		step = -1
	}
	if math.Abs(float64(hi)-float64(lo)) >= rangeMax {
		return nil, fmt.Errorf("type expr range too large")
	}
	size := (hi-lo)*step + 1
	if err := charge(c, int(size)*25); err != nil {
		return nil, err
	}
	out := make([]any, 0, size)
	for v := lo; ; v += step {
		out = append(out, v)
		if v == hi {
			break
		}
	}
	return out, nil
}

func binary(op string, l, r any, c *evalCtx) (any, error) {
	switch op {
	case "==":
		return dataEqual(l, r), nil
	case "!=":
		return !dataEqual(l, r), nil
	case "<", "<=", ">", ">=":
		c, err := dataCompare(l, r)
		if err != nil {
			return nil, err
		}
		switch op {
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		case ">":
			return c > 0, nil
		}
		return c >= 0, nil
	case "in":
		return contains(r, l)
	}
	// Arithmetic reads numeric strings: arguments are text, and add 2 3
	// is 5. Comparison does not (dataEqual, dataCompare): two strings
	// compare as strings.
	if nl, ok := number(l); ok {
		if nr, ok := number(r); ok {
			return arith(op, nl, nr)
		}
	}
	// string * number repeats.
	if a, ok := l.(string); ok && op == "*" && isNum(r) {
		n, isInt := r.(int64)
		if !isInt || n < 0 || (len(a) > 0 && n > int64(dataMaxBytes/len(a))) {
			return nil, fmt.Errorf("type expr repeat")
		}
		if err := charge(c, len(a)*int(n)); err != nil {
			return nil, err
		}
		return strings.Repeat(a, int(n)), nil
	}
	if op == "+" {
		if a, ok := l.(string); ok {
			if z, ok := r.(string); ok {
				if err := charge(c, len(a)+len(z)); err != nil {
					return nil, err
				}
				return a + z, nil
			}
		}
		if a, ok := l.([]any); ok {
			if z, ok := r.([]any); ok {
				if err := charge(c, 16*(len(a)+len(z))); err != nil {
					return nil, err
				}
				return append(append(make([]any, 0, len(a)+len(z)), a...), z...), nil
			}
		}
	}
	return nil, fmt.Errorf("type expr %s %s %s", typeName(l), op, typeName(r))
}

func contains(hay, needle any) (any, error) {
	switch h := hay.(type) {
	case []any:
		for _, e := range h {
			if dataEqual(e, needle) {
				return true, nil
			}
		}
		return false, nil
	case *omap:
		k, ok := needle.(string)
		if !ok {
			return nil, fmt.Errorf("type expr in %s", typeName(needle))
		}
		_, has := h.get(k)
		return has, nil
	case string:
		s, ok := needle.(string)
		if !ok {
			return nil, fmt.Errorf("type expr in %s", typeName(needle))
		}
		return strings.Contains(h, s), nil
	}
	return nil, fmt.Errorf("type expr in %s", typeName(hay))
}

func arith(op string, a, b any) (any, error) {
	ia, aInt := a.(int64)
	ib, bInt := b.(int64)
	if aInt && bInt {
		switch op {
		case "+":
			s := ia + ib
			if (ia > 0 && ib > 0 && s < 0) || (ia < 0 && ib < 0 && s >= 0) {
				return nil, fmt.Errorf("type expr overflow")
			}
			return s, nil
		case "-":
			s := ia - ib
			if (ia >= 0 && ib < 0 && s < 0) || (ia < 0 && ib > 0 && s >= 0) {
				return nil, fmt.Errorf("type expr overflow")
			}
			return s, nil
		case "*":
			if ia != 0 && ib != 0 {
				p := ia * ib
				if p/ib != ia || (ia == -1 && ib == math.MinInt64) || (ib == -1 && ia == math.MinInt64) {
					return nil, fmt.Errorf("type expr overflow")
				}
				return p, nil
			}
			return int64(0), nil
		case "/":
			if ib == 0 {
				return nil, fmt.Errorf("type expr div zero")
			}
			if ia == math.MinInt64 && ib == -1 {
				return nil, fmt.Errorf("type expr overflow")
			}
			if ia%ib == 0 {
				return ia / ib, nil
			}
			return float64(ia) / float64(ib), nil
		case "%":
			if ib == 0 {
				return nil, fmt.Errorf("type expr div zero")
			}
			if ia == math.MinInt64 && ib == -1 {
				return int64(0), nil
			}
			return ia % ib, nil
		}
	}
	fa, fb := asFloat(a), asFloat(b)
	var out float64
	switch op {
	case "+":
		out = fa + fb
	case "-":
		out = fa - fb
	case "*":
		out = fa * fb
	case "/":
		if fb == 0 {
			return nil, fmt.Errorf("type expr div zero")
		}
		out = fa / fb
	case "%":
		if fb == 0 {
			return nil, fmt.Errorf("type expr div zero")
		}
		out = math.Mod(fa, fb)
	default:
		return nil, fmt.Errorf("type expr %s", op)
	}
	if math.IsInf(out, 0) || math.IsNaN(out) {
		return nil, fmt.Errorf("type expr overflow")
	}
	return out, nil
}
