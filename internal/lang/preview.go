package lang

import (
	"fmt"
	"path"
	"strings"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// Preview is a display of the grant check. It does not authorize a verb.
// A denied line is Block so the editor does not call Run. lang.Run repeats
// the check. Do not log the line, the grant, or a path. A patched sash can
// still ask the agent, and the agent re-checks.

// Face is how one span is drawn. FaceDenied is an authorization mark.
type Face int

const (
	FacePlain Face = iota
	FaceFilter
	FaceToolkit
	FaceUnknown
	FaceDanger
	FaceDenied
	FaceTypePipe
)

// Span covers source bytes [Start, End). Later spans win on overlap.
type Span struct {
	Start int
	End   int
	Face  Face
	Key   string
}

// LinePreview is the painted line and whether Enter may run it.
// The function Preview shares this result. Go cannot give both the same name.
type LinePreview struct {
	Spans  []Span
	Smile  string
	Denied bool
	Block  bool
}

// denyParent fails closed if a display path ever calls the toolkit.
type denyParent struct{}

func (denyParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{}, fmt.Errorf("preview")
}

// Preview paints src with the same parse and preflight as Run.
// A denied line is Block. Usage and type errors are not.
func Preview(src string, g grant.Grant, opt Options) LinePreview {
	var out LinePreview
	if _, err := lex(src); err != nil {
		out.Smile = err.Error()
		out.Block = true
		return out
	}
	prog, err := parse(src)
	if err != nil {
		out.Smile = err.Error()
		out.Block = true
		return out
	}
	toks, err := lex(src)
	if err != nil {
		out.Smile = err.Error()
		out.Block = true
		return out
	}
	var pipes []Pipeline
	collectPipes(prog, &pipes)
	for _, pipe := range pipes {
		paintPipe(toks, pipe, g, &out.Spans)
	}
	m := &machine{
		g:    g,
		p:    denyParent{},
		vars: map[string]value{},
		fns:  map[string]*Fn{},
	}
	if opt.Prefix != "" {
		m.stack = []string{path.Clean(opt.Prefix)}
	}
	denial := ""
	typePipe := false
	other := ""
	for _, pipe := range pipes {
		_, prob := m.prepare(pipe)
		if prob == nil {
			continue
		}
		if prob.status == exitDenied {
			if denial == "" {
				denial = prob.msg
			}
			key := strings.TrimPrefix(prob.msg, "denied ")
			for i := range out.Spans {
				if out.Spans[i].Key == key {
					out.Spans[i].Face = FaceDenied
				}
			}
			continue
		}
		if prob.msg == "type |>" {
			typePipe = true
			continue
		}
		if other == "" {
			other = prob.msg
		}
	}
	known := ""
	for _, sp := range out.Spans {
		if sp.Face == FaceDenied && sp.Key != "" {
			known = "denied " + sp.Key
			break
		}
	}
	if denial != "" || known != "" {
		out.Denied = true
		out.Block = true
		if denial != "" {
			out.Smile = denial
		} else {
			out.Smile = known
		}
		for i := range out.Spans {
			if out.Spans[i].Face == FaceTypePipe {
				out.Spans[i].Face = FaceDenied
			}
		}
		return out
	}
	if typePipe {
		out.Smile = "type |>"
		return out
	}
	out.Smile = other
	return out
}

func collectPipes(prog *Program, out *[]Pipeline) {
	if prog == nil {
		return
	}
	for _, s := range prog.Stmts {
		switch s := s.(type) {
		case *Chain:
			*out = append(*out, s.Pipes...)
		case *Let:
			if s.Pipe != nil {
				*out = append(*out, *s.Pipe)
			}
		case *If:
			*out = append(*out, s.Cond)
			collectPipes(&s.Then, out)
			collectPipes(s.Else, out)
		case *For:
			*out = append(*out, s.Src)
			collectPipes(&s.Body, out)
		case *Fn:
			collectPipes(&s.Body, out)
		}
	}
}

func paintPipe(toks []token, pipe Pipeline, g grant.Grant, spans *[]Span) {
	for _, st := range pipe.Stages {
		paintStage(st, g, spans)
		paintRedir(toks, st, g, spans)
	}
	paintTypePipe(toks, pipe, spans)
}

func paintStage(st Stage, g grant.Grant, spans *[]Span) {
	if len(st.Argv) == 0 {
		return
	}
	start := st.Argv[0].Pos
	end := st.Argv[len(st.Argv)-1].End
	argv, ok := plainArgv(st)
	if !ok {
		*spans = append(*spans, Span{Start: start, End: end, Face: FaceFilter})
		return
	}
	face, key := stageFace(argv, g)
	*spans = append(*spans, Span{Start: start, End: end, Face: face, Key: key})
}

func paintRedir(toks []token, st Stage, g grant.Grant, spans *[]Span) {
	for _, r := range st.Redirs {
		k := kGT
		key := "file.write"
		if r.Kind == "<" {
			k = kLT
			key = "file.read"
		}
		var op token
		found := false
		for _, t := range toks {
			if t.kind == k && t.end <= r.Path.Pos {
				op = t
				found = true
			}
		}
		if !found {
			continue
		}
		face := FaceDenied
		if g.Has(key) {
			if key == "file.write" {
				face = FaceDanger
			} else {
				face = FaceToolkit
			}
		}
		*spans = append(*spans, Span{Start: op.pos, End: r.Path.End, Face: face, Key: key})
	}
}

func paintTypePipe(toks []token, pipe Pipeline, spans *[]Span) {
	if len(pipe.Stages) < 2 {
		return
	}
	exp := make([]expStage, len(pipe.Stages))
	for i, st := range pipe.Stages {
		argv, ok := plainArgv(st)
		if !ok {
			return
		}
		info, prob := classify(argv, map[string]*Fn{})
		if prob != nil {
			return
		}
		op := ""
		if i > 0 {
			op = pipe.Ops[i-1]
		}
		exp[i] = expStage{argv: argv, key: info.key, op: op, call: info.call, mover: info.mover}
	}
	prob := typecheck(exp)
	if prob == nil || prob.msg != "type |>" {
		return
	}
	for i := 0; i < len(pipe.Stages)-1; i++ {
		if pipe.Ops[i] != "|>" {
			continue
		}
		if len(pipe.Stages[i].Argv) == 0 || len(pipe.Stages[i+1].Argv) == 0 {
			continue
		}
		from := pipe.Stages[i].Argv[len(pipe.Stages[i].Argv)-1].End
		to := pipe.Stages[i+1].Argv[0].Pos
		for _, t := range toks {
			if t.kind == kRow && t.pos >= from && t.pos < to {
				*spans = append(*spans, Span{Start: t.pos, End: t.end, Face: FaceTypePipe})
			}
		}
	}
}

func plainArgv(st Stage) ([]string, bool) {
	if len(st.Argv) == 0 {
		return nil, false
	}
	out := make([]string, len(st.Argv))
	for i, w := range st.Argv {
		s := plain(w)
		if s == "" {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

func stageFace(argv []string, g grant.Grant) (Face, string) {
	key := knownKey(argv)
	info, prob := classify(argv, map[string]*Fn{})
	if key == "" && prob == nil {
		key = info.key
	}
	if key != "" && !g.Has(key) {
		return FaceDenied, key
	}
	if dangerKey(key) {
		return FaceDanger, key
	}
	if key != "" {
		return FaceToolkit, key
	}
	if prob != nil && prob.status == exitUnknown {
		return FaceUnknown, ""
	}
	return FaceFilter, ""
}

// knownKey is paint-only. A missing key fails closed in Preview.
// Execution still uses classify and prepare.
func knownKey(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	switch argv[0] {
	case "ps":
		return "ps"
	case "journal":
		return "journal"
	case "ls", "cd", "pushd":
		return "file.read"
	case "cat":
		if len(argv) >= 2 {
			return "file.read"
		}
		return ""
	case "unit":
		if len(argv) < 2 || argv[1] == "status" {
			return "unit.status"
		}
		if argv[1] == "restart" {
			return "unit.restart"
		}
		return ""
	case "package":
		if len(argv) < 2 {
			return ""
		}
		switch argv[1] {
		case "query":
			return "package.query"
		case "install":
			return "package.install"
		default:
			return ""
		}
	case "sysctl":
		if len(argv) < 2 {
			return ""
		}
		switch argv[1] {
		case "get":
			return "sysctl.get"
		case "set":
			return "sysctl.set"
		default:
			return ""
		}
	case "config":
		if len(argv) >= 2 && argv[1] == "read" {
			return "config.read"
		}
		return ""
	default:
		return ""
	}
}

func dangerKey(key string) bool {
	return key == "unit.restart" || key == "package.install" || key == "sysctl.set"
}
