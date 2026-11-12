package line

// Tab completes the word at the cursor from what sash already holds: the
// compiled-in table (forms, subcommands, Flags), the session's names and
// functions and those bound earlier on the line, and paths this session
// has already seen. Threats: Tab calls no parent and lists no directory,
// so it adds no agent call or audit row and cannot hang on the verb pipe;
// a path is offered only when grant.AllowsRead covers it, a denied row is
// dim and inserts nothing, every shown cell goes through needsCell, and an
// inserted path that is not a plain word is quoted, so a crafted name can
// neither drive the terminal nor split into words. A value is never shown.

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/afterdarksys/sash/internal/lang"
)

const (
	// seenMax bounds the seen paths; the oldest go first.
	seenMax = 1024
	// menuMax bounds one completion menu.
	menuMax = 50
)

// wordStop ends a word for Tab.
const wordStop = " \t;|{}()<>&"

// cmdKeywords lead a command without being it: if ps<Tab> completes ps.
var cmdKeywords = map[string]bool{"if": true, "while": true, "do": true, "else": true, "try": true, "&&": true, "||": true}

// remember keeps the paths a submitted line names: its literal path words
// (glob results included, since submit sees the expanded line), resolved
// against the prefix the line ran under, and the prefix it left.
func (e *Editor) remember(line, before, after string) {
	if lang.DeclaresSecret(line) {
		return
	}
	for _, w := range lang.PathWords(line) {
		if !strings.HasPrefix(w, "/") {
			if before == "" {
				continue
			}
			w = path.Join(before, w)
		}
		e.see(path.Clean(w))
	}
	if after != "" {
		e.see(path.Clean(after))
	}
}

func (e *Editor) see(p string) {
	if e.seenSet == nil {
		e.seenSet = map[string]bool{}
	}
	if e.seenSet[p] || strings.ContainsAny(p, "\n\r\x00") {
		return
	}
	e.seenSet[p] = true
	e.seen = append(e.seen, p)
	if len(e.seen) > seenMax {
		delete(e.seenSet, e.seen[0])
		e.seen = e.seen[1:]
	}
}

// complete is the rows for the word at the cursor and the span of the line
// a chosen row replaces.
func (e *Editor) complete() ([]completion, int, int) {
	line, cur := e.line, e.cursor
	start := cur
	for start > 0 && !strings.ContainsRune(wordStop, rune(line[start-1])) {
		start--
	}
	word := line[start:cur]
	cmdFrom := strings.LastIndexAny(line[:start], ";|{(&") + 1
	for {
		for cmdFrom < start && (line[cmdFrom] == ' ' || line[cmdFrom] == '\t') {
			cmdFrom++
		}
		end := strings.IndexAny(line[cmdFrom:cur], " \t")
		if end < 0 || !cmdKeywords[line[cmdFrom:cmdFrom+end]] {
			break
		}
		cmdFrom += end
	}
	cmd := line[cmdFrom:cur]
	if strings.HasPrefix(word, "$") {
		return e.completeNames(line[:start], word[1:]), start, cur
	}
	if rows := e.completeCommand(line[:start], cmd); len(rows) > 0 {
		return rows, cmdFrom, cur
	}
	if strings.HasPrefix(word, "-") {
		return completeFlags(cmd, word), start, cur
	}
	return e.completePaths(word), start, cur
}

// completeCommand offers forms that strictly extend what is typed of the
// command, and functions when it is still one word.
func (e *Editor) completeCommand(before, cmd string) []completion {
	if cmd == "" {
		return nil
	}
	var out []completion
	for _, row := range table {
		if len(row.insert) > len(cmd) && strings.HasPrefix(row.insert, cmd) {
			out = append(out, row)
		}
	}
	if !strings.ContainsAny(cmd, " \t") {
		for _, f := range uniqSorted(append(e.opt.State.FnNames(), lineFns(before)...)) {
			if strings.HasPrefix(f, cmd) && len(f) > len(cmd) {
				out = append(out, completion{insert: f + " ", form: f, desc: "function"})
			}
		}
	}
	return out
}

// completeFlags offers the Flags of the longest form the command starts
// with.
func completeFlags(cmd, word string) []completion {
	var best lang.Builtin
	for _, b := range lang.Builtins() {
		if (cmd == b.Form || strings.HasPrefix(cmd, b.Form+" ")) && len(b.Form) > len(best.Form) {
			best = b
		}
	}
	var out []completion
	for _, f := range best.Flags {
		if strings.HasPrefix(f, word) {
			out = append(out, completion{insert: f + " ", form: f, desc: "flag of " + best.Form})
		}
	}
	return out
}

// completeNames offers names, never values: the session's and those bound
// earlier on the line.
func (e *Editor) completeNames(before, prefix string) []completion {
	var out []completion
	for _, n := range uniqSorted(append(e.opt.State.Names(), lineNames(before)...)) {
		if strings.HasPrefix(n, prefix) {
			out = append(out, completion{insert: "$" + n, form: "$" + n, desc: "name"})
		}
		if len(out) == menuMax {
			break
		}
	}
	return out
}

// completePaths offers seen paths the grant lets this session read.
// A relative word matches seen paths under the prefix.
func (e *Editor) completePaths(word string) []completion {
	typed := strings.TrimLeft(word, `'"`)
	base := ""
	if !strings.HasPrefix(typed, "/") && e.opt.Prefix != "" {
		base = strings.TrimSuffix(path.Clean(e.opt.Prefix), "/") + "/"
	}
	var out []completion
	for _, p := range sortedCopy(e.seen) {
		if !e.g.AllowsRead(p) {
			continue
		}
		show := p
		if base != "" {
			if !strings.HasPrefix(p, base) {
				continue
			}
			show = p[len(base):]
		} else if !strings.HasPrefix(typed, "/") && typed != "" {
			continue
		}
		if !strings.HasPrefix(show, typed) || show == typed {
			continue
		}
		out = append(out, completion{insert: quotePath(show), form: show, desc: "seen path", key: "file.read"})
		if len(out) == menuMax {
			break
		}
	}
	return out
}

// quotePath keeps a plain path as it is and quotes any other: single
// quotes, or double quotes with ", \, and $ escaped when it holds a '.
func quotePath(p string) string {
	plain := p != ""
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_./:@%+=,-", c) >= 0) {
			plain = false
			break
		}
	}
	if plain {
		return p
	}
	if !strings.Contains(p, "'") {
		return "'" + p + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	return `"` + r.Replace(p) + `"`
}

var (
	lineFnRe   = regexp.MustCompile(`(?:^|[\s;{])fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)`)
	lineBindRe = regexp.MustCompile(`(?:^|[\s;{])(?:let|catch|for)\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*,\s*([A-Za-z_][A-Za-z0-9_]*))?`)
	lineDeclRe = regexp.MustCompile(`(?:^|[\s;{])declare(?:\s+-[A-Za-z]+)*\s+([A-Za-z_][A-Za-z0-9_]*)`)
	lineWithRe = regexp.MustCompile(`(?:^|[\s,{])([A-Za-z_][A-Za-z0-9_]*)\s*=[^=]`)
	nameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// lineFns are the fn names defined earlier on the line.
func lineFns(before string) []string {
	var out []string
	for _, m := range lineFnRe.FindAllStringSubmatch(before, -1) {
		out = append(out, m[1])
	}
	return out
}

// lineNames are the names bound earlier on the line: let, for, catch,
// declare, fn parameters, and with items. A name is offered, never what
// it is bound to.
func lineNames(before string) []string {
	var out []string
	for _, m := range lineBindRe.FindAllStringSubmatch(before, -1) {
		out = append(out, m[1])
		if m[2] != "" && m[2] != "in" {
			out = append(out, m[2])
		}
	}
	for _, m := range lineDeclRe.FindAllStringSubmatch(before, -1) {
		out = append(out, m[1])
	}
	for _, m := range lineFnRe.FindAllStringSubmatch(before, -1) {
		for _, p := range strings.Split(m[2], ",") {
			if p = strings.TrimSpace(p); nameRe.MatchString(p) {
				out = append(out, p)
			}
		}
	}
	if strings.Contains(before, "with ") {
		for _, m := range lineWithRe.FindAllStringSubmatch(before, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func uniqSorted(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
