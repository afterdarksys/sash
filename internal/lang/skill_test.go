package lang

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/skills"
)

// skillFiles is every embedded skill file by name.
func skillFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	b, err := skills.Main()
	if err != nil {
		t.Fatal(err)
	}
	out["SKILL.md"] = string(b)
	for _, n := range skills.References() {
		b, err := skills.Reference(n)
		if err != nil {
			t.Fatal(err)
		}
		out["reference/"+n+".md"] = string(b)
	}
	return out
}

// tableCells splits a markdown table row on the | that are not escaped.
func tableCells(row string) []string {
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row) && row[i+1] == '|':
			cur.WriteByte('|')
			i++
		case row[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(row[i])
		}
	}
	// A row starts and ends with |: drop the empty edges.
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	return cells
}

// The honesty test: reference/verbs.md lists exactly the closed table,
// each form once, with its kind, its key, and whether plan mode stops it.
// Adding a builtin without the skill row fails here.
func TestSkillVerbsMatchTheClosedTable(t *testing.T) {
	text := skillFiles(t)["reference/verbs.md"]
	type row struct{ kind, key, mutates string }
	got := map[string]row{}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		c := tableCells(line)
		if len(c) != 5 {
			t.Fatalf("row %q has %d cells", line, len(c))
		}
		form := strings.Trim(c[0], "`")
		if _, dup := got[form]; dup {
			t.Errorf("%q is listed twice", form)
		}
		got[form] = row{c[1], c[2], c[3]}
	}
	for _, b := range Builtins() {
		key := b.Key
		if key == "" {
			key = "-"
		}
		mut := "no"
		if mutating[b.Key] {
			mut = "yes"
		}
		want := row{b.Kind, key, mut}
		r, ok := got[b.Form]
		switch {
		case !ok:
			t.Errorf("%q is in builtin.go but not in reference/verbs.md", b.Form)
		case r != want:
			t.Errorf("%q: skill says %+v, builtin.go says %+v", b.Form, r, want)
		}
		delete(got, b.Form)
	}
	for form := range got {
		t.Errorf("%q is in reference/verbs.md but not in builtin.go", form)
	}
}

// Every expression function is named in reference/language.md.
func TestSkillNamesEveryFunction(t *testing.T) {
	text := skillFiles(t)["reference/language.md"]
	for _, f := range funcNames() {
		if !strings.Contains(text, "`"+f+"`") && !strings.Contains(text, "`"+f+"(") {
			t.Errorf("function %s is not in reference/language.md", f)
		}
	}
}

// skillBlock is one fenced sash example: its file, its source, and what a
// "# expect:" first line says it does.
type skillBlock struct {
	file, src string
	expect    string // "", or "parse <stderr>", or "<status> <stderr>"
}

func skillBlocks(t *testing.T) []skillBlock {
	t.Helper()
	var out []skillBlock
	for name, text := range skillFiles(t) {
		lines := strings.Split(text, "\n")
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "```sash" {
				continue
			}
			var body []string
			j := i + 1
			for ; j < len(lines) && strings.TrimSpace(lines[j]) != "```"; j++ {
				body = append(body, lines[j])
			}
			if j == len(lines) {
				t.Fatalf("%s: unclosed sash block at line %d", name, i+1)
			}
			b := skillBlock{file: fmt.Sprintf("%s:%d", name, i+1), src: strings.Join(body, "\n") + "\n"}
			if len(body) > 0 {
				if e, ok := strings.CutPrefix(body[0], "# expect: "); ok {
					b.expect = e
				}
			}
			out = append(out, b)
			i = j
		}
	}
	return out
}

// skillParent fails the test on any call: no example may reach a verb.
type skillParent struct{ t *testing.T }

func (f skillParent) Do(v parent.Verb) (parent.Result, error) {
	f.t.Errorf("an example called the parent: %s", v.Key)
	return parent.Result{}, fmt.Errorf("no parent")
}

// Every sash example in the skill is real sash: it parses (the --lint
// path). An example marked "# expect: parse <msg>" must fail to parse with
// that message; one marked "# expect: <status> <msg>" must fail that way
// when run with an empty grant and a parent that refuses every call, so
// it never reaches a verb.
func TestSkillExamplesAreSash(t *testing.T) {
	blocks := skillBlocks(t)
	if len(blocks) < 20 {
		t.Fatalf("only %d sash examples found", len(blocks))
	}
	files := map[string]bool{}
	for _, b := range blocks {
		files[strings.SplitN(b.file, ":", 2)[0]] = true
		lint := Lint(b.src)
		switch {
		case b.expect == "":
			if lint.Status == exitType {
				t.Errorf("%s: does not parse: %s\n%s", b.file, lint.Stderr, b.src)
			}
		case strings.HasPrefix(b.expect, "parse "):
			msg := strings.TrimPrefix(b.expect, "parse ")
			if lint.Status != exitType || !strings.Contains(lint.Stderr, msg) {
				t.Errorf("%s: want a parse error %q, lint %d %q", b.file, msg, lint.Status, lint.Stderr)
			}
		default:
			if lint.Status == exitType {
				t.Errorf("%s: does not parse: %s", b.file, lint.Stderr)
			}
			status, msg, ok := strings.Cut(b.expect, " ")
			want, err := strconv.Atoi(status)
			if !ok || err != nil {
				t.Fatalf("%s: bad expect %q", b.file, b.expect)
			}
			if msg == "wasm_unsupported" && wasmBuilt {
				// With the sashwasm tag the module load is a file read,
				// which the empty grant denies before any call.
				want, msg = exitDenied, "denied file.read"
			}
			out := Run(b.src, grant.Grant{}, skillParent{t}, Options{})
			if out.Status != want || !strings.Contains(out.Stderr, msg) {
				t.Errorf("%s: want %d %q, got %d %q", b.file, want, msg, out.Status, out.Stderr)
			}
		}
	}
	for _, f := range []string{"SKILL.md", "reference/language.md", "reference/features.md"} {
		if !files[f] {
			t.Errorf("%s has no sash example", f)
		}
	}
}
