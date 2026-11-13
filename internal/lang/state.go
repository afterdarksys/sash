package lang

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// State is what one session keeps between lines: names, functions, and set
// options. The prompt passes the same State to every line; a script gets a
// fresh one. No option touches the grant, the prefix roots, or the agent
// re-check.
type State struct {
	vars     map[string]value
	fns      map[string]*Fn
	readonly map[string]bool
	types    map[string]string // number, list, map

	errexit bool
	nounset bool
	xtrace  bool
	noglob  bool
	loopMax int
	verbMax int
	// plan stops mutating verbs at the parent door; planLocked (sash
	// --plan) keeps a script from turning it off.
	plan       bool
	planLocked bool
	// confirm asks y/N before each mutating verb (interactive only).
	confirm bool
	// checks are the check rows of this session, for checks.
	checks []parent.Row
	// gen changes whenever the State may have been written, so a display
	// memo keyed by it is never stale. Every write (assign, fn
	// definition, set, declare, check) happens inside Run, which bumps it
	// once when it returns, or in a setter below, which bumps it too.
	gen uint64
	// origins is the provenance table (prov.go), at most provPerState;
	// fnSrc places each fn body's lines.
	origins []origin
	fnSrc   map[*Fn]fnSource
	// secretOrigin, secretHash, and secretText only grow: the origins,
	// response hashes, and texts of every secret bound (prov.go).
	secretOrigin map[uint32]bool
	secretHash   map[[32]byte]bool
	secretText   map[string]bool
	// secrets memoizes SecretCount for one generation.
	secrets    int
	secretsGen uint64
	secretsOK  bool
}

// Generation is the write counter: equal values mean no write between.
func (s *State) Generation() uint64 {
	if s == nil {
		return 0
	}
	return s.gen
}

// LockPlan turns plan mode on for good: sash --plan.
func (s *State) LockPlan() {
	s.plan, s.planLocked = true, true
	s.gen++
}

// NewState returns the defaults: errexit, nounset, xtrace, and noglob off,
// pipefail on, loopmax 100000, verbmax 10000.
func NewState() *State {
	return &State{
		vars:     map[string]value{},
		fns:      map[string]*Fn{},
		readonly: map[string]bool{},
		types:    map[string]string{},
		loopMax:  loopMaxDefault,
		verbMax:  verbMaxDefault,
	}
}

// clone is a shallow copy for display paths that must not change the
// session. nil clones to a fresh State.
func (s *State) clone() *State {
	c := NewState()
	if s == nil {
		return c
	}
	*c = *s
	c.vars, c.fns = map[string]value{}, map[string]*Fn{}
	c.readonly, c.types = map[string]bool{}, map[string]string{}
	for k, v := range s.vars {
		c.vars[k] = v
	}
	for k, v := range s.fns {
		c.fns[k] = v
	}
	for k, v := range s.readonly {
		c.readonly[k] = v
	}
	for k, v := range s.types {
		c.types[k] = v
	}
	return c
}

// SetXtrace is the editor's trace on and trace off.
func (s *State) SetXtrace(on bool) {
	s.xtrace = on
	s.gen++
}

// msgConfirmLocked refuses turning confirm off from the language: once on
// it is a latch, so no pasted line, fn, use file, or with block can drop
// it before a verb. Only the editor's whole-line confirm off clears it.
const msgConfirmLocked = "confirm is locked"

// ConfirmOff is the editor's confirm off: the one way to clear the latch.
// It is a whole line the editor handles; it never reaches Run.
func (s *State) ConfirmOff() {
	s.confirm = false
	s.gen++
}

// Confirm reports whether set -o confirm is on.
func (s *State) Confirm() bool { return s != nil && s.confirm }

// Plan reports plan mode, for the right prompt.
func (s *State) Plan() bool { return s != nil && s.plan }

// SecretCount is how many bound names hold a secret (declare -s, or a
// value computed from one), for the right prompt. It returns a count
// only: never a name or a value.
func (s *State) SecretCount() int {
	if s == nil {
		return 0
	}
	if s.secretsOK && s.secretsGen == s.gen {
		return s.secrets
	}
	// Only a data value can hold a secretVal: rows, bytes, and strings
	// never do, so nothing is converted.
	n := 0
	for _, v := range s.vars {
		if v.kind == valData && isSecret(v.d) {
			n++
		}
	}
	s.secrets, s.secretsGen, s.secretsOK = n, s.gen, true
	return n
}

// Names are the bound names, sorted, for Tab after $. Values are never
// returned.
func (s *State) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.vars))
	for n := range s.vars {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FnNames are the defined functions, sorted, for Tab.
func (s *State) FnNames() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.fns))
	for n := range s.fns {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Xtrace reports set -x.
func (s *State) Xtrace() bool { return s.xtrace }

type setOption struct {
	name  string
	short byte
	desc  string
}

var setOptions = []setOption{
	{"errexit", 'e', "a failed stage ends the script"},
	{"nounset", 'u', "an unset name or missing key is an error"},
	{"xtrace", 'x', "print each stage key before it runs (trace)"},
	{"noglob", 'f', "* stays a literal word"},
	{"plan", 0, "mutating verbs print would and do not run"},
	{"confirm", 0, "ask y/N before each mutating verb (terminal only)"},
	{"pipefail", 0, "a pipeline fails when a stage fails (always on)"},
	{"loopmax", 0, "iterations one loop may run"},
	{"verbmax", 0, "agent verbs one line may call"},
}

func (s *State) flag(name string) *bool {
	switch name {
	case "errexit":
		return &s.errexit
	case "nounset":
		return &s.nounset
	case "xtrace":
		return &s.xtrace
	case "noglob":
		return &s.noglob
	case "plan":
		return &s.plan
	case "confirm":
		return &s.confirm
	}
	return nil
}

func (s *State) optionRows() value {
	cols := []string{"name", "value", "desc"}
	var rows []parent.Row
	for _, o := range setOptions {
		v := ""
		switch o.name {
		case "pipefail":
			v = "on"
		case "loopmax":
			v = strconv.Itoa(s.loopMax)
		case "verbmax":
			v = strconv.Itoa(s.verbMax)
		default:
			v = "off"
			if *s.flag(o.name) {
				v = "on"
			}
		}
		rows = append(rows, parent.Row{"name": o.name, "value": v, "desc": o.desc})
	}
	return value{kind: valRows, rows: rows, cols: cols}
}

func optionByShort(c byte) string {
	for _, o := range setOptions {
		if o.short == c && c != 0 {
			return o.name
		}
	}
	return ""
}

// runSet is set and shopt. Both write the one option table.
//
//	set                     list
//	set -e +u -x -f         short flags
//	set -o name, +o name    long names
//	set loopmax N           bounded caps
//	shopt                   list
//	shopt -s name, -u name  long names
func (s *State) runSet(argv []string) (value, error) {
	if len(argv) == 1 {
		return s.optionRows(), nil
	}
	name := argv[0]
	if name == "shopt" {
		if len(argv) < 3 || (argv[1] != "-s" && argv[1] != "-u") {
			return value{}, fmt.Errorf("usage shopt")
		}
		for _, n := range argv[2:] {
			if err := s.setLong(n, argv[1] == "-s"); err != nil {
				return value{}, err
			}
		}
		return value{kind: valBytes}, nil
	}
	if argv[1] == "loopmax" || argv[1] == "verbmax" {
		if len(argv) != 3 {
			return value{}, fmt.Errorf("usage set %s", argv[1])
		}
		n, err := strconv.Atoi(argv[2])
		if err != nil || n < 1 || n > capCeil {
			return value{}, fmt.Errorf("usage set %s: 1-%d", argv[1], capCeil)
		}
		if argv[1] == "loopmax" {
			s.loopMax = n
		} else {
			s.verbMax = n
		}
		return value{kind: valBytes}, nil
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if len(a) < 2 || (a[0] != '-' && a[0] != '+') {
			return value{}, fmt.Errorf("usage set")
		}
		on := a[0] == '-'
		if a[1:] == "o" {
			if i+1 >= len(argv) {
				return value{}, fmt.Errorf("usage set -o")
			}
			i++
			if err := s.setLong(argv[i], on); err != nil {
				return value{}, err
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			n := optionByShort(a[j])
			if n == "" {
				return value{}, fmt.Errorf("usage set: no option %c", a[j])
			}
			if err := s.setLong(n, on); err != nil {
				return value{}, err
			}
		}
	}
	return value{kind: valBytes}, nil
}

func (s *State) setLong(name string, on bool) error {
	if name == "record" {
		// In-session recording needs file.write with a mode (jobs §2);
		// offline recording is sash --record.
		return fmt.Errorf("record needs file.write mode")
	}
	if name == "pipefail" {
		if !on {
			return fmt.Errorf("usage set: pipefail is always on")
		}
		return nil
	}
	f := s.flag(name)
	if f == nil {
		return fmt.Errorf("usage set: no option %s", name)
	}
	if name == "plan" && !on && s.planLocked {
		return fmt.Errorf("usage set: plan is locked on for this run")
	}
	if name == "confirm" && !on && s.confirm {
		return fmt.Errorf(msgConfirmLocked)
	}
	*f = on
	return nil
}

// checkBind refuses to rebind a read-only name.
func (s *State) checkBind(name string) error {
	if s.readonly[name] {
		return fmt.Errorf("readonly %s", name)
	}
	return nil
}

// typed checks a value against a declared type and converts a numeric
// string for a number name.
func (s *State) typed(name string, v value) (value, error) {
	t, ok := s.types[name]
	if !ok {
		return v, nil
	}
	d := toDatum(v)
	switch t {
	case "number":
		n, ok := number(d)
		if !ok {
			return value{}, fmt.Errorf("type %s number", name)
		}
		out := fromDatum(n)
		out.prov = v.prov
		return out, nil
	case "list":
		if _, ok := d.([]any); !ok {
			return value{}, fmt.Errorf("type %s list", name)
		}
	case "map":
		if _, ok := d.(*omap); !ok {
			return value{}, fmt.Errorf("type %s map", name)
		}
	}
	return v, nil
}

// assign binds name to v under the read-only and type rules.
func (s *State) assign(name string, v value) error {
	if err := s.checkBind(name); err != nil {
		return err
	}
	v, err := s.typed(name, v)
	if err != nil {
		return err
	}
	s.vars[name] = v
	return nil
}

func kindName(v value) string {
	switch v.kind {
	case valStr, valBytes:
		return "string"
	case valRow:
		return "row"
	case valRows:
		return "rows"
	}
	return typeName(v.d)
}

// declareRows lists names, types, and flags. Values stay out unless one
// name is asked for with -p: they may hold file contents.
func (s *State) declareRows(only string) (value, error) {
	cols := []string{"name", "type", "flags"}
	if only != "" {
		cols = append(cols, "value")
	}
	var names []string
	for n := range s.vars {
		if only == "" || n == only {
			names = append(names, n)
		}
	}
	if only != "" && len(names) == 0 {
		return value{}, fmt.Errorf("unset %s", only)
	}
	sort.Strings(names)
	var rows []parent.Row
	for _, n := range names {
		var flags []string
		if s.readonly[n] {
			flags = append(flags, "r")
		}
		if t, ok := s.types[n]; ok {
			flags = append(flags, map[string]string{"number": "i", "list": "a", "map": "A"}[t])
		}
		row := parent.Row{"name": n, "type": kindName(s.vars[n]), "flags": strings.Join(flags, "")}
		if only != "" {
			row["value"] = strings.TrimSuffix(asBytes(s.vars[n]), "\n")
		}
		rows = append(rows, row)
	}
	return value{kind: valRows, rows: rows, cols: cols}, nil
}
