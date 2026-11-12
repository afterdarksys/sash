package lang

// Needs is sash --needs: the grant manifest of a whole script
// (docs/plans/2026-11-12-sash-needs.md).
//
// Threats: needs reads the parse tree only. It runs nothing, calls no
// parent, and opens no file, so a hostile script cannot make it touch the
// host. Under-reporting is the dangerous failure: every need it cannot see
// (a value, a relative path without one literal base, journal, a use file)
// is a row with a note, and the result is incomplete (exit 1). It prints
// literal targets only; a value, and so a declare -s value, prints as $.
// Every printed cell is escaped, so a crafted literal cannot drive the
// terminal. The manifest is advisory: nothing reads it to grant, and the
// agent still checks every verb. It does not log the script or a target.
// It does not cover a need behind a use file; that row says so.

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"
)

// NeedsOptions selects the output. Policy prints policy flags instead of
// the table.
type NeedsOptions struct {
	Policy bool
}

const (
	noteDynamic      = "dynamic"
	noteRelative     = "relative"
	noteNotGrantable = "not_grantable"
	noteUse          = "use: not followed"
	// noteUnpasteable is a literal target no flag can carry: it starts
	// with "-" or has a control or format character.
	noteUnpasteable = "unpasteable"
	// noteCommandFromValue is a stage a run refuses: its command name is
	// a value. The script can run up to it, so it is incomplete, not a
	// usage error.
	noteCommandFromValue = msgCommandFromValue

	needsLineCap = 10
	// needsBaseCap bounds the prefixes tracked at one point; past it the
	// prefix is any.
	needsBaseCap = 16
)

// needsFlag is the policy flag for every verb key. "" is a key no flag
// grants: ps needs none, and journal cannot be granted. A key classify
// returns must be here; a test walks the builtin table.
var needsFlag = map[string]string{
	"ps":              "",
	"journal":         "",
	"file.read":       "--file-read",
	"file.write":      "--file-write",
	"unit.status":     "--service-status",
	"unit.restart":    "--service",
	"package.query":   "--package",
	"package.install": "--package",
	"sysctl.get":      "--tweak-sysctl",
	"sysctl.set":      "--tweak-sysctl",
	"config.read":     "--config",
}

var needsMutates = map[string]bool{
	"file.write": true, "unit.restart": true, "package.install": true, "sysctl.set": true,
}

// A path need lists a directory (cd, ls, pushd, with prefix, a glob) or
// reads or writes a file (cat, <, >, template, template_file, use). The
// policy root is the directory itself or the file's parent.
const (
	needDir = iota
	needFile
)

type needRow struct {
	key, target, note string
	lines             map[int]bool
}

type needsRun struct {
	nl    []int // offsets of "\n" in the text
	fns   map[string]*Fn
	moves bool // a function may move the prefix, or a use may define one
	rows  map[string]*needRow
	flags map[string]bool
}

// needBase is the set of prefixes a statement may run under: "" is unset
// and "?" is any. A transfer only adds, so a statement that did not run,
// a failed cd, or the join after a branch is a union and never drops a
// prefix the run could have.
type needBase map[string]bool

func (b needBase) plus(more ...string) needBase {
	out := needBase{}
	for k := range b {
		out[k] = true
	}
	for _, k := range more {
		out[k] = true
	}
	if len(out) > needsBaseCap {
		out["?"] = true
	}
	return out
}

func (b needBase) union(o needBase) needBase {
	out := b.plus()
	for k := range o {
		out[k] = true
	}
	return out
}

// resolve makes p absolute. A relative p resolves only when the base is
// one known directory; an unset base cannot read, so it adds nothing.
func (b needBase) resolve(p string) (string, bool) {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), true
	}
	if b["?"] {
		return "", false
	}
	dir := ""
	n := 0
	for k := range b {
		if k != "" {
			dir = k
			n++
		}
	}
	if n != 1 {
		return "", false
	}
	return path.Clean(path.Join(dir, p)), true
}

// under is the base after a move to p succeeds: "" is a value, so any.
// A relative move from an unset prefix fails, so it adds nothing.
func (b needBase) under(p string) needBase {
	if p == "" {
		return needBase{"?": true}
	}
	if strings.HasPrefix(p, "/") {
		return needBase{path.Clean(p): true}
	}
	out := needBase{}
	for k := range b {
		switch k {
		case "":
		case "?":
			out["?"] = true
		default:
			out[path.Clean(path.Join(k, p))] = true
		}
	}
	return out
}

// Needs lists what src would ask for. It expands history and braces as a
// run does, finds the globs a run would list, and walks every statement:
// both arms, loop bodies, try and catch, with, retry, timeout, and every
// fn body, called or not.
func Needs(src string, opt NeedsOptions) Outcome {
	fail := func(msg string) Outcome {
		return Outcome{Status: exitType, Stderr: msg + "\n"}
	}
	text, prob := expandHistory(src, nil)
	if prob == nil && len(text) > expandMax {
		prob = braceProb()
	}
	if prob == nil {
		text, prob = expandBraces(text)
	}
	if prob == nil && len(text) > expandMax {
		prob = braceProb()
	}
	if prob != nil {
		return fail(prob.Msg)
	}
	n := &needsRun{fns: map[string]*Fn{}, rows: map[string]*needRow{}, flags: map[string]bool{}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			n.nl = append(n.nl, i)
		}
	}
	// A run lists globs before it parses, against the starting prefix,
	// which a script does not have: a relative glob is never resolved.
	if _, prob := mapWordsAt(text, func(at int, word string) (string, *ExpandProblem) {
		return word, n.glob(at, word)
	}); prob != nil {
		return fail(prob.Msg)
	}
	prog, err := parse(text)
	if err != nil {
		return fail(err.Error())
	}
	whyFns(prog, n.fns)
	n.moves = needsFnMoves(prog)
	n.prog(prog, needBase{"": true})
	rows := n.sorted()
	out := Outcome{}
	for _, r := range rows {
		if r.note != "" {
			out.Status = 1
		}
	}
	if opt.Policy {
		out.Stdout = n.policy(rows)
	} else {
		out.Stdout = needsTable(rows)
	}
	return out
}

func (n *needsRun) glob(at int, word string) *ExpandProblem {
	if !strings.Contains(word, "*") {
		return nil
	}
	if strings.Contains(word, "$") {
		return &ExpandProblem{Status: exitType, Msg: "type glob $"}
	}
	dir, pat, _, _, prob := splitStar(word)
	if prob != nil {
		return prob
	}
	if _, err := path.Match(pat, "x"); err != nil {
		return globProb()
	}
	if dir == "" {
		dir = "."
	}
	n.literalPath("file.read", dir, needDir, needBase{"": true}, at)
	return nil
}

func (n *needsRun) line(at int) int {
	return sort.SearchInts(n.nl, at) + 1
}

func (n *needsRun) add(key, target, note string, at int, flag string) {
	// A literal need whose flag cannot be pasted is not granted by the
	// policy output, so the manifest is incomplete.
	if note == "" && flag == "" && needsFlag[key] != "" {
		note = noteUnpasteable
	}
	id := key + "\x00" + target + "\x00" + note
	r := n.rows[id]
	if r == nil {
		r = &needRow{key: key, target: target, note: note, lines: map[int]bool{}}
		n.rows[id] = r
	}
	r.lines[n.line(at)] = true
	if flag != "" {
		n.flags[flag] = true
	}
}

// value adds a need whose target is one word: a unit, a package, a key.
// A subcommand from a value (dyn) lists every key it could be, but not
// which one the run uses, so each is dynamic and has no flag.
func (n *needsRun) value(key string, w Word, dyn bool) {
	s := plain(w)
	switch {
	case s == "":
		n.add(key, "$", noteDynamic, w.Pos, "")
	case dyn:
		n.add(key, s, noteDynamic, w.Pos, "")
	default:
		n.add(key, s, "", w.Pos, needsPolicyFlag(key, s))
	}
}

func (n *needsRun) path(key string, w Word, kind int, b needBase) {
	s := plain(w)
	if s == "" {
		n.add(key, "$", noteDynamic, w.Pos, "")
		return
	}
	n.literalPath(key, s, kind, b, w.Pos)
}

// literalPath reports a glob as the directory before its first "*", as
// why does, and a relative path as written when its base is not one
// known directory.
func (n *needsRun) literalPath(key, s string, kind int, b needBase, at int) {
	if strings.Contains(s, "*") {
		s = whyGlobDir(s)
		kind = needDir
	}
	full, ok := b.resolve(s)
	if !ok {
		n.add(key, s, noteRelative, at, "")
		return
	}
	root := full
	if kind == needFile {
		root = path.Dir(full)
	}
	n.add(key, full, "", at, needsPolicyFlag(key, root))
}

func (n *needsRun) prog(p *Program, b needBase) needBase {
	if p == nil {
		return b
	}
	for _, s := range p.Stmts {
		b = n.stmt(s, b)
	}
	return b
}

func (n *needsRun) stmt(s Stmt, b needBase) needBase {
	switch s := s.(type) {
	case *Chain:
		// The pipes run in order or are skipped; either way the base
		// after the last one holds every prefix.
		for _, p := range s.Pipes {
			b = n.pipe(p, b)
		}
		return b
	case *Let:
		if s.Pipe != nil {
			return n.pipe(*s.Pipe, b)
		}
		return b
	case *Declare:
		if s.Value != nil && s.Value.Pipe != nil {
			return n.pipe(*s.Value.Pipe, b)
		}
		return b
	case *If:
		if s.CondX == nil {
			b = n.pipe(s.Cond, b)
		}
		return n.prog(&s.Then, b).union(n.prog(s.Else, b))
	case *For:
		if s.SrcX == nil && s.SrcRef == "" {
			b = n.pipe(s.Src, b)
		}
		return n.loop(&s.Body, nil, b)
	case *While:
		var cond *Pipeline
		if s.CondX == nil {
			cond = &s.Cond
		}
		return n.loop(&s.Body, cond, b)
	case *Retry:
		return n.loop(&s.Body, nil, b)
	case *Timeout:
		return n.prog(&s.Body, b)
	case *Try:
		body := n.prog(&s.Body, b)
		in := b
		if n.progMoves(&s.Body) {
			in = b.plus("?")
		}
		return body.union(n.prog(s.Catch, in))
	case *With:
		inner := b
		if s.Prefix != nil {
			n.path("file.read", *s.Prefix, needDir, b)
			// The body runs only when the move succeeds.
			inner = b.under(plain(*s.Prefix))
		}
		for _, l := range s.Lets {
			if l.Pipe != nil {
				inner = n.pipe(*l.Pipe, inner)
			}
		}
		n.prog(&s.Body, inner)
		// with restores the prefix on every way out.
		return b
	case *Use:
		n.use(s, b)
		return b
	case *Fn:
		// A body runs under its caller's prefix, which may be any.
		n.prog(&s.Body, needBase{"?": true})
		return b
	case *Jump:
		return b
	default:
		// A statement needs does not know: say so rather than skip it.
		n.add("-", "-", noteNotGrantable, 0, "")
		return b.plus("?")
	}
}

// loop runs a body zero or more times. When it, or a condition checked
// each time, may move the prefix, the body and what follows run under
// any prefix.
func (n *needsRun) loop(body *Program, cond *Pipeline, b needBase) needBase {
	in := b
	if n.progMoves(body) || (cond != nil && n.pipeMoves(*cond)) {
		in = b.plus("?")
	}
	if cond != nil {
		in = n.pipe(*cond, in)
	}
	return in.union(n.prog(body, in))
}

func (n *needsRun) use(u *Use, b needBase) {
	s := plain(u.Path)
	if s == "" {
		n.add("file.read", "$", noteUse, u.Path.Pos, "")
		return
	}
	full, ok := b.resolve(s)
	if !ok {
		n.add("file.read", s, noteUse, u.Path.Pos, "")
		return
	}
	n.add("file.read", full, noteUse, u.Path.Pos, needsPolicyFlag("file.read", path.Dir(full)))
}

func (n *needsRun) pipe(p Pipeline, b needBase) needBase {
	for _, st := range p.Stages {
		b = n.stage(st, b)
	}
	return b
}

func (n *needsRun) stage(st Stage, b needBase) needBase {
	for _, r := range st.Redirs {
		switch r.Kind {
		case "<":
			n.path("file.read", r.Path, needFile, b)
		case ">":
			n.path("file.write", r.Path, needFile, b)
		}
	}
	if len(st.Argv) == 0 {
		return b
	}
	name := plain(st.Argv[0])
	if name == "" {
		// A command name from a value does not run: a run refuses the
		// stage before any call.
		n.add("-", "$", noteCommandFromValue, st.Argv[0].Pos, "")
		return b
	}
	// A literal stage classify refuses never runs. A stage with a value
	// is judged by its shape: a value is one word, never split.
	if argv, ok := plainArgv(st); ok {
		if _, prob := classify(argv, n.fns); prob != nil && prob.status != exitUnknown {
			return b
		}
	}
	args := st.Argv[1:]
	at := st.Argv[0].Pos
	sub := ""
	dynSub := false
	if len(args) > 0 {
		sub = plain(args[0])
		dynSub = sub == ""
	}
	switch name {
	case "ps":
		n.add("ps", "-", "", at, "")
	case "journal":
		n.add("journal", "-", noteNotGrantable, at, "")
	case "cat":
		for _, w := range args {
			n.path("file.read", w, needFile, b)
		}
	case "ls":
		if len(args) == 0 {
			n.literalPath("file.read", ".", needDir, b, at)
		} else if len(args) == 1 {
			n.path("file.read", args[0], needDir, b)
		}
	case "cd", "pushd":
		if len(args) == 1 {
			n.path("file.read", args[0], needDir, b)
			// The move can fail, so the old prefixes stay.
			return b.union(b.under(plain(args[0])))
		}
	case "popd":
		// The prefix below the top is not tracked.
		return b.plus("?")
	case "template":
		if len(args) == 1 {
			n.path("file.read", args[0], needFile, b)
		}
	case "template_file":
		if len(args) == 3 || len(args) == 4 {
			n.path("file.read", args[0], needFile, b)
			n.path("file.read", args[1], needFile, b)
			key := "file.write"
			if len(args) == 4 {
				key = "file.read"
			}
			n.path(key, args[2], needFile, b)
		}
	case "unit":
		// The agent serves unit status only for one named unit.
		if len(args) == 0 {
			n.add("unit.status", "-", noteNotGrantable, at, "")
			break
		}
		if sub == "status" || dynSub {
			if len(args) == 2 {
				n.value("unit.status", args[1], dynSub)
			} else {
				n.add("unit.status", "-", noteNotGrantable, at, "")
			}
		}
		if (sub == "restart" || dynSub) && len(args) == 2 {
			n.value("unit.restart", args[1], dynSub)
		}
	case "package":
		if sub == "query" || dynSub {
			if len(args) == 2 {
				n.value("package.query", args[1], dynSub)
			} else {
				n.add("package.query", "-", noteNotGrantable, at, "")
			}
		}
		if (sub == "install" || dynSub) && len(args) == 2 {
			n.value("package.install", args[1], dynSub)
		}
	case "sysctl":
		if (sub == "get" || dynSub) && len(args) == 2 {
			n.value("sysctl.get", args[1], dynSub)
		}
		if (sub == "set" || dynSub) && len(args) == 3 {
			n.value("sysctl.set", args[1], dynSub)
		}
	case "config":
		if (sub == "read" || dynSub) && len(args) == 2 {
			n.config(args[1], b)
		}
	default:
		// A function's body is counted where it is defined. A call may
		// move the prefix when a function here moves it or a use may
		// define one.
		if !reserved(name) && n.moves {
			return b.plus("?")
		}
	}
	return b
}

// config is exact: the agent serves config read only when argv is the
// clean absolute path, so a relative path is never resolved. A run also
// checks the path against the file_read roots, so it is a file.read need
// too.
func (n *needsRun) config(w Word, b needBase) {
	n.path("file.read", w, needFile, b)
	s := plain(w)
	switch {
	case s == "":
		n.add("config.read", "$", noteDynamic, w.Pos, "")
	case !strings.HasPrefix(s, "/"):
		n.add("config.read", s, noteRelative, w.Pos, "")
	case path.Clean(s) != s:
		n.add("config.read", s, noteNotGrantable, w.Pos, "")
	default:
		n.add("config.read", s, "", w.Pos, needsPolicyFlag("config.read", s))
	}
}

func needsMover(st Stage) bool {
	if len(st.Argv) == 0 {
		return false
	}
	switch plain(st.Argv[0]) {
	case "cd", "pushd", "popd":
		return true
	}
	return false
}

// needsFnMoves is true when a use may define a function, or a function
// body moves the prefix.
func needsFnMoves(prog *Program) bool {
	var uses []*Use
	collectUses(prog, &uses)
	if len(uses) > 0 {
		return true
	}
	moves := false
	needsEachStmt(prog, func(s Stmt) {
		if f, ok := s.(*Fn); ok {
			needsEachStage(&f.Body, func(st Stage) {
				if needsMover(st) {
					moves = true
				}
			})
		}
	})
	return moves
}

func (n *needsRun) pipeMoves(p Pipeline) bool {
	for _, st := range p.Stages {
		if needsMover(st) {
			return true
		}
		if n.moves && len(st.Argv) > 0 {
			if name := plain(st.Argv[0]); name != "" && !reserved(name) {
				return true
			}
		}
	}
	return false
}

func (n *needsRun) progMoves(p *Program) bool {
	moves := false
	needsEachStage(p, func(st Stage) {
		if n.pipeMoves(Pipeline{Stages: []Stage{st}}) {
			moves = true
		}
	})
	return moves
}

// needsEachStmt calls fn on every statement at any depth.
func needsEachStmt(p *Program, fn func(Stmt)) {
	if p == nil {
		return
	}
	for _, s := range p.Stmts {
		fn(s)
		switch s := s.(type) {
		case *If:
			needsEachStmt(&s.Then, fn)
			needsEachStmt(s.Else, fn)
		case *For:
			needsEachStmt(&s.Body, fn)
		case *While:
			needsEachStmt(&s.Body, fn)
		case *Try:
			needsEachStmt(&s.Body, fn)
			needsEachStmt(s.Catch, fn)
		case *With:
			needsEachStmt(&s.Body, fn)
		case *Retry:
			needsEachStmt(&s.Body, fn)
		case *Timeout:
			needsEachStmt(&s.Body, fn)
		case *Fn:
			needsEachStmt(&s.Body, fn)
		}
	}
}

// needsEachStage calls fn on every stage at any depth, conditions,
// sources, and let, declare, and with values included.
func needsEachStage(p *Program, fn func(Stage)) {
	pipe := func(pl Pipeline) {
		for _, st := range pl.Stages {
			fn(st)
		}
	}
	needsEachStmt(p, func(s Stmt) {
		switch s := s.(type) {
		case *Chain:
			for _, pl := range s.Pipes {
				pipe(pl)
			}
		case *Let:
			if s.Pipe != nil {
				pipe(*s.Pipe)
			}
		case *Declare:
			if s.Value != nil && s.Value.Pipe != nil {
				pipe(*s.Value.Pipe)
			}
		case *If:
			if s.CondX == nil {
				pipe(s.Cond)
			}
		case *For:
			if s.SrcX == nil && s.SrcRef == "" {
				pipe(s.Src)
			}
		case *While:
			if s.CondX == nil {
				pipe(s.Cond)
			}
		case *With:
			for _, l := range s.Lets {
				if l.Pipe != nil {
					pipe(*l.Pipe)
				}
			}
		}
	})
}

func (n *needsRun) sorted() []*needRow {
	rows := make([]*needRow, 0, len(n.rows))
	for _, r := range n.rows {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].key != rows[j].key {
			return rows[i].key < rows[j].key
		}
		if rows[i].target != rows[j].target {
			return rows[i].target < rows[j].target
		}
		return rows[i].note < rows[j].note
	})
	return rows
}

func needsLines(r *needRow) string {
	ls := make([]int, 0, len(r.lines))
	for l := range r.lines {
		ls = append(ls, l)
	}
	sort.Ints(ls)
	var parts []string
	for i, l := range ls {
		if i == needsLineCap {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, strconv.Itoa(l))
	}
	return strings.Join(parts, ",")
}

var needsCols = []string{"key", "target", "mutates", "lines", "note"}

func needsTable(rows []*needRow) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	tw.Write([]byte(strings.Join(needsCols, "\t") + "\n"))
	for _, r := range rows {
		mut := "no"
		if needsMutates[r.key] {
			mut = "yes"
		}
		cells := []string{r.key, r.target, mut, needsLines(r), r.note}
		for i := range cells {
			cells[i] = needsCell(cells[i])
		}
		tw.Write([]byte(strings.Join(cells, "\t") + "\n"))
	}
	tw.Flush()
	return b.String()
}

// policy prints the flags for the literal needs, sorted and once each,
// then a comment for each need no flag expresses.
func (n *needsRun) policy(rows []*needRow) string {
	var b strings.Builder
	flags := make([]string, 0, len(n.flags))
	for f := range n.flags {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	for _, f := range flags {
		b.WriteString(f + "\n")
	}
	for _, r := range rows {
		why := ""
		switch {
		case r.note == noteDynamic:
			why = "the target or the subcommand comes from a value"
		case r.note == noteRelative:
			why = "a relative path with no single literal base"
		case r.note == noteNotGrantable:
			why = "no policy flag grants this"
		case r.note == noteCommandFromValue:
			why = "a command name from a value does not run; the run stops here"
		case r.note == noteUse:
			why = "use reads this file; the needs of what it defines are not listed"
		case r.note == noteUnpasteable:
			why = "the " + needsKind(r.key) + " cannot be a flag value (it starts with - or has a control or format character); grant it by hand"
		default:
			continue
		}
		b.WriteString(needsCell("# "+r.key+" "+r.target+": "+why+" (lines "+needsLines(r)+")") + "\n")
	}
	return b.String()
}

// needsKind names what a key's flag value is.
func needsKind(key string) string {
	switch {
	case strings.HasPrefix(key, "file."):
		return "path"
	case strings.HasPrefix(key, "unit."):
		return "unit"
	case strings.HasPrefix(key, "package."):
		return "package"
	case strings.HasPrefix(key, "sysctl."):
		return "sysctl key"
	case key == "config.read":
		return "config dir"
	}
	return "target"
}

// needsPolicyFlag is the flag for key on v, or "" when v cannot be
// pasted: it starts with "-", so a CLI would read it as a flag, or it has
// a character needsCell escapes. A value a shell would split or expand is
// single-quoted.
func needsPolicyFlag(key, v string) string {
	flag := needsFlag[key]
	if flag == "" || strings.HasPrefix(v, "-") || needsCell(v) != v {
		return ""
	}
	return flag + " " + needsQuote(v)
}

func needsQuote(v string) string {
	safe := v != ""
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_./:@%+=,-", c) >= 0) {
			safe = false
			break
		}
	}
	if safe {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// needsCell makes s safe to print: a tab becomes a space, and a control
// character (below 0x20, 0x7f, C1 0x80-0x9f), a format character (bidi
// overrides, zero-width), U+2028 and U+2029, anything else not printable,
// a backslash, and a byte that is not UTF-8 are escaped.
func needsCell(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == '\\':
			b.WriteString(`\\`)
		case unicode.Is(unicode.Cf, r) || !unicode.IsPrint(r):
			if r > 0xffff {
				fmt.Fprintf(&b, `\U%08x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}
