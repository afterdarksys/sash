package lang

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Expression functions (language v3 contract). Pure and in process: no
// verb, no file, no clock, no random. Every result is charged against the
// evaluation budget before it is built. Regex is RE2, so matching is linear
// time, and a pattern is at most 1 KiB.

type exprCall struct {
	name string
	args []Expr
}

func (exprCall) expr() {}

type exprFunc struct {
	min, max int // argument counts; max -1 is unbounded
	fn       func(args []any, c *evalCtx) (any, error)
}

var exprFuncs map[string]exprFunc

func init() {
	exprFuncs = map[string]exprFunc{
		"len":     {1, 1, fnLen},
		"upper":   {1, 1, strFn(strings.ToUpper)},
		"lower":   {1, 1, strFn(strings.ToLower)},
		"trim":    {1, 1, strFn(strings.TrimSpace)},
		"split":   {2, 2, fnSplit},
		"join":    {2, 2, fnJoin},
		"replace": {3, 3, fnReplace},
		"starts":  {2, 2, fnStarts},
		"ends":    {2, 2, fnEnds},
		"pad":     {2, 2, fnPad},
		"repeat":  {2, 2, fnRepeat},
		"match":   {2, 2, fnMatch},
		"find":    {2, 2, fnFind},
		"groups":  {2, 2, fnGroups},
		"sub":     {3, 3, fnSub},
		"int":     {1, 1, fnInt},
		"float":   {1, 1, fnFloat},
		"str": {1, 1, func(a []any, c *evalCtx) (any, error) {
			if err := charge(c, dataSize(a[0], 0)); err != nil {
				return nil, err
			}
			return textOf(a[0]), nil
		}},
		"bool":    {1, 1, func(a []any, _ *evalCtx) (any, error) { return truthy(a[0]), nil }},
		"type":    {1, 1, func(a []any, _ *evalCtx) (any, error) { return typeName(a[0]), nil }},
		"sum":     {1, 1, fnSum},
		"avg":     {1, 1, fnAvg},
		"min":     {1, 1, func(a []any, _ *evalCtx) (any, error) { return fnExtreme(a[0], -1) }},
		"max":     {1, 1, func(a []any, _ *evalCtx) (any, error) { return fnExtreme(a[0], 1) }},
		"sort":    {1, 1, fnSort},
		"uniq":    {1, 1, fnUniq},
		"reverse": {1, 1, fnReverse},
		"first":   {2, 2, func(a []any, c *evalCtx) (any, error) { return fnSlice(a, c, true) }},
		"last":    {2, 2, func(a []any, c *evalCtx) (any, error) { return fnSlice(a, c, false) }},
		"range":   {2, 2, fnRange},
		"keys":    {1, 1, fnKeys},
		"values":  {1, 1, fnValues},
		"has":     {2, 2, fnHas},
		"get":     {3, 3, fnGet},
		"merge":   {2, 2, fnMerge},
	}
}

func evalCall(x exprCall, lookup func(string) (any, bool), c *evalCtx) (any, error) {
	f := exprFuncs[x.name]
	args := make([]any, len(x.args))
	for i, a := range x.args {
		d, err := evalNode(a, lookup, c)
		if err != nil {
			return nil, err
		}
		args[i] = d
	}
	return f.fn(args, c)
}

func fnErr(name string, args ...any) error {
	types := make([]string, len(args))
	for i, a := range args {
		types[i] = typeName(a)
	}
	return fmt.Errorf("type %s(%s)", name, strings.Join(types, ", "))
}

func charged(c *evalCtx, s string) (any, error) {
	if err := charge(c, len(s)); err != nil {
		return nil, err
	}
	return s, nil
}

func strFn(f func(string) string) func([]any, *evalCtx) (any, error) {
	return func(a []any, c *evalCtx) (any, error) {
		s, ok := a[0].(string)
		if !ok {
			return nil, fnErr("text", a[0])
		}
		return charged(c, f(s))
	}
}

func fnLen(a []any, _ *evalCtx) (any, error) {
	switch x := a[0].(type) {
	case string:
		return int64(len([]rune(x))), nil
	case []any:
		return int64(len(x)), nil
	case *omap:
		return int64(len(x.keys)), nil
	}
	return nil, fnErr("len", a[0])
}

func twoStrings(name string, a []any) (string, string, error) {
	s, ok1 := a[0].(string)
	t, ok2 := a[1].(string)
	if !ok1 || !ok2 {
		return "", "", fnErr(name, a...)
	}
	return s, t, nil
}

func fnSplit(a []any, c *evalCtx) (any, error) {
	s, sep, err := twoStrings("split", a)
	if err != nil {
		return nil, err
	}
	n := strings.Count(s, sep) + 1
	if sep == "" {
		n = utf8.RuneCountInString(s)
	}
	if err := charge(c, len(s)+16*n); err != nil {
		return nil, err
	}
	parts := strings.Split(s, sep)
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out, nil
}

func fnJoin(a []any, c *evalCtx) (any, error) {
	l, ok1 := a[0].([]any)
	sep, ok2 := a[1].(string)
	if !ok1 || !ok2 {
		return nil, fnErr("join", a...)
	}
	// Charge the rendered size before rendering anything.
	if err := charge(c, dataSize(l, 0)+len(sep)*len(l)); err != nil {
		return nil, err
	}
	parts := make([]string, len(l))
	for i, e := range l {
		parts[i] = textOf(e)
	}
	return strings.Join(parts, sep), nil
}

func fnReplace(a []any, c *evalCtx) (any, error) {
	s, ok1 := a[0].(string)
	old, ok2 := a[1].(string)
	nw, ok3 := a[2].(string)
	if !ok1 || !ok2 || !ok3 || old == "" {
		return nil, fnErr("replace", a...)
	}
	if n := strings.Count(s, old); n > 0 && len(nw) > len(old) {
		if err := charge(c, len(s)+n*(len(nw)-len(old))); err != nil {
			return nil, err
		}
	}
	return strings.ReplaceAll(s, old, nw), nil
}

func fnStarts(a []any, _ *evalCtx) (any, error) {
	s, p, err := twoStrings("starts", a)
	if err != nil {
		return nil, err
	}
	return strings.HasPrefix(s, p), nil
}

func fnEnds(a []any, _ *evalCtx) (any, error) {
	s, p, err := twoStrings("ends", a)
	if err != nil {
		return nil, err
	}
	return strings.HasSuffix(s, p), nil
}

func intArg(d any) (int64, bool) {
	n, ok := number(d)
	i, isInt := n.(int64)
	return i, ok && isInt
}

// pad pads to width n with spaces: on the right, or on the left when n is
// negative.
func fnPad(a []any, c *evalCtx) (any, error) {
	s, ok := a[0].(string)
	n, ok2 := intArg(a[1])
	if !ok || !ok2 {
		return nil, fnErr("pad", a...)
	}
	w := n
	if w < 0 {
		w = -w
	}
	have := int64(len([]rune(s)))
	if w <= have {
		return s, nil
	}
	if err := charge(c, int(w-have)+len(s)); err != nil {
		return nil, err
	}
	fill := strings.Repeat(" ", int(w-have))
	if n < 0 {
		return fill + s, nil
	}
	return s + fill, nil
}

func fnRepeat(a []any, c *evalCtx) (any, error) {
	s, ok := a[0].(string)
	n, ok2 := intArg(a[1])
	if !ok || !ok2 || n < 0 || (len(s) > 0 && n > int64(dataMaxBytes/len(s))) {
		return nil, fnErr("repeat", a...)
	}
	if err := charge(c, len(s)*int(n)); err != nil {
		return nil, err
	}
	return strings.Repeat(s, int(n)), nil
}

// regexMax bounds a pattern. RE2 matches in linear time.
const regexMax = 1024

var (
	reMu    sync.Mutex
	reCache = map[string]*regexp.Regexp{}
)

func compileRE(p string) (*regexp.Regexp, error) {
	if len(p) > regexMax {
		return nil, fmt.Errorf("type regex too long")
	}
	reMu.Lock()
	defer reMu.Unlock()
	if re, ok := reCache[p]; ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("type regex")
	}
	if len(reCache) >= 256 {
		reCache = map[string]*regexp.Regexp{}
	}
	reCache[p] = re
	return re, nil
}

func reArgs(name string, a []any) (string, *regexp.Regexp, error) {
	s, p, err := twoStrings(name, a)
	if err != nil {
		return "", nil, err
	}
	re, err := compileRE(p)
	return s, re, err
}

func fnMatch(a []any, _ *evalCtx) (any, error) {
	s, re, err := reArgs("match", a)
	if err != nil {
		return nil, err
	}
	return re.MatchString(s), nil
}

func fnFind(a []any, _ *evalCtx) (any, error) {
	s, re, err := reArgs("find", a)
	if err != nil {
		return nil, err
	}
	loc := re.FindStringIndex(s)
	if loc == nil {
		return nil, nil
	}
	return s[loc[0]:loc[1]], nil
}

func fnGroups(a []any, c *evalCtx) (any, error) {
	s, re, err := reArgs("groups", a)
	if err != nil {
		return nil, err
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil, nil
	}
	if err := charge(c, 16*len(m)); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(m)-1)
	for _, g := range m[1:] {
		out = append(out, g)
	}
	return out, nil
}

func fnSub(a []any, c *evalCtx) (any, error) {
	s, ok1 := a[0].(string)
	p, ok2 := a[1].(string)
	repl, ok3 := a[2].(string)
	if !ok1 || !ok2 || !ok3 {
		return nil, fnErr("sub", a...)
	}
	re, err := compileRE(p)
	if err != nil {
		return nil, err
	}
	if err := charge(c, subBound(re, s, repl)); err != nil {
		return nil, err
	}
	return re.ReplaceAllString(s, repl), nil
}

// subMatchCount is how many matches subBound counts before it assumes the
// worst, so counting itself stays bounded.
const subMatchCount = 100000

// subBound is an upper bound on ReplaceAllString's result, computed before
// it runs: the input, plus repl per match, plus each $ reference expanding
// to at most every byte of the input (matches do not overlap).
func subBound(re *regexp.Regexp, s, repl string) int {
	n := len(re.FindAllStringIndex(s, subMatchCount))
	if n == subMatchCount {
		n = len(s) + 1
	}
	refs := strings.Count(repl, "$")
	bound := len(s) + n*len(repl) + refs*len(s)
	if bound < 0 {
		return dataMaxBytes + 1
	}
	return bound
}

func fnInt(a []any, _ *evalCtx) (any, error) {
	switch x := a[0].(type) {
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	}
	n, ok := number(a[0])
	if !ok {
		return nil, fnErr("int", a[0])
	}
	if f, isF := n.(float64); isF {
		if f >= 9.2e18 || f <= -9.2e18 {
			return nil, fmt.Errorf("type expr overflow")
		}
		return int64(math.Trunc(f)), nil
	}
	return n, nil
}

func fnFloat(a []any, _ *evalCtx) (any, error) {
	n, ok := number(a[0])
	if !ok {
		return nil, fnErr("float", a[0])
	}
	return asFloat(n), nil
}

func numList(name string, d any) ([]any, error) {
	l, ok := d.([]any)
	if !ok {
		return nil, fnErr(name, d)
	}
	out := make([]any, len(l))
	for i, e := range l {
		n, ok := number(e)
		if !ok {
			return nil, fnErr(name, e)
		}
		out[i] = n
	}
	return out, nil
}

func fnSum(a []any, _ *evalCtx) (any, error) {
	l, err := numList("sum", a[0])
	if err != nil {
		return nil, err
	}
	var total any = int64(0)
	for _, n := range l {
		if total, err = arith("+", total, n); err != nil {
			return nil, err
		}
	}
	return total, nil
}

func fnAvg(a []any, c *evalCtx) (any, error) {
	l, err := numList("avg", a[0])
	if err != nil {
		return nil, err
	}
	if len(l) == 0 {
		return nil, nil
	}
	total := 0.0
	for _, n := range l {
		total += asFloat(n)
	}
	return total / float64(len(l)), nil
}

func fnExtreme(d any, dir int) (any, error) {
	l, ok := d.([]any)
	if !ok {
		return nil, fnErr("min/max", d)
	}
	if len(l) == 0 {
		return nil, nil
	}
	best := l[0]
	for _, e := range l[1:] {
		cmp, err := dataCompare(e, best)
		if err != nil {
			return nil, err
		}
		if cmp*dir > 0 {
			best = e
		}
	}
	return best, nil
}

func fnSort(a []any, c *evalCtx) (any, error) {
	l, ok := a[0].([]any)
	if !ok {
		return nil, fnErr("sort", a[0])
	}
	if err := charge(c, 16*len(l)); err != nil {
		return nil, err
	}
	out := append([]any(nil), l...)
	var cerr error
	sort.SliceStable(out, func(i, j int) bool {
		cmp, err := dataCompare(out[i], out[j])
		if err != nil && cerr == nil {
			cerr = err
		}
		return cmp < 0
	})
	if cerr != nil {
		return nil, cerr
	}
	return out, nil
}

// uniqKey is a value's identity for uniq: its type and its JSON text.
func uniqKey(d any) string {
	if n, ok := number(d); ok && !isText(d) {
		return "n" + textOf(asFloat(n))
	}
	return typeName(d) + ":" + textOf(d)
}

func isText(d any) bool { _, ok := d.(string); return ok }

func fnUniq(a []any, c *evalCtx) (any, error) {
	l, ok := a[0].([]any)
	if !ok {
		return nil, fnErr("uniq", a[0])
	}
	if err := charge(c, 16*len(l)); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]any, 0, len(l))
	for _, e := range l {
		k := uniqKey(e)
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out, nil
}

func fnReverse(a []any, c *evalCtx) (any, error) {
	switch x := a[0].(type) {
	case []any:
		if err := charge(c, 16*len(x)); err != nil {
			return nil, err
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[len(x)-1-i] = e
		}
		return out, nil
	case string:
		r := []rune(x)
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
		return charged(c, string(r))
	}
	return nil, fnErr("reverse", a[0])
}

func fnSlice(a []any, c *evalCtx, head bool) (any, error) {
	l, ok := a[0].([]any)
	n, ok2 := intArg(a[1])
	if !ok || !ok2 || n < 0 {
		return nil, fnErr("first/last", a...)
	}
	if n > int64(len(l)) {
		n = int64(len(l))
	}
	if err := charge(c, 16*int(n)); err != nil {
		return nil, err
	}
	if head {
		return append([]any(nil), l[:n]...), nil
	}
	return append([]any(nil), l[int64(len(l))-n:]...), nil
}

func fnRange(a []any, c *evalCtx) (any, error) {
	return evalRange(exprRange{exprLit{a[0]}, exprLit{a[1]}}, nil, c)
}

func fnKeys(a []any, c *evalCtx) (any, error) {
	o, ok := a[0].(*omap)
	if !ok {
		return nil, fnErr("keys", a[0])
	}
	if err := charge(c, 16*len(o.keys)); err != nil {
		return nil, err
	}
	out := make([]any, len(o.keys))
	for i, k := range o.keys {
		out[i] = k
	}
	return out, nil
}

func fnValues(a []any, c *evalCtx) (any, error) {
	o, ok := a[0].(*omap)
	if !ok {
		return nil, fnErr("values", a[0])
	}
	if err := charge(c, 16*len(o.keys)); err != nil {
		return nil, err
	}
	out := make([]any, len(o.keys))
	for i, k := range o.keys {
		out[i] = o.m[k]
	}
	return out, nil
}

func fnHas(a []any, _ *evalCtx) (any, error) {
	o, ok := a[0].(*omap)
	k, ok2 := a[1].(string)
	if !ok || !ok2 {
		return nil, fnErr("has", a...)
	}
	_, has := o.get(k)
	return has, nil
}

func fnGet(a []any, _ *evalCtx) (any, error) {
	o, ok := a[0].(*omap)
	k, ok2 := a[1].(string)
	if !ok || !ok2 {
		return nil, fnErr("get", a...)
	}
	if v, has := o.get(k); has {
		return v, nil
	}
	return a[2], nil
}

func fnMerge(a []any, c *evalCtx) (any, error) {
	x, ok := a[0].(*omap)
	y, ok2 := a[1].(*omap)
	if !ok || !ok2 {
		return nil, fnErr("merge", a...)
	}
	if err := charge(c, 32*(len(x.keys)+len(y.keys))); err != nil {
		return nil, err
	}
	o := newOmap()
	for _, k := range x.keys {
		o.set(k, x.m[k])
	}
	for _, k := range y.keys {
		o.set(k, y.m[k])
	}
	return o, nil
}

// funcNames lists the expression functions for builtin.
func funcNames() []string {
	out := make([]string, 0, len(exprFuncs))
	for k := range exprFuncs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
