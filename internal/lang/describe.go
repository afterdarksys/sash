package lang

// sash --describe: a service file's block and the needs of each lifecycle
// entry, as one stable JSON document (docs/plans/2026-11-21-sash-init-
// contract.md; sahinit derives a unit from it). --grant-request turns the
// literal needs into an unsigned sash/2 body.
//
// Threats: describe reads the parse tree only, as --needs does. It runs
// nothing, takes no grant, calls no parent and opens no file. An entry's
// needs follow the functions it calls by literal name; a need it cannot
// see (a value, a use file) is a row with a note and the result is
// incomplete (exit 1), and a grant request never carries such a row. The
// document is advisory: the init and sash still check every verb.

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/afterdarksys/sash/internal/grant"
)

// DescribeVersion names the --describe document's shape. A change to the
// shape is a new version.
const DescribeVersion = "sash-describe/1"

// DescribeOptions selects the output: GrantRequest prints the unsigned
// sash/2 body instead of the description.
type DescribeOptions struct {
	GrantRequest bool
}

type describeDoc struct {
	V        string                    `json:"v"`
	Service  describeService           `json:"service"`
	Entries  []string                  `json:"entries"`
	Needs    map[string][]describeNeed `json:"needs"`
	Complete bool                      `json:"complete"`
}

type describeService struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Requires    []string         `json:"requires"`
	After       []string         `json:"after"`
	Wants       []string         `json:"wants"`
	User        string           `json:"user"`
	Group       string           `json:"group"`
	Run         []string         `json:"run"`
	Ready       *describeReady   `json:"ready"`
	Stop        *describeStop    `json:"stop"`
	Restart     *describeRestart `json:"restart"`
}

type describeReady struct {
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	TimeoutMS int64  `json:"timeout_ms"`
}

type describeStop struct {
	Signal    string `json:"signal"`
	TimeoutMS int64  `json:"timeout_ms"`
}

type describeRestart struct {
	Policy   string `json:"policy"`
	DelayMS  int64  `json:"delay_ms"`
	Burst    int    `json:"burst"`
	WithinMS int64  `json:"within_ms"`
}

type describeNeed struct {
	Key     string `json:"key"`
	Target  string `json:"target"`
	Mutates bool   `json:"mutates"`
	Lines   []int  `json:"lines"`
	Note    string `json:"note"`
}

func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Describe prints src's service block and per-entry needs as JSON. Exit 0
// complete, 1 incomplete, 2 a parse error or no service block.
func Describe(src string, opt DescribeOptions) Outcome {
	fail := func(msg string) Outcome { return Outcome{Status: exitType, Stderr: safeText(msg) + "\n"} }
	top, _, prog, err := needsParse(src, nil)
	if err != nil {
		return fail(err.Error())
	}
	svc := serviceOf(prog)
	if svc == nil {
		return fail("describe: the file has no service block")
	}
	doc := describeDoc{V: DescribeVersion, Service: describeService{
		Name: svc.Name, Description: svc.Description, Requires: list(svc.Requires), After: list(svc.After),
		Wants: list(svc.Wants), User: svc.User, Group: svc.Group, Run: list(svc.Run),
	}, Entries: []string{}, Needs: map[string][]describeNeed{}, Complete: true}
	if r := svc.Ready; r != nil {
		doc.Service.Ready = &describeReady{Kind: r.Kind, Target: r.Target, TimeoutMS: r.Timeout.Milliseconds()}
	}
	if s := svc.Stop; s != nil {
		doc.Service.Stop = &describeStop{Signal: s.Signal, TimeoutMS: s.Timeout.Milliseconds()}
	}
	if r := svc.Restart; r != nil {
		doc.Service.Restart = &describeRestart{Policy: r.Policy, DelayMS: r.Delay.Milliseconds(), Burst: r.Burst, WithinMS: r.Within.Milliseconds()}
	}
	fns := map[string]*Fn{}
	var checkFn *Fn
	var uses []*Use
	for _, s := range prog.Stmts {
		switch s := s.(type) {
		case *Fn:
			if s.Name == "check" {
				checkFn = s
			} else {
				fns[s.Name] = s
			}
		case *Use:
			uses = append(uses, s)
		}
	}
	var all []*needRow
	for _, entry := range []string{"start", "stop", "reload", "check", "run"} {
		n := &needsRun{nl: top.nl, fns: top.fns, moves: top.moves, rows: map[string]*needRow{}, flags: map[string]bool{}}
		switch {
		case entry == "run":
			if len(svc.Run) == 0 || fns["start"] != nil {
				continue
			}
			n.add("svc.spawn", svc.Run[0], "", prog.Pos[0], "")
		case entry == "check" && checkFn != nil:
			n.entryNeeds(checkFn, fns)
		case entry != "check" && fns[entry] != nil:
			n.entryNeeds(fns[entry], fns)
		default:
			continue
		}
		// The top level runs before every entry: a use reads its file, and
		// what it defines is not followed.
		for _, u := range uses {
			n.use(u, needBase{"": true})
		}
		doc.Entries = append(doc.Entries, entry)
		rows := []describeNeed{}
		for _, r := range n.sorted() {
			lines := []int{}
			for l := range r.lines {
				lines = append(lines, l)
			}
			sort.Ints(lines)
			rows = append(rows, describeNeed{Key: r.key, Target: r.target, Mutates: needsMutates[r.key], Lines: lines, Note: r.note})
			if r.note != "" && r.note != noteWasm {
				doc.Complete = false
			}
			all = append(all, r)
		}
		doc.Needs[entry] = rows
	}
	out := Outcome{}
	if !doc.Complete {
		out.Status = 1
	}
	if opt.GrantRequest {
		b, err := grantRequest(svc, all)
		if err != nil {
			return fail(err.Error())
		}
		out.Stdout = string(b)
		return out
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fail("describe: " + err.Error())
	}
	out.Stdout = string(b) + "\n"
	return out
}

// entryNeeds walks fn's body, and the body of every function it reaches by
// literal name, once each. The entry runs with no prefix; a helper under
// any.
func (n *needsRun) entryNeeds(fn *Fn, fns map[string]*Fn) {
	seen := map[*Fn]bool{fn: true}
	queue := []*Fn{fn}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		base := needBase{"?": true}
		if f == fn {
			base = needBase{"": true}
		}
		n.prog(&f.Body, base)
		needsEachStage(&f.Body, func(st Stage) {
			if len(st.Argv) == 0 {
				return
			}
			if g := fns[plain(st.Argv[0])]; g != nil && !seen[g] {
				seen[g] = true
				queue = append(queue, g)
			}
		})
	}
}

// grantRequest is the unsigned sash/2 body the literal needs ask for. A
// row with a note is left out: the request never grants what a run might
// not ask for, and the description says it is incomplete.
func grantRequest(svc *Service, rows []*needRow) ([]byte, error) {
	name := svc.Name
	sets := map[string]map[string]bool{"ent": {}, "read": {}, "write": {}, "exec": {}, "caps": {}, "mounts": {}, "sources": {}, "env": {}}
	add := func(set, v string) { sets[set][v] = true }
	private := false
	for _, r := range rows {
		if r.key != "-" {
			add("ent", r.key)
		}
		if r.note != "" && r.note != noteWasm {
			continue
		}
		switch r.key {
		case "file.read":
			add("read", r.target)
		case "file.write":
			add("write", r.target)
		case "svc.spawn":
			if c, ok := strings.CutPrefix(r.target, "cap "); ok {
				add("caps", c)
			} else if e, ok := strings.CutPrefix(r.target, "env "); ok {
				// A denied name is refused at the run whatever the grant
				// lists, so the request never asks for it.
				if grant.EnvName.MatchString(e) && !grant.EnvDenied(e) {
					add("env", e)
				}
			} else if strings.HasPrefix(r.target, "/") {
				add("exec", r.target)
			}
		case "fs.mount":
			if src, ok := strings.CutPrefix(r.target, "source "); ok {
				add("sources", src)
			} else {
				add("mounts", r.target)
			}
		case "svc.probe":
			kind, target, _ := strings.Cut(r.target, " ")
			switch kind {
			case "unix", "file":
				add("read", target)
			case "tcp":
				if probeTCP(target, true) != "" && probeTCP(target, false) == "" {
					private = true
				}
			}
		}
	}
	sorted := func(set string) []string {
		out := []string{}
		for k := range sets[set] {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	g := grant.Grant{
		Actor: "service:" + name, Entitlements: sorted("ent"), FileRead: sorted("read"), FileWrite: sorted("write"),
		AllowPrivate: private, Service: name, Exec: sorted("exec"), Caps: sorted("caps"), Mounts: sorted("mounts"),
		MountSources: sorted("sources"), User: svc.User, Group: svc.Group, EnvNames: sorted("env"),
	}
	b, err := grant.Request(g)
	if err != nil {
		return nil, errors.New("describe: grant request")
	}
	return b, nil
}
