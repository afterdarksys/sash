package lang

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	parsed := 0
	for _, line := range strings.Split(text, "\n") {
		// Every table line is the header, the separator, or one form:
		// a row this test cannot read is an error, never skipped.
		if !strings.HasPrefix(line, "|") || line == "| form | kind | key | mutates | what |" || strings.HasPrefix(line, "| --- |") {
			continue
		}
		c := tableCells(line)
		if len(c) != 5 || len(c[0]) < 3 || !strings.HasPrefix(c[0], "`") || !strings.HasSuffix(c[0], "`") || strings.Count(c[0], "`") != 2 {
			t.Errorf("unreadable table row %q", line)
			continue
		}
		form := strings.Trim(c[0], "`")
		if _, dup := got[form]; dup {
			t.Errorf("%q is listed twice", form)
		}
		got[form] = row{c[1], c[2], c[3]}
		parsed++
	}
	if parsed != len(Builtins()) {
		t.Errorf("reference/verbs.md has %d rows, builtin.go %d forms", parsed, len(Builtins()))
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

// skillBlock is one fenced sash example: its file, its source, and the
// "# " marker lines at its top that say what it does.
type skillBlock struct {
	file, src string
	expect    string   // "", "parse <msg>", or "<empty|host|plan> <status> [<msg>]"
	marks     []string // "stdout: x", "nostdout: x", "stderr: x", "call: x", "nocall: x", "lint: x", "nolint: x"
	drop      []string // entitlements "# drop: key" removes from the host grant
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
			for _, l := range body {
				m, ok := strings.CutPrefix(l, "# ")
				if !ok {
					break
				}
				if e, ok := strings.CutPrefix(m, "expect: "); ok {
					b.expect = e
					continue
				}
				if d, ok := strings.CutPrefix(m, "drop: "); ok {
					b.drop = append(b.drop, d)
					continue
				}
				for _, k := range []string{"stdout: ", "nostdout: ", "stderr: ", "call: ", "nocall: ", "lint: ", "nolint: "} {
					if strings.HasPrefix(m, k) {
						b.marks = append(b.marks, m)
					}
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

// skillFS is the fake host the examples run against: file bodies by path,
// and directory listings for globs. Nothing here touches the real host.
var skillFS = map[string]string{
	"/srv/app/token":           "s3cr3tvalue\n",
	"/srv/app/token.copy":      "s3cr3tvalue\n",
	"/srv/app/app.yaml":        "port: 8080\nhosts: [web1, web2]\n",
	"/srv/app/app.tpl":         "listen {{ $port }}\n",
	"/srv/app/app.conf":        "listen 8080\n",
	"/srv/app/db.yaml":         "password: s3cr3tvalue\n",
	"/srv/app/db.tpl":          "password={{ $password }}\n",
	"/srv/app/lib.sash":        "fn hi(n) { echo hello $n }\ndeclare -r port = 8080\n",
	"/etc/nginx/nginx.conf":    "listen 80;\nerror_log /var/log/nginx/error.log;\n",
	"/var/log/nginx/error.log": "an error line\n",
}

var skillDirs = map[string][]parent.Row{
	"/var/log/nginx": {{"name": "error.log", "type": "file"}},
	"/srv/app":       {{"name": "app.yaml", "type": "file"}, {"name": "conf.d", "type": "dir"}},
	"/var/log":       {{"name": "nginx", "type": "dir"}},
}

// skillHost answers every verb from fixtures and records each call.
type skillHost struct{ calls []string }

func (h *skillHost) Do(v parent.Verb) (parent.Result, error) {
	h.calls = append(h.calls, fmt.Sprintf("%s argv=%s path=%s mode=%s body=%q", v.Key, strings.Join(v.Argv, " "), v.Path, v.Mode, v.Body))
	rows := func(cols []string, r ...parent.Row) (parent.Result, error) {
		return parent.Result{Kind: parent.KindRows, Cols: cols, Rows: r}, nil
	}
	text := func(s string) (parent.Result, error) {
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte(s)}, nil
	}
	switch v.Key {
	case "ps":
		return rows([]string{"user", "pid", "cmd"}, parent.Row{"user": "root", "pid": "1", "cmd": "init"}, parent.Row{"user": "www", "pid": "40", "cmd": "nginx"})
	case "unit.status":
		return rows([]string{"unit", "state"}, parent.Row{"unit": v.Argv[len(v.Argv)-1], "state": "active"})
	case "package.query":
		return rows([]string{"name", "version"}, parent.Row{"name": "nginx", "version": "1.24"})
	case "sysctl.get":
		if v.Argv[len(v.Argv)-1] == "fail.three" {
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte("no such key\n"), Exit: 3}, nil
		}
		return text("1\n")
	case "config.read":
		return text("key = value\n")
	case "atomic.begin":
		return rows([]string{"deadline"}, parent.Row{"deadline": "4102444800"})
	case "file.read":
		switch v.Argv[0] {
		case "ls":
			return rows([]string{"name", "type"}, skillDirs[v.Path]...)
		case "stat":
			return text("")
		}
		if b, ok := skillFS[v.Path]; ok {
			return text(b)
		}
		return parent.Result{}, fmt.Errorf("no_such_file")
	}
	return text("")
}

func skillGrant(drop []string) grant.Grant {
	var ents []string
	for _, e := range []string{"ps", "unit.status", "unit.restart", "package.query", "package.install", "sysctl.get", "sysctl.set", "config.read", "file.read", "file.write"} {
		if !slices.Contains(drop, e) {
			ents = append(ents, e)
		}
	}
	return grant.Grant{V: "sash/1", Purpose: "terminal:open", Entitlements: ents,
		FileRead: []string{"/srv", "/var", "/etc"}, FileWrite: []string{"/srv", "/var/tmp"}}
}

// Every sash example in the skill is real sash and does what the text
// says. It must parse (the --lint path), and then it runs:
//   - unmarked: against the fake host with a full grant, under a locked
//     plan (sash --plan), and must end 0 with no mutating call reaching
//     the host;
//   - "# expect: parse <msg>": must fail to parse with that message;
//   - "# expect: empty <status> <msg>": with an empty grant and a parent
//     that fails the test on any call;
//   - "# expect: host <status> <msg>" or "plan ...": against the fake host,
//     without or with the plan lock.
//
// "# stdout:", "# nostdout:", "# stderr:", "# call:", "# nocall:", "# lint:",
// and "# nolint:" lines check the output, the host's calls, and the lint
// table (substrings); "# drop: key" takes an entitlement out of the grant.
func TestSkillExamplesAreSash(t *testing.T) {
	blocks := skillBlocks(t)
	if len(blocks) < 30 {
		t.Fatalf("only %d sash examples found", len(blocks))
	}
	files := map[string]bool{}
	for _, b := range blocks {
		files[strings.SplitN(b.file, ":", 2)[0]] = true
		lint := Lint(b.src)
		if msg, ok := strings.CutPrefix(b.expect, "parse "); ok {
			if lint.Status != exitType || !strings.Contains(lint.Stderr, msg) {
				t.Errorf("%s: want a parse error %q, lint %d %q", b.file, msg, lint.Status, lint.Stderr)
			}
			continue
		}
		if lint.Status == exitType {
			t.Errorf("%s: does not parse: %s\n%s", b.file, lint.Stderr, b.src)
			continue
		}
		mode, want, msg := "plan", 0, ""
		if b.expect != "" {
			f := strings.SplitN(b.expect, " ", 3)
			n, err := strconv.Atoi(f[min(1, len(f)-1)])
			if len(f) < 2 || err != nil || (f[0] != "empty" && f[0] != "host" && f[0] != "plan") {
				t.Fatalf("%s: bad expect %q", b.file, b.expect)
			}
			mode, want = f[0], n
			if len(f) == 3 {
				msg = f[2]
			}
		}
		var out Outcome
		host := &skillHost{}
		switch mode {
		case "empty":
			if msg == "wasm_unsupported" && wasmBuilt {
				// With the sashwasm tag the module load is a file read,
				// which the empty grant denies before any call.
				want, msg = exitDenied, "denied file.read"
			}
			out = Run(b.src, grant.Grant{}, skillParent{t}, Options{})
		case "host":
			out = Run(b.src, skillGrant(b.drop), host, Options{})
		case "plan":
			st := NewState()
			st.LockPlan()
			out = Run(b.src, skillGrant(b.drop), host, Options{State: st})
			for _, c := range host.calls {
				if mutating[strings.SplitN(c, " ", 2)[0]] {
					t.Errorf("%s: plan mode reached the host: %s", b.file, c)
				}
			}
		}
		if out.Status != want || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: want %s %d %q, got %d stdout %q stderr %q", b.file, mode, want, msg, out.Status, out.Stdout, out.Stderr)
		}
		calls := strings.Join(host.calls, "\n")
		for _, m := range b.marks {
			k, v, _ := strings.Cut(m, ": ")
			ok := map[string]bool{
				"stdout":   strings.Contains(out.Stdout, v),
				"nostdout": !strings.Contains(out.Stdout, v),
				"stderr":   strings.Contains(out.Stderr, v),
				"call":     strings.Contains(calls, v),
				"nocall":   !strings.Contains(calls, v),
				"lint":     strings.Contains(lint.Stdout, v),
				"nolint":   !strings.Contains(lint.Stdout, v),
			}[k]
			if !ok {
				t.Errorf("%s: %s failed\nstdout %q\nstderr %q\ncalls:\n%s", b.file, m, out.Stdout, out.Stderr, calls)
			}
		}
	}
	for _, f := range []string{"SKILL.md", "reference/language.md", "reference/features.md"} {
		if !files[f] {
			t.Errorf("%s has no sash example", f)
		}
	}
}

// features.md says a recording redacts only bound secrets: the response a
// declare -s read and any text equal to a bound secret. A plain cat of the
// same file, with no secret bound, is kept as it came.
func TestSkillRecordingRedactsOnlyBoundSecrets(t *testing.T) {
	rec := func(src string) string {
		t.Helper()
		var buf bytes.Buffer
		tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if out := Run(src, skillGrant(nil), &skillHost{}, Options{Tape: tape}); out.Status != 0 {
			t.Fatalf("%q: %+v", src, out)
		}
		if err := tape.Finish(); err != nil {
			t.Fatal(err)
		}
		// Byte responses are stored base64-encoded.
		return buf.String()
	}
	const clear = "czNjcjN0dmFsdWUK" // base64 of the token file

	if r := rec("let raw = (cat /srv/app/token)\n"); !strings.Contains(r, clear) {
		t.Errorf("an unbound read was redacted: %s", r)
	}
	if r := rec("declare -s tok = (cat /srv/app/token)\nlet raw = (cat /srv/app/token.copy)\n"); strings.Contains(r, clear) || strings.Contains(r, "s3cr3tvalue") {
		t.Errorf("a bound secret's text is in the recording: %s", r)
	}
	text := skillFiles(t)["reference/features.md"]
	if !strings.Contains(text, "Only bound secrets are redacted") {
		t.Errorf("features.md no longer states the recording rule")
	}
}

// The atomic limits features.md names are the ones sash prints for the
// agent's refusals.
func TestSkillAtomicLimits(t *testing.T) {
	text := skillFiles(t)["reference/features.md"]
	msgs := ""
	for _, m := range fileWriteRefusals {
		msgs += m + "\n"
	}
	for _, want := range []string{"64 files", "32 MiB", "0600, 0640, or 0644"} {
		if !strings.Contains(msgs, want) || !strings.Contains(text, want) {
			t.Errorf("%q: in sash's messages %v, in features.md %v", want, strings.Contains(msgs, want), strings.Contains(text, want))
		}
	}
	for _, code := range []string{"atomic_files", "atomic_size", "atomic_mode", "owner_changed", "atomic_inconsistent", "atomic_restored"} {
		if !strings.Contains(text, code) {
			t.Errorf("features.md does not name %s", code)
		}
	}
	if _, ok := fileWriteRefusals["owner_changed"]; !ok {
		t.Errorf("owner_changed is no longer a refusal sash knows")
	}
}
