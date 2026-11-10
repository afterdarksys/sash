package lang

import (
	"path"
	"strings"
	"text/tabwriter"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// Why explains the grant check for one line. It is session UI like Preview:
// it reads the grant already in memory, calls no parent, and runs nothing.
// History and braces expand as on the line. Globs are not listed; a glob
// word is checked as a read of the directory before its first "*". A $
// word is a value and is not parsed again. The fix column is display text
// only; the agent re-checks every verb. Do not log the line, the grant, or
// a path.
func Why(src string, g grant.Grant, opt Options) Outcome {
	out := Outcome{Prefix: opt.Prefix}
	text, prob := expandHistory(src, append([]string(nil), opt.History...))
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
		out.Status = prob.Status
		out.Stderr = prob.Msg + "\n"
		return out
	}
	prog, err := parse(text)
	if err != nil {
		out.Status = exitType
		out.Stderr = err.Error() + "\n"
		return out
	}
	var pipes []Pipeline
	collectPipes(prog, &pipes)
	// A copy: whyFns registers the line's functions, and why must not
	// change the session.
	m := newMachine(g, denyParent{}, opt.State.clone())
	if opt.Prefix != "" {
		m.stack = []string{path.Clean(opt.Prefix)}
	}
	whyFns(prog, m.fns)
	var rows []parent.Row
	var uses []*Use
	collectUses(prog, &uses)
	for _, u := range uses {
		cmd := "use " + text[u.Path.Pos:u.Path.End]
		if d := m.whyKey(cmd, "file.read"); d != nil {
			rows = append(rows, *d)
		} else if m.useDenied(u) != "" {
			rows = append(rows, whyRow(cmd, "file.read", "no", "outside_roots", "roots: "+strings.Join(m.g.FileRead, " ")))
		} else {
			rows = append(rows, whyRow(cmd, "file.read", "yes", "granted", "loads fn and declare -r only"))
		}
	}
	for _, pipe := range pipes {
		for _, st := range pipe.Stages {
			rows = append(rows, m.whyStage(text, st)...)
		}
		rows = append(rows, m.whyShape(text, pipe)...)
	}
	out.Stdout = whyTable(rows)
	return out
}

var whyCols = []string{"command", "key", "granted", "reason", "fix"}

// whyHint names the policy grant that would add a missing key. Display only.
var whyHint = map[string]string{
	"ps":              "every fleet grant has ps",
	"file.read":       "policy file_read root: --profile observe (/var/log) or --file-read <dir>",
	"file.write":      "policy file_write root: --file-write <dir>",
	"unit.status":     "policy --service-status <unit> (--profile observe)",
	"unit.restart":    "policy --service <unit> (--profile operate)",
	"package.query":   "policy --package <name>",
	"package.install": "policy --package <name>",
	"sysctl.get":      "policy --tweak-sysctl <key>",
	"sysctl.set":      "policy --tweak-sysctl <key>",
	"config.read":     "policy --config <dir>",
}

func whyRow(command, key, granted, reason, fix string) parent.Row {
	if key == "" {
		key = "-"
	}
	return parent.Row{"command": command, "key": key, "granted": granted, "reason": reason, "fix": fix}
}

// whyFns registers the functions the line defines, as Run would before a call.
func whyFns(prog *Program, fns map[string]*Fn) {
	if prog == nil {
		return
	}
	for _, s := range prog.Stmts {
		switch s := s.(type) {
		case *Fn:
			fns[s.Name] = s
			whyFns(&s.Body, fns)
		case *If:
			whyFns(&s.Then, fns)
			whyFns(s.Else, fns)
		case *For:
			whyFns(&s.Body, fns)
		case *While:
			whyFns(&s.Body, fns)
		case *Try:
			whyFns(&s.Body, fns)
			whyFns(s.Catch, fns)
		}
	}
}

// whyShape runs Run's own prepare on a pipeline of literal words, so a line
// Run would refuse for its shape (a "<" past the first stage, a type error)
// is not reported as granted. prepare calls no parent. A denial is already a
// row; a pipeline with a $ word is checked when it runs.
func (m *machine) whyShape(text string, pipe Pipeline) []parent.Row {
	start, end := -1, -1
	for _, st := range pipe.Stages {
		words := append([]Word(nil), st.Argv...)
		for _, r := range st.Redirs {
			words = append(words, r.Path)
		}
		for _, w := range words {
			if plain(w) == "" {
				return nil
			}
			if start < 0 || w.Pos < start {
				start = w.Pos
			}
			if w.End > end {
				end = w.End
			}
		}
	}
	if start < 0 {
		return nil
	}
	_, prob := m.prepare(pipe)
	if prob == nil || prob.status == exitDenied || prob.msg == "prefix_unset" {
		return nil
	}
	return []parent.Row{whyRow(text[start:end], "", "-", "usage", "the line does not run: "+prob.msg)}
}

func (m *machine) whyStage(text string, st Stage) []parent.Row {
	if len(st.Argv) == 0 {
		return nil
	}
	command := text[st.Argv[0].Pos:st.Argv[len(st.Argv)-1].End]
	argv, literal := plainArgv(st)
	if !literal {
		argv = make([]string, len(st.Argv))
		for i, w := range st.Argv {
			if argv[i] = plain(w); argv[i] == "" {
				argv[i] = "$"
			}
		}
	}
	var rows []parent.Row
	if argv[0] == "$" {
		rows = append(rows, whyRow(command, "", "-", "value", "a command name from a value does not run"))
	} else {
		rows = append(rows, m.whyCommand(command, argv, literal))
	}
	for _, r := range st.Redirs {
		if r.Kind != "<" && r.Kind != ">" {
			continue
		}
		rows = append(rows, m.whyRedir(text, r))
	}
	return rows
}

func (m *machine) whyCommand(command string, argv []string, literal bool) parent.Row {
	key := knownKey(argv)
	info, cprob := classify(argv, m.fns)
	if key == "" && cprob == nil {
		key = info.key
	}
	if cprob != nil && cprob.status == exitUnknown {
		return whyRow(command, "", "-", "unknown", "not a sash command; sash does not run host binaries")
	}
	if key == "" {
		if cprob != nil {
			return whyRow(command, "", "-", "usage", cprob.msg)
		}
		return whyRow(command, "", "-", "in_process", "")
	}
	if denied := m.whyKey(command, key); denied != nil {
		return *denied
	}
	if cprob != nil {
		return whyRow(command, key, "yes", "usage", cprob.msg)
	}
	if !literal {
		return whyRow(command, key, "yes", "value_path", "a path from a value is checked when the line runs")
	}
	es := expStage{argv: whyGlobDirs(argv), key: key}
	if prob := m.bindPaths(&es); prob != nil {
		return m.whyPathRow(command, key, prob, m.g.FileRead)
	}
	return whyRow(command, key, "yes", "granted", whyAgentNote(key))
}

func (m *machine) whyRedir(text string, r Redir) parent.Row {
	command := text[r.Path.Pos:r.Path.End]
	key := "file.read"
	roots := m.g.FileRead
	if r.Kind == ">" {
		key = "file.write"
		roots = m.g.FileWrite
	}
	command = r.Kind + " " + command
	if denied := m.whyKey(command, key); denied != nil {
		return *denied
	}
	p := plain(r.Path)
	if p == "" {
		return whyRow(command, key, "yes", "value_path", "a path from a value is checked when the line runs")
	}
	full, err := resolvePath(m.prefix(), whyGlobDir(p))
	if err != nil {
		return m.whyPathRow(command, key, &problem{exitType, "prefix_unset"}, roots)
	}
	ok := m.g.AllowsRead(full)
	if key == "file.write" {
		ok = m.g.AllowsWrite(full)
	}
	if !ok {
		return m.whyPathRow(command, key, &problem{exitDenied, "denied " + key}, roots)
	}
	return whyRow(command, key, "yes", "granted", "")
}

// whyKey returns the row for a key this session cannot use, or nil.
func (m *machine) whyKey(command, key string) *parent.Row {
	var row parent.Row
	switch {
	case m.g.V == "":
		row = whyRow(command, key, "no", "no_session", "started without --session, so there is no parent; log in through the agent")
	case key == "journal":
		row = whyRow(command, key, "no", "not_grantable", "the agent has no journal reader yet")
	case !m.g.Has(key):
		fix := whyHint[key]
		if m.g.Purpose == "emergency" {
			fix = "emergency grant: adssh signs it on this host"
		}
		row = whyRow(command, key, "no", "not_in_grant", fix)
	default:
		return nil
	}
	return &row
}

func (m *machine) whyPathRow(command, key string, prob *problem, roots []string) parent.Row {
	if prob.msg == "prefix_unset" {
		return whyRow(command, key, "no", "prefix_unset", "this grant has no file_read root, so a relative path has no base")
	}
	if prob.status != exitDenied {
		return whyRow(command, key, "yes", "usage", prob.msg)
	}
	fix := "no roots in this grant"
	if len(roots) > 0 {
		fix = "roots: " + strings.Join(roots, " ")
	}
	return whyRow(command, key, "no", "outside_roots", fix)
}

func whyAgentNote(key string) string {
	switch key {
	case "unit.status", "unit.restart", "package.query", "package.install", "sysctl.get", "sysctl.set", "config.read":
		return "the agent also checks this host's policy list"
	}
	return ""
}

func whyGlobDirs(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if i == 0 {
			out[i] = a
			continue
		}
		out[i] = whyGlobDir(a)
	}
	return out
}

// whyGlobDir is the directory a glob lists: the path before its first "*".
func whyGlobDir(a string) string {
	i := strings.IndexByte(a, '*')
	if i < 0 {
		return a
	}
	head := a[:i]
	j := strings.LastIndexByte(head, '/')
	switch {
	case j < 0:
		return "."
	case j == 0:
		return "/"
	default:
		return head[:j]
	}
}

func whyTable(rows []parent.Row) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	tw.Write([]byte(strings.Join(whyCols, "\t") + "\n"))
	for _, row := range rows {
		cells := make([]string, len(whyCols))
		for i, c := range whyCols {
			cells[i] = strings.ReplaceAll(row[c], "\t", " ")
		}
		tw.Write([]byte(strings.Join(cells, "\t") + "\n"))
	}
	tw.Flush()
	return b.String()
}
