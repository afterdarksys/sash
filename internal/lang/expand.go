package lang

// Threats: expansion rewrites the text the grant check, the agent, and the
// audit argv see. It does not authorize a call and it does not hide a
// mutation behind a short name. A glob lists through file.read (argv ls)
// only after Has and AllowsRead, and drops anything outside that directory.
// A trailing (/) keeps directories only and does not run a host binary.
// History, braces, and globs fail closed. This file does not log the line,
// a path, or a grant, and it does not compare secret bytes.

import (
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

const (
	expandMax   = 1 << 20
	globMax     = 1000
	globDepth   = 8
	globWordCap = 32
	braceCap    = 64
	bracePasses = 8
	histBang    = 1
	histDollar  = 2
)

// ExpandProblem is a failed expansion. Msg has no trailing newline.
// Status 2 is a type error, 126 is denial, 1 is a parent failure.
type ExpandProblem struct {
	Status int
	Msg    string
}

// Expand rewrites history, then braces, then globs. An empty src is unchanged.
// History is copied. A second call is a no-op once designators, brace patterns,
// and unquoted globs are gone.
func Expand(src string, g grant.Grant, p parent.Parent, opt Options) (string, *ExpandProblem) {
	if src == "" {
		return "", nil
	}
	hist := append([]string(nil), opt.History...)
	out, prob := expandHistory(src, hist)
	if prob != nil {
		return "", prob
	}
	if len(out) > expandMax {
		return "", braceProb()
	}
	out, prob = expandBraces(out)
	if prob != nil {
		return "", prob
	}
	if len(out) > expandMax {
		return "", braceProb()
	}
	if opt.State == nil || !opt.State.noglob {
		out, prob = expandGlobs(out, g, p, opt)
		if prob != nil {
			return "", prob
		}
	}
	if len(out) > expandMax {
		return "", braceProb()
	}
	return out, nil
}

func braceProb() *ExpandProblem {
	return &ExpandProblem{Status: exitType, Msg: "type brace"}
}

func globProb() *ExpandProblem {
	return &ExpandProblem{Status: exitType, Msg: "type glob"}
}

func histProb() *ExpandProblem {
	return &ExpandProblem{Status: exitType, Msg: "type history"}
}

func expandHistory(src string, hist []string) (string, *ExpandProblem) {
	lines, finalNL := logicalLines(src)
	if len(lines) == 0 && !finalNL {
		return src, nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		exp, prob := expandLine(line, hist)
		if prob != nil {
			return "", prob
		}
		out = append(out, exp)
		if strings.TrimSpace(exp) != "" {
			hist = append(hist, exp)
		}
	}
	joined := strings.Join(out, "\n")
	if finalNL {
		joined += "\n"
	}
	return joined, nil
}

// logicalLines splits on newlines outside quotes. A backslash inside double
// quotes hides the next byte. The result does not add an empty line when src
// already ends with a newline.
func logicalLines(src string) ([]string, bool) {
	if src == "" {
		return nil, false
	}
	var lines []string
	var b strings.Builder
	var quote byte
	esc := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if quote == '"' && esc {
			b.WriteByte(c)
			esc = false
			continue
		}
		if quote == '"' && c == '\\' {
			b.WriteByte(c)
			esc = true
			continue
		}
		if quote == 0 && (c == '\'' || c == '"') {
			quote = c
			b.WriteByte(c)
			continue
		}
		if quote != 0 && c == quote {
			quote = 0
			b.WriteByte(c)
			continue
		}
		if quote == 0 && c == '\n' {
			lines = append(lines, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	finalNL := src[len(src)-1] == '\n' && quote == 0
	if quote != 0 || !finalNL {
		lines = append(lines, b.String())
	}
	return lines, finalNL
}

func expandLine(s string, hist []string) (string, *ExpandProblem) {
	var b strings.Builder
	ix := newExprSpans(s)
	for i := 0; i < len(s); {
		// A [ ] expression is not history text: "!" there is not.
		if n := ix.span(s, i); n > 0 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		if commentAt(s, i) {
			b.WriteString(s[i:])
			break
		}
		c := s[i]
		if c == '\'' {
			n := scanQuote(s[i:], '\'')
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		if c == '"' {
			out, n, prob := expandDouble(s[i:], hist)
			if prob != nil {
				return "", prob
			}
			b.WriteString(out)
			i += n
			continue
		}
		if (i == 0 || histBound(s[i-1])) && i+1 < len(s) && s[i] == '!' {
			kind, n := histDesignator(s[i:])
			if kind != 0 && (i+n == len(s) || histBound(s[i+n])) {
				rep, prob := historyReplace(kind, hist, false)
				if prob != nil {
					return "", prob
				}
				b.WriteString(rep)
				i += n
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), nil
}

func expandDouble(s string, hist []string) (string, int, *ExpandProblem) {
	var b strings.Builder
	b.WriteByte('"')
	esc := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if esc {
			b.WriteByte(c)
			esc = false
			continue
		}
		if c == '\\' {
			b.WriteByte(c)
			esc = true
			continue
		}
		if c == '"' {
			b.WriteByte('"')
			return b.String(), i + 1, nil
		}
		prevOK := i == 1 || histBound(s[i-1])
		if prevOK && c == '!' {
			kind, n := histDesignator(s[i:])
			end := i + n
			if kind != 0 && (end == len(s) || s[end] == '"' || histBound(s[end])) {
				rep, prob := historyReplace(kind, hist, true)
				if prob != nil {
					return "", 0, prob
				}
				b.WriteString(rep)
				i = end - 1
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String(), len(s), nil
}

func histDesignator(s string) (int, int) {
	if len(s) >= 2 && s[0] == '!' && s[1] == '!' {
		return histBang, 2
	}
	if len(s) >= 2 && s[0] == '!' && s[1] == '$' {
		return histDollar, 2
	}
	return 0, 0
}

func historyReplace(kind int, hist []string, quoted bool) (string, *ExpandProblem) {
	if len(hist) == 0 {
		return "", histProb()
	}
	newest := hist[len(hist)-1]
	var rep string
	if kind == histBang {
		rep = newest
	} else {
		fields := strings.Fields(newest)
		if len(fields) == 0 {
			return "", histProb()
		}
		rep = fields[len(fields)-1]
	}
	if quoted {
		if strings.Contains(rep, "\"") || strings.Contains(rep, "\\") || strings.Contains(rep, "\n") {
			return "", histProb()
		}
		return rep, nil
	}
	if kind == histBang {
		return rep, nil
	}
	q, ok := quoteArg(rep)
	if !ok {
		return "", histProb()
	}
	return q, nil
}

func histBound(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '|', '&', ';', '<', '>', '(', ')', '{', '}', '=':
		return true
	default:
		return false
	}
}

func commentAt(s string, i int) bool {
	return i < len(s) && s[i] == '#' && (i == 0 || histBound(s[i-1]))
}

func scanQuote(s string, q byte) int {
	if len(s) == 0 {
		return 0
	}
	if q == '\'' {
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				return i + 1
			}
		}
		return len(s)
	}
	esc := false
	for i := 1; i < len(s); i++ {
		if esc {
			esc = false
			continue
		}
		if s[i] == '\\' {
			esc = true
			continue
		}
		if s[i] == '"' {
			return i + 1
		}
	}
	return len(s)
}

func expandBraces(src string) (string, *ExpandProblem) {
	cur := src
	for pass := 0; pass < bracePasses; pass++ {
		next, changed, prob := braceOnce(cur)
		if prob != nil {
			return "", prob
		}
		if !changed {
			return cur, nil
		}
		if len(next) > expandMax {
			return "", braceProb()
		}
		cur = next
	}
	_, changed, prob := braceOnce(cur)
	if prob != nil {
		return "", prob
	}
	if changed {
		return "", braceProb()
	}
	return cur, nil
}

func braceOnce(src string) (string, bool, *ExpandProblem) {
	next, prob := mapWords(src, func(word string) (string, *ExpandProblem) {
		out, _, err := braceWordOnce(word)
		return out, err
	})
	if prob != nil {
		return "", false, prob
	}
	return next, next != src, nil
}

func mapWords(src string, fn func(string) (string, *ExpandProblem)) (string, *ExpandProblem) {
	var b strings.Builder
	ix := newExprSpans(src)
	for i := 0; i < len(src); {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			b.WriteByte(c)
			i++
			continue
		}
		if commentAt(src, i) {
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			b.WriteString(src[i:j])
			i = j
			continue
		}
		// A [ ] expression has no braces to expand and no globs to list:
		// its "*" is multiplication and its "{" a map.
		if n := ix.span(src, i); n > 0 {
			b.WriteString(src[i : i+n])
			i += n
			continue
		}
		if c == '\'' || c == '"' {
			n := scanQuote(src[i:], c)
			b.WriteString(src[i : i+n])
			i += n
			continue
		}
		j := i
		for j < len(src) {
			d := src[j]
			if d == ' ' || d == '\t' || d == '\n' || d == '\r' || d == '\'' || d == '"' || commentAt(src, j) {
				break
			}
			j++
		}
		repl, prob := fn(src[i:j])
		if prob != nil {
			return "", prob
		}
		b.WriteString(repl)
		if b.Len() > expandMax {
			return "", braceProb()
		}
		i = j
	}
	return b.String(), nil
}

func braceWordOnce(word string) (string, bool, *ExpandProblem) {
	for i := 0; i < len(word); {
		if word[i] != '{' {
			i++
			continue
		}
		out, ok, next, prob := expandOneBrace(word, i)
		if prob != nil {
			return "", false, prob
		}
		if ok {
			return out, true, nil
		}
		if next <= i {
			next = i + 1
		}
		i = next
	}
	return word, false, nil
}

func expandOneBrace(word string, start int) (string, bool, int, *ExpandProblem) {
	open := start
	for {
		closeAt, found := findClose(word, open)
		if !found {
			return "", false, open + 1, nil
		}
		body := word[open+1 : closeAt]
		if rel := strings.IndexByte(body, '{'); rel >= 0 {
			open = open + 1 + rel
			continue
		}
		if strings.ContainsAny(body, " \t\r\n'\"}") {
			return "", false, closeAt + 1, nil
		}
		alts, prob, literal := braceAlts(body)
		if prob != nil {
			return "", false, 0, prob
		}
		if alts == nil {
			return "", false, closeAt + 1, nil
		}
		prefix := word[:open]
		suffix := word[closeAt+1:]
		if literal && prefix == "" && suffix == "" {
			return "'" + word + "'", true, len(word), nil
		}
		if literal {
			return prefix + alts[0] + suffix, true, len(word), nil
		}
		joined, prob := joinAlts(prefix, alts, suffix)
		if prob != nil {
			return "", false, 0, prob
		}
		return joined, true, len(word), nil
	}
}

func findClose(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func braceAlts(body string) ([]string, *ExpandProblem, bool) {
	if body == "" {
		return nil, nil, false
	}
	if strings.Contains(body, ",") {
		n := strings.Count(body, ",") + 1
		if n > braceCap {
			return nil, braceProb(), false
		}
		parts := strings.Split(body, ",")
		for _, p := range parts {
			if p == "" || strings.ContainsAny(p, " \t\r\n'\"{}") {
				return nil, nil, false
			}
		}
		return parts, nil, false
	}
	if strings.Contains(body, "..") {
		alts, prob := braceRange(body)
		return alts, prob, false
	}
	return []string{body}, nil, true
}

func braceRange(body string) ([]string, *ExpandProblem) {
	parts := strings.Split(body, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, nil
	}
	for _, p := range parts {
		if p == "" {
			return nil, nil
		}
	}
	step := int64(1)
	if len(parts) == 3 {
		n, ok := parseBraceInt(parts[2])
		if !ok {
			return nil, nil
		}
		step = n
	}
	if len(parts[0]) == 1 && len(parts[1]) == 1 && isBraceLetter(parts[0][0]) && isBraceLetter(parts[1][0]) {
		if !sameLetterClass(parts[0][0], parts[1][0]) {
			return nil, nil
		}
		nums, prob := collectNums(int64(parts[0][0]), int64(parts[1][0]), step)
		if prob != nil {
			return nil, prob
		}
		out := make([]string, len(nums))
		for i, n := range nums {
			out[i] = string([]byte{byte(n)})
		}
		return out, nil
	}
	start, sok := parseBraceInt(parts[0])
	end, eok := parseBraceInt(parts[1])
	if !sok || !eok {
		return nil, nil
	}
	nums, prob := collectNums(start, end, step)
	if prob != nil {
		return nil, prob
	}
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = strconv.FormatInt(n, 10)
	}
	return out, nil
}

func isBraceLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func sameLetterClass(a, b byte) bool {
	lower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	upper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	return (lower(a) && lower(b)) || (upper(a) && upper(b))
}

func parseBraceInt(s string) (int64, bool) {
	if s == "" || strings.Contains(s, "+") {
		return 0, false
	}
	neg := false
	d := s
	if s[0] == '-' {
		neg = true
		d = s[1:]
	}
	if d == "" {
		return 0, false
	}
	if len(d) > 1 && d[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(d); i++ {
		if d[i] < '0' || d[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(d, 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		if d == "0" {
			return 0, true
		}
		return -n, true
	}
	return n, true
}

func collectNums(start, end, step int64) ([]int64, *ExpandProblem) {
	if step == 0 || step == math.MinInt64 {
		return nil, braceProb()
	}
	if start == end {
		return []int64{start}, nil
	}
	if start < end && step < 0 {
		step = -step
	}
	if start > end && step > 0 {
		step = -step
	}
	var out []int64
	s := start
	for {
		if len(out) >= braceCap {
			return nil, braceProb()
		}
		out = append(out, s)
		if rangePast(s, end, step) {
			break
		}
		if step > 0 {
			if s > math.MaxInt64-step {
				return nil, braceProb()
			}
		} else if s < math.MinInt64-step {
			return nil, braceProb()
		}
		s += step
	}
	return out, nil
}

func rangePast(s, end, step int64) bool {
	if step > 0 {
		if s >= end {
			return true
		}
		if s > math.MaxInt64-step {
			return false
		}
		return s+step > end
	}
	if s <= end {
		return true
	}
	if s < math.MinInt64-step {
		return false
	}
	return s+step < end
}

func joinAlts(prefix string, alts []string, suffix string) (string, *ExpandProblem) {
	var b strings.Builder
	for i, a := range alts {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(prefix)
		b.WriteString(a)
		b.WriteString(suffix)
		if b.Len() > expandMax {
			return "", braceProb()
		}
	}
	return b.String(), nil
}

func expandGlobs(src string, g grant.Grant, p parent.Parent, opt Options) (string, *ExpandProblem) {
	return mapWords(src, func(word string) (string, *ExpandProblem) {
		if !strings.Contains(word, "*") {
			return word, nil
		}
		return globWord(word, g, p, opt)
	})
}

func globWord(word string, g grant.Grant, p parent.Parent, opt Options) (string, *ExpandProblem) {
	dir, pat, rec, dirs, prob := splitStar(word)
	if prob != nil {
		return "", prob
	}
	full, prob := globDir(dir, opt)
	if prob != nil {
		return "", prob
	}
	if !g.Has("file.read") || !g.AllowsRead(full) {
		return "", &ExpandProblem{Status: exitDenied, Msg: "denied file.read"}
	}
	if _, err := path.Match(pat, "x"); err != nil {
		return "", globProb()
	}
	if p == nil {
		return "", &ExpandProblem{Status: 1, Msg: "parent file.read"}
	}
	var matches []string
	if prob := walk(p, g, full, full, pat, rec, dirs, 0, &matches); prob != nil {
		return "", prob
	}
	if len(matches) == 0 {
		return "", globProb()
	}
	sort.Strings(matches)
	var b strings.Builder
	for i, m := range matches {
		q, ok := quoteArg(m)
		if !ok {
			return "", globProb()
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(q)
		if b.Len() > expandMax {
			return "", braceProb()
		}
	}
	return b.String(), nil
}

func globDir(dir string, opt Options) (string, *ExpandProblem) {
	if dir == "" {
		if opt.Prefix == "" {
			return "", &ExpandProblem{Status: exitType, Msg: "prefix_unset"}
		}
		return path.Clean(opt.Prefix), nil
	}
	if strings.HasPrefix(dir, "/") {
		return path.Clean(dir), nil
	}
	full, err := resolvePath(opt.Prefix, dir)
	if err != nil {
		return "", &ExpandProblem{Status: exitType, Msg: "prefix_unset"}
	}
	return full, nil
}

func splitStar(word string) (string, string, bool, bool, *ExpandProblem) {
	dirs := false
	if strings.HasSuffix(word, "(/)") {
		dirs = true
		word = word[:len(word)-len("(/)")]
		if !strings.Contains(word, "*") {
			return "", "", false, false, globProb()
		}
	}
	if strings.Contains(word, "***") {
		return "", "", false, false, globProb()
	}
	at := segmentStarStar(word)
	if strings.Contains(word, "**") && at < 0 {
		return "", "", false, false, globProb()
	}
	if at >= 0 {
		if strings.Count(word, "**") != 1 {
			return "", "", false, false, globProb()
		}
		pre := word[:at]
		post := word[at+2:]
		if strings.Contains(pre, "*") || strings.HasPrefix(post, "*") {
			return "", "", false, false, globProb()
		}
		var pat string
		switch {
		case post == "" || post == "/":
			pat = "*"
		case strings.HasPrefix(post, "/"):
			name := post[1:]
			if strings.Contains(name, "/") {
				return "", "", false, false, globProb()
			}
			if name == "" {
				pat = "*"
			} else {
				pat = name
			}
		default:
			return "", "", false, false, globProb()
		}
		if pre != "" && !strings.HasSuffix(pre, "/") {
			return "", "", false, false, globProb()
		}
		dir := ""
		if pre != "" {
			dir = path.Clean(pre)
		}
		return dir, pat, true, dirs, nil
	}
	star := strings.IndexByte(word, '*')
	if star < 0 {
		return "", word, false, dirs, nil
	}
	if strings.Contains(word[star+1:], "/") {
		return "", "", false, false, globProb()
	}
	pre := word[:star]
	rest := word[star:]
	if strings.HasSuffix(pre, "/") {
		return path.Clean(pre), rest, false, dirs, nil
	}
	if strings.Contains(pre, "/") {
		cleaned := path.Clean(pre)
		return path.Dir(cleaned), path.Base(pre) + rest, false, dirs, nil
	}
	return "", word, false, dirs, nil
}

func segmentStarStar(word string) int {
	i := 0
	for i < len(word) {
		j := strings.Index(word[i:], "**")
		if j < 0 {
			return -1
		}
		at := i + j
		prevOK := at == 0 || word[at-1] == '/'
		nextOK := at+2 == len(word) || word[at+2] != '*'
		if prevOK && nextOK {
			return at
		}
		if !nextOK {
			return -1
		}
		i = at + 1
	}
	return -1
}

func walk(p parent.Parent, g grant.Grant, root, dir, pat string, rec, dirs bool, depth int, matches *[]string) *ExpandProblem {
	if len(*matches) > globWordCap {
		return globProb()
	}
	res, err := p.Do(parent.Verb{Key: "file.read", Argv: []string{"ls", dir}, Path: dir})
	if err != nil {
		return &ExpandProblem{Status: 1, Msg: "parent file.read"}
	}
	if res.Exit != 0 || res.Kind != parent.KindRows || len(res.Rows) >= globMax {
		return globProb()
	}
	var childDirs []string
	for _, row := range res.Rows {
		name := row["name"]
		typ := row["type"]
		if !keepName(name) {
			continue
		}
		full := path.Clean(path.Join(dir, name))
		if !underDir(full, root) || !g.AllowsRead(full) {
			continue
		}
		if typ != "file" && typ != "dir" {
			continue
		}
		ok, merr := path.Match(pat, name)
		if merr != nil {
			return globProb()
		}
		if ok && (!dirs || typ == "dir") {
			*matches = append(*matches, full)
			if len(*matches) > globWordCap {
				return globProb()
			}
		}
		if rec && typ == "dir" && depth+1 < globDepth {
			childDirs = append(childDirs, full)
		}
	}
	sort.Strings(childDirs)
	for _, cd := range childDirs {
		if !g.AllowsRead(cd) {
			continue
		}
		if prob := walk(p, g, root, cd, pat, true, dirs, depth+1, matches); prob != nil {
			return prob
		}
	}
	return nil
}

func keepName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		return false
	}
	return true
}

func underDir(full, dir string) bool {
	if full == dir {
		return true
	}
	if dir == "/" {
		return strings.HasPrefix(full, "/")
	}
	return strings.HasPrefix(full, dir+"/")
}

func quoteArg(s string) (string, bool) {
	if safePlain(s) {
		return s, true
	}
	if strings.Contains(s, "'") || strings.Contains(s, "\n") {
		return "", false
	}
	return "'" + s + "'", true
}

func safePlain(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "*\\$[]") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || bareStop(c) {
			return false
		}
	}
	return true
}
