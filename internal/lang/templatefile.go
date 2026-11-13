package lang

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// template_file spec tpl out [--check] and template tpl. The files are read
// through file.read and out is written through the same door as ">": the
// grant, the write roots, and the agent re-check all apply.

var schemaKeys = map[string]bool{"type": true, "required": true, "enum": true, "pattern": true, "default": true}

// specVars reads a decoded spec: a plain map of vars, or a map with vars
// and schema. It returns the vars after schema checks and defaults.
func specVars(d any) (*omap, error) {
	o, ok := d.(*omap)
	if !ok {
		return nil, fmt.Errorf("spec must be a map")
	}
	_, hasVars := o.get("vars")
	sch, hasSchema := o.get("schema")
	if !hasVars && !hasSchema {
		return o, nil
	}
	for _, k := range o.keys {
		if k != "vars" && k != "schema" {
			return nil, fmt.Errorf("spec: unknown key %s", safeTag(k))
		}
	}
	vars := newOmap()
	if v, ok := o.get("vars"); ok {
		vm, ok := v.(*omap)
		if !ok {
			return nil, fmt.Errorf("spec: vars must be a map")
		}
		for _, k := range vm.keys {
			vars.set(k, vm.m[k])
		}
	}
	if !hasSchema {
		return vars, nil
	}
	sm, ok := sch.(*omap)
	if !ok {
		return nil, fmt.Errorf("spec: schema must be a map")
	}
	for _, k := range vars.keys {
		if _, ok := sm.get(k); !ok {
			return nil, fmt.Errorf("spec: %s is not in the schema", safeTag(k))
		}
	}
	for _, name := range sm.keys {
		rule, ok := sm.m[name].(*omap)
		if !ok {
			return nil, fmt.Errorf("spec: schema %s must be a map", safeTag(name))
		}
		for _, k := range rule.keys {
			if !schemaKeys[k] {
				return nil, fmt.Errorf("spec: schema %s: unknown rule %s", safeTag(name), safeTag(k))
			}
		}
		v, has := vars.get(name)
		if !has {
			if dflt, ok := rule.get("default"); ok {
				vars.set(name, dflt)
				v, has = dflt, true
			}
		}
		if req, _ := rule.get("required"); req == true && !has {
			return nil, fmt.Errorf("spec: %s is required", safeTag(name))
		}
		if !has {
			continue
		}
		if err := checkRule(name, rule, v); err != nil {
			return nil, err
		}
	}
	return vars, nil
}

func checkRule(name string, rule *omap, v any) error {
	if t, ok := rule.get("type"); ok {
		want, _ := t.(string)
		got := typeName(v)
		switch want {
		case "string", "number", "bool", "list", "map":
		default:
			return fmt.Errorf("spec: schema %s: unknown type", safeTag(name))
		}
		if got != want {
			return fmt.Errorf("spec: %s must be %s", safeTag(name), want)
		}
	}
	if e, ok := rule.get("enum"); ok {
		list, ok := e.([]any)
		if !ok {
			return fmt.Errorf("spec: schema %s: enum must be a list", safeTag(name))
		}
		found := false
		for _, x := range list {
			if dataEqual(x, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("spec: %s is not one of its enum", safeTag(name))
		}
	}
	if p, ok := rule.get("pattern"); ok {
		pat, ok := p.(string)
		if !ok {
			return fmt.Errorf("spec: schema %s: pattern must be a string", safeTag(name))
		}
		re, err := compileRE(pat)
		if err != nil {
			return fmt.Errorf("spec: schema %s: %s", safeTag(name), err.Error())
		}
		s, ok := v.(string)
		if !ok || !re.MatchString(s) {
			return fmt.Errorf("spec: %s does not match its pattern", safeTag(name))
		}
	}
	return nil
}

func specFormat(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	}
	return ""
}

func classifyTemplateFile(argv []string) (classified, *problem) {
	check := len(argv) == 5 && argv[4] == "--check"
	if len(argv) != 4 && !check {
		return classified{}, &problem{exitType, "usage template_file spec tpl out [--check]"}
	}
	if specFormat(argv[1]) == "" {
		return classified{}, &problem{exitType, "usage template_file: spec must end .yaml .yml .json or .toml"}
	}
	key := "file.write"
	if check {
		key = "file.read"
	}
	return classified{key: key, in: inNone, out: inBytes}, nil
}

// readFile reads one path the stage already bound.
func (m *machine) readFile(p string) (string, error) {
	res, err := m.do(parent.Verb{Key: "file.read", Argv: []string{"cat", p}, Path: p})
	if err != nil || res.Exit != 0 {
		return "", fmt.Errorf("cannot read")
	}
	if len(res.Bytes) > dataMaxBytes {
		return "", fmt.Errorf("too large")
	}
	return string(res.Bytes), nil
}

func (m *machine) runTemplateFile(st expStage) (value, int) {
	fail := func(msg string) (value, int) {
		m.err.WriteString(msg + "\n")
		return value{}, exitType
	}
	specText, err := m.readFile(st.paths[0])
	if err != nil {
		return fail("template_file spec: " + err.Error())
	}
	tpl, err := m.readFile(st.paths[1])
	if err != nil {
		return fail("template_file tpl: " + err.Error())
	}
	sv, err := runFrom(specFormat(st.paths[0]), value{kind: valStr, s: specText})
	if err != nil {
		return fail("template_file spec: " + err.Error())
	}
	vars, err := specVars(sv.d)
	if err != nil {
		return fail(err.Error())
	}
	out, err := renderTemplatePinned(tpl, vars, m.st.loopMax, m.uuidPin())
	if err != nil {
		return fail(err.Error())
	}
	// Plan mode diffs only a file this session may read; a write-only out
	// is reported by the door as a write, never read.
	readable := m.g.Has("file.read") && m.g.AllowsRead(st.paths[2])
	if len(st.argv) == 5 || (m.st.plan && readable) {
		// --check, or plan mode: diff the current file against the render.
		// In plan mode the write still goes to the door, which prints it.
		cur, rerr := m.readFile(st.paths[2])
		if rerr != nil {
			cur = ""
		}
		d, same := lineDiff(cur, out)
		if len(st.argv) == 5 {
			if same {
				return value{kind: valBytes}, 0
			}
			return value{kind: valBytes, b: []byte(d)}, 1
		}
		m.err.WriteString(d)
	}
	return value{kind: valBytes, b: []byte(out)}, 0
}

func (m *machine) runTemplate(st expStage, in value) (value, int) {
	tpl, err := m.readFile(st.path)
	if err != nil {
		m.err.WriteString("template: " + err.Error() + "\n")
		return value{}, exitType
	}
	out, err := renderTemplatePinned(tpl, toDatum(in), m.st.loopMax, m.uuidPin())
	if err != nil {
		if errors.Is(err, errSecretFault) {
			m.secretFault()
			m.ctl = ctlHalt
		}
		m.err.WriteString(err.Error() + "\n")
		return value{}, exitType
	}
	return value{kind: valBytes, b: []byte(out)}, 0
}

// diffMaxLines bounds the line diff; past it the files are reported as
// different without a listing.
const diffMaxLines = 4000

// lineDiff is a line diff by longest common subsequence: "- " lines only in
// a, "+ " lines only in b.
func lineDiff(a, b string) (string, bool) {
	if a == b {
		return "", true
	}
	al := strings.Split(a, "\n")
	bl := strings.Split(b, "\n")
	if len(al) > diffMaxLines || len(bl) > diffMaxLines {
		return "files differ (too large to list)\n", false
	}
	n, m := len(al), len(bl)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + bl[j] + "\n")
			j++
		default:
			out.WriteString("- " + al[i] + "\n")
			i++
		}
	}
	return out.String(), false
}
