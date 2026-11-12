package lang

import (
	"regexp"
	"strings"
	"testing"
)

type needsWant struct {
	key, target, mutates, note string
}

var needsSplit = regexp.MustCompile(`\s{2,}`)

// needsTableRows parses the table: key, target, mutates, lines, note.
func needsTableRows(t *testing.T, out Outcome) [][]string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out.Stdout, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "key") {
		t.Fatalf("header %q", lines[0])
	}
	var rows [][]string
	for _, l := range lines[1:] {
		f := needsSplit.Split(strings.TrimRight(l, " "), -1)
		for len(f) < 5 {
			f = append(f, "")
		}
		rows = append(rows, f)
	}
	return rows
}

func needsHas(rows [][]string, w needsWant) bool {
	for _, r := range rows {
		if r[0] == w.key && r[1] == w.target && (w.mutates == "" || r[2] == w.mutates) && r[4] == w.note {
			return true
		}
	}
	return false
}

func TestNeedsVerbKinds(t *testing.T) {
	cases := []struct {
		src    string
		status int
		want   []needsWant
		policy []string
	}{
		{"ps -ef", 0, []needsWant{{"ps", "-", "no", ""}}, nil},
		{"journal", 1, []needsWant{{"journal", "-", "no", noteNotGrantable}}, nil},
		{"cat /etc/hosts", 0, []needsWant{{"file.read", "/etc/hosts", "no", ""}}, []string{"--file-read /etc"}},
		{"ls /var/log", 0, []needsWant{{"file.read", "/var/log", "no", ""}}, []string{"--file-read /var/log"}},
		{"cat < /etc/a", 0, []needsWant{{"file.read", "/etc/a", "no", ""}}, []string{"--file-read /etc"}},
		{"echo hi > /tmp/out", 0, []needsWant{{"file.write", "/tmp/out", "yes", ""}}, []string{"--file-write /tmp"}},
		{"unit status nginx", 0, []needsWant{{"unit.status", "nginx", "no", ""}}, []string{"--service-status nginx"}},
		{"unit restart nginx", 0, []needsWant{{"unit.restart", "nginx", "yes", ""}}, []string{"--service nginx"}},
		{"unit status", 1, []needsWant{{"unit.status", "-", "no", noteNotGrantable}}, nil},
		{"package query curl", 0, []needsWant{{"package.query", "curl", "no", ""}}, []string{"--package curl"}},
		{"package install curl", 0, []needsWant{{"package.install", "curl", "yes", ""}}, []string{"--package curl"}},
		{"sysctl get net.ipv4.ip_forward", 0, []needsWant{{"sysctl.get", "net.ipv4.ip_forward", "no", ""}}, []string{"--tweak-sysctl net.ipv4.ip_forward"}},
		{"sysctl set vm.swappiness 10", 0, []needsWant{{"sysctl.set", "vm.swappiness", "yes", ""}}, []string{"--tweak-sysctl vm.swappiness"}},
		// A run checks config read against the file_read roots too.
		{"config read /etc/nginx/nginx.conf", 0, []needsWant{{"config.read", "/etc/nginx/nginx.conf", "no", ""}, {"file.read", "/etc/nginx/nginx.conf", "no", ""}},
			[]string{"--config /etc/nginx/nginx.conf", "--file-read /etc/nginx"}},
		{"config read etc/nginx", 1, []needsWant{{"config.read", "etc/nginx", "no", noteRelative}, {"file.read", "etc/nginx", "no", noteRelative}}, nil},
		{"template /etc/t.tpl", 0, []needsWant{{"file.read", "/etc/t.tpl", "no", ""}}, []string{"--file-read /etc"}},
		{"template_file /s/v.yaml /t/a.tpl /o/a.conf", 0, []needsWant{
			{"file.read", "/s/v.yaml", "no", ""}, {"file.read", "/t/a.tpl", "no", ""}, {"file.write", "/o/a.conf", "yes", ""},
		}, []string{"--file-read /s", "--file-read /t", "--file-write /o"}},
		{"cd /tmp", 0, []needsWant{{"file.read", "/tmp", "no", ""}}, []string{"--file-read /tmp"}},
		{"pushd /srv", 0, []needsWant{{"file.read", "/srv", "no", ""}}, []string{"--file-read /srv"}},
		{"with prefix /srv { cat a }", 0, []needsWant{{"file.read", "/srv", "no", ""}, {"file.read", "/srv/a", "no", ""}}, []string{"--file-read /srv"}},
		{"cat /var/log/*.log", 0, []needsWant{{"file.read", "/var/log", "no", ""}}, []string{"--file-read /var/log"}},
		{"cat /var/log/**/x.log", 0, []needsWant{{"file.read", "/var/log", "no", ""}}, []string{"--file-read /var/log"}},
		{"use /etc/lib.sash", 1, []needsWant{{"file.read", "/etc/lib.sash", "no", noteUse}}, []string{"--file-read /etc"}},
		{"echo hi; cat; egrep x", 0, nil, nil},
	}
	for _, c := range cases {
		out := Needs(c.src, NeedsOptions{})
		if out.Status != c.status {
			t.Errorf("%q: status %d want %d\n%s", c.src, out.Status, c.status, out.Stdout)
			continue
		}
		rows := needsTableRows(t, out)
		if len(rows) != len(c.want) {
			t.Errorf("%q: rows %q want %v", c.src, rows, c.want)
		}
		for _, w := range c.want {
			if !needsHas(rows, w) {
				t.Errorf("%q: missing %v in %q", c.src, w, rows)
			}
		}
		pol := Needs(c.src, NeedsOptions{Policy: true})
		if pol.Status != c.status {
			t.Errorf("%q: policy status %d", c.src, pol.Status)
		}
		var flags []string
		for _, l := range strings.Split(strings.TrimRight(pol.Stdout, "\n"), "\n") {
			if l != "" && !strings.HasPrefix(l, "# ") {
				flags = append(flags, l)
			}
		}
		if strings.Join(flags, "|") != strings.Join(c.policy, "|") {
			t.Errorf("%q: policy %q want %q", c.src, flags, c.policy)
		}
	}
}

// template_file --check reads out; it writes nothing.
func TestNeedsTemplateFileCheckOnlyReads(t *testing.T) {
	out := Needs("template_file /s/v.yaml /t/a.tpl /o/a.conf --check", NeedsOptions{})
	if out.Status != 0 || strings.Contains(out.Stdout, "file.write") || !needsHas(needsTableRows(t, out), needsWant{"file.read", "/o/a.conf", "no", ""}) {
		t.Fatalf("status %d\n%s", out.Status, out.Stdout)
	}
}

// Every branch, loop body, handler, block, and fn body counts, called
// or not.
func TestNeedsCountsEveryBranch(t *testing.T) {
	src := `if [ true ] {
  cat /n/then
} else {
  cat /n/else
}
if cat /n/cond { echo y }
for x in cat /n/src |> lines { cat /n/for }
while [ false ] { cat /n/while }
do { cat /n/do } while [ false ]
try { cat /n/try } catch e { cat /n/catch }
with set -e { cat /n/with }
with v = (cat /n/withlet) { echo $v }
retry 2 { cat /n/retry }
timeout 1s { cat /n/timeout }
let a = (cat /n/let)
declare -r b = (cat /n/declare)
fn never() { cat /n/fn }
fn outer() { fn inner() { cat /n/inner } }
cat /n/a && cat /n/and || cat /n/or
`
	out := Needs(src, NeedsOptions{})
	if out.Status != 0 {
		t.Fatalf("status %d stderr %q\n%s", out.Status, out.Stderr, out.Stdout)
	}
	rows := needsTableRows(t, out)
	for _, p := range []string{"then", "else", "cond", "src", "for", "while", "do", "try", "catch", "with", "withlet",
		"retry", "timeout", "let", "declare", "fn", "inner", "a", "and", "or"} {
		if !needsHas(rows, needsWant{"file.read", "/n/" + p, "no", ""}) {
			t.Errorf("missing /n/%s in\n%s", p, out.Stdout)
		}
	}
}

func TestNeedsDynamicIsIncomplete(t *testing.T) {
	cases := []struct {
		src  string
		want []needsWant
	}{
		{"cat $f", []needsWant{{"file.read", "$", "no", noteDynamic}}},
		{"echo x > $f", []needsWant{{"file.write", "$", "yes", noteDynamic}}},
		{"unit status $u", []needsWant{{"unit.status", "$", "no", noteDynamic}}},
		// A value subcommand may be either form.
		{"unit $s nginx", []needsWant{{"unit.status", "nginx", "no", noteDynamic}, {"unit.restart", "nginx", "yes", noteDynamic}}},
		{"package $s curl", []needsWant{{"package.query", "curl", "no", noteDynamic}, {"package.install", "curl", "yes", noteDynamic}}},
		{"sysctl $s k 1", []needsWant{{"sysctl.set", "k", "yes", noteDynamic}}},
		// The path resolves and is judged as for config read.
		{"cd /etc; config $op app.toml", []needsWant{{"file.read", "/etc/app.toml", "no", noteDynamic}, {"config.read", "app.toml", "no", noteRelative}}},
		{"config $op /etc/x/../shadow", []needsWant{{"file.read", "/etc/shadow", "no", noteDynamic}, {"config.read", "/etc/x/../shadow", "no", noteNotGrantable}}},
		{"config $op /etc/x", []needsWant{{"config.read", "/etc/x", "no", noteDynamic}, {"file.read", "/etc/x", "no", noteDynamic}}},
		{"config read $p", []needsWant{{"config.read", "$", "no", noteDynamic}, {"file.read", "$", "no", noteDynamic}}},
		{"use $lib", []needsWant{{"file.read", "$", "no", noteUse}}},
		{"with prefix $d { cat a }", []needsWant{{"file.read", "$", "no", noteDynamic}, {"file.read", "a", "no", noteRelative}}},
		// A bound literal is not resolved.
		{"let f = /etc/hosts\ncat $f", []needsWant{{"file.read", "$", "no", noteDynamic}}},
	}
	for _, c := range cases {
		out := Needs(c.src, NeedsOptions{})
		rows := needsTableRows(t, out)
		for _, w := range c.want {
			if !needsHas(rows, w) {
				t.Errorf("%q: missing %v in\n%s", c.src, w, out.Stdout)
			}
		}
		incomplete := false
		for _, w := range c.want {
			if w.note != "" {
				incomplete = true
			}
		}
		if incomplete && out.Status != 1 {
			t.Errorf("%q: status %d want 1", c.src, out.Status)
		}
		if strings.Contains(out.Stdout, "/etc/hosts") {
			t.Errorf("%q: resolved a value\n%s", c.src, out.Stdout)
		}
	}
	// A value command name does not run: the run refuses the stage.
	for _, src := range []string{"$cmd /etc/x", "let c = unit\n$c restart nginx", "fn f(p) { $p restart nginx }"} {
		out := Needs(src, NeedsOptions{})
		rows := needsTableRows(t, out)
		if out.Status != 1 || !needsHas(rows, needsWant{"-", "$", "no", noteCommandFromValue}) ||
			strings.Contains(out.Stdout, "unit.restart") {
			t.Fatalf("%q: %d\n%s", src, out.Status, out.Stdout)
		}
	}
}

// A relative path resolves only under one literal base; otherwise it is
// reported as written and the result is incomplete.
func TestNeedsRelativeBase(t *testing.T) {
	cases := []struct {
		src    string
		target string
		note   string
	}{
		{"cat x", "x", noteRelative},
		{"ls", ".", noteRelative},
		{"cd /etc; cat hosts", "/etc/hosts", ""},
		{"cd /etc; ls", "/etc", ""},
		{"cd /etc; cat ../x", "/x", ""},
		{"cd /a; cd b; cat c", "c", noteRelative},
		{"cd b; cat c", "c", noteRelative},
		{"cd /a; echo hi > out", "/a/out", ""},
		{"pushd /a; popd; cat c", "c", noteRelative},
		{"cd /a; if [ true ] { cd /b }; cat c", "c", noteRelative},
		{"if [ true ] { cd /b }; cat c", "/b/c", ""},
		{"cd /a; for x in [ [1] ] { cat c; cd /b }", "c", noteRelative},
		{"cd /a; while [ false ] { cd /b }; cat c", "c", noteRelative},
		{"cd /a; retry 2 { cat c; cd /b }", "c", noteRelative},
		{"cd /a; try { cd /b } catch e { cat c }", "c", noteRelative},
		{"cd /a; with prefix /b { cd /c }; cat d", "/a/d", ""},
		{"cd /a; with prefix b { cat c }", "/a/b/c", ""},
		{"cd /a; fn f() { cat c }", "c", noteRelative},
		{"fn f() { cd /b }\ncd /a; f; cat c", "c", noteRelative},
		{"cd /a; g; cat c", "/a/c", ""},
		{"use /l.sash; cd /a; g; cat c", "c", noteRelative},
		{"cd $d; cat c", "c", noteRelative},
		// A run lists a glob against the starting prefix, which a
		// script does not have.
		{"cd /var/log; echo *.log", ".", noteRelative},
	}
	for _, c := range cases {
		out := Needs(c.src, NeedsOptions{})
		if !needsHas(needsTableRows(t, out), needsWant{key: "file.read", target: c.target, note: c.note}) &&
			!needsHas(needsTableRows(t, out), needsWant{key: "file.write", target: c.target, note: c.note}) {
			t.Errorf("%q: want %s %q in\n%s", c.src, c.target, c.note, out.Stdout)
		}
		if c.note != "" && out.Status != 1 {
			t.Errorf("%q: status %d want 1", c.src, out.Status)
		}
	}
}

func TestNeedsPolicyDedupesAndComments(t *testing.T) {
	src := "cat /etc/a; cat /etc/b\nls /etc\nunit status nginx; unit status nginx\ncat $x\njournal\nps\ncat rel\nuse /lib/x.sash\ncat \"/srv/a b/c\""
	out := Needs(src, NeedsOptions{Policy: true})
	want := "--file-read '/srv/a b'\n" +
		"--file-read /etc\n" +
		"--file-read /lib\n" +
		"--service-status nginx\n" +
		"# file.read $: the target or the subcommand comes from a value (lines 4)\n" +
		"# file.read /lib/x.sash: use reads this file; the needs of what it defines are not listed (lines 8)\n" +
		"# file.read rel: a relative path with no single literal base (lines 7)\n" +
		"# journal -: no policy flag grants this (lines 5)\n"
	if out.Status != 1 || out.Stdout != want {
		t.Fatalf("status %d\n%s\nwant\n%s", out.Status, out.Stdout, want)
	}
}

func TestNeedsLinesAreCapped(t *testing.T) {
	out := Needs(strings.Repeat("cat /etc/a\n", 12), NeedsOptions{})
	rows := needsTableRows(t, out)
	if len(rows) != 1 || rows[0][3] != "1,2,3,4,5,6,7,8,9,10,…" {
		t.Fatalf("%q", rows)
	}
}

func TestNeedsRefusesWhatARunRefuses(t *testing.T) {
	for _, src := range []string{"cat (", "if {", "cat /a/$x*", "cat /a/***", "echo !!", "echo {1..99999999}"} {
		out := Needs(src, NeedsOptions{})
		if out.Status != exitType || out.Stdout != "" || out.Stderr == "" {
			t.Errorf("%q: %+v", src, out)
		}
		if pol := Needs(src, NeedsOptions{Policy: true}); pol.Status != exitType || pol.Stdout != "" {
			t.Errorf("%q policy: %+v", src, pol)
		}
	}
}

// A declare -s value never reaches the output: a target from it is $.
func TestNeedsNeverPrintsASecret(t *testing.T) {
	src := "declare -s tok = \"hunter2\"\ncat $tok\nunit status $tok\nsysctl set k $tok\necho $tok > /tmp/x\nlet p = /etc/$tok\ncat $p"
	for _, policy := range []bool{false, true} {
		out := Needs(src, NeedsOptions{Policy: policy})
		if strings.Contains(out.Stdout+out.Stderr, "hunter2") {
			t.Fatalf("secret printed:\n%s", out.Stdout)
		}
		if out.Status != 1 {
			t.Fatalf("status %d", out.Status)
		}
	}
}

// A crafted literal cannot drive the terminal or forge a column.
func TestNeedsEscapesControlCharacters(t *testing.T) {
	src := "cat '/etc/a\x1b[31mb'\nunit status 'ng\tinx'\ncat '/etc/c\u0085d'\ncat '/etc/e\x7ff'\ncat '/x\x1b/y'"
	for _, policy := range []bool{false, true} {
		out := Needs(src, NeedsOptions{Policy: policy})
		for _, bad := range []string{"\x1b", "\x7f", "\u0085", "\tinx"} {
			if strings.Contains(out.Stdout, bad) {
				t.Fatalf("policy %v printed %q:\n%q", policy, bad, out.Stdout)
			}
		}
		if !policy && !strings.Contains(out.Stdout, `/etc/a\x1b[31mb`) {
			t.Fatalf("escape missing:\n%s", out.Stdout)
		}
		if policy && !strings.Contains(out.Stdout, `# file.read /x\x1b/y: the path cannot be a flag value`) {
			t.Fatalf("an unpasteable root must be a comment:\n%s", out.Stdout)
		}
		// The root /x\x1b cannot be pasted, so the manifest is incomplete.
		if out.Status != 1 {
			t.Fatalf("policy %v: status %d", policy, out.Status)
		}
	}
	cells := map[string]string{
		"a\tb\\c\x01\xff\u009b": `a b\c\x01\xff\u009b`,
		"a\u202eb":              `a\u202eb`,
		"a\u200bb\u200db":       `a\u200bb\u200db`,
		"a\u2028b\u2029c":       `a\u2028b\u2029c`,
		"a\ufeffb":              `a\ufeffb`,
		"a\U000e0001b":          `a\U000e0001b`,
		"/etc/naïve-日本":         "/etc/naïve-日本",
	}
	for in, want := range cells {
		if got := needsCell(in); got != want {
			t.Errorf("cell %q = %q want %q", in, got, want)
		}
	}
}

// Bidi and zero-width characters are escaped in the table and the policy.
func TestNeedsEscapesFormatCharacters(t *testing.T) {
	src := "cat '/etc/a\u202eb'\nunit status 'ngi\u200bnx'\npackage query 'cu\u2028rl'"
	for _, policy := range []bool{false, true} {
		out := Needs(src, NeedsOptions{Policy: policy})
		for _, bad := range []string{"\u202e", "\u200b", "\u2028"} {
			if strings.Contains(out.Stdout, bad) {
				t.Fatalf("policy %v printed %q:\n%q", policy, bad, out.Stdout)
			}
		}
		if out.Status != 1 {
			t.Fatalf("policy %v: status %d\n%s", policy, out.Status, out.Stdout)
		}
		if policy && (strings.Contains(out.Stdout, "--service-status") || strings.Contains(out.Stdout, "--package") ||
			!strings.Contains(out.Stdout, "# unit.status ngi\\u200bnx: the unit cannot be a flag value") ||
			!strings.Contains(out.Stdout, "# package.query cu\\u2028rl: the package cannot be a flag value") ||
			!strings.Contains(out.Stdout, "--file-read /etc\n")) {
			t.Fatalf("policy:\n%s", out.Stdout)
		}
	}
}

// A target that starts with "-" would be read as another flag: it is a
// comment, never a flag, and the manifest is incomplete.
func TestNeedsFlagShapedTargetIsNotAFlag(t *testing.T) {
	cases := []struct{ src, kind string }{
		{`unit status "--file-write=/"`, "unit"},
		{`unit restart -x`, "unit"},
		{`package install "--file-write=/"`, "package"},
		{`sysctl get -a`, "sysctl key"},
	}
	for _, c := range cases {
		out := Needs(c.src, NeedsOptions{Policy: true})
		if out.Status != 1 {
			t.Errorf("%q: status %d", c.src, out.Status)
		}
		for _, l := range strings.Split(strings.TrimRight(out.Stdout, "\n"), "\n") {
			if !strings.HasPrefix(l, "# ") {
				t.Errorf("%q: flag line %q", c.src, l)
			}
		}
		if !strings.Contains(out.Stdout, "the "+c.kind+" cannot be a flag value") {
			t.Errorf("%q: %q", c.src, out.Stdout)
		}
		tab := Needs(c.src, NeedsOptions{})
		if tab.Status != 1 || !needsHas(needsTableRows(t, tab), needsWant{key: needsTableRows(t, tab)[0][0], target: needsTableRows(t, tab)[0][1], note: noteUnpasteable}) {
			t.Errorf("%q table:\n%s", c.src, tab.Stdout)
		}
	}
}

// A sample for every form with a key; needs must report that key. A verb
// added to the builtin table without a needs mapping fails here.
func TestNeedsCoversEveryBuiltinKey(t *testing.T) {
	samples := map[string]string{
		"ps":              "ps",
		"unit status":     "unit status nginx",
		"unit restart":    "unit restart nginx",
		"journal":         "journal",
		"package query":   "package query curl",
		"package install": "package install curl",
		"sysctl get":      "sysctl get k",
		"sysctl set":      "sysctl set k 1",
		"config read":     "config read /etc/x",
		"cat":             "cat /etc/x",
		"ls":              "ls /etc",
		"cd":              "cd /etc",
		"pushd":           "pushd /etc",
		"use":             "use /etc/x.sash",
		"template_file":   "template_file /s.yaml /t /o",
		"template":        "template /t",
	}
	for _, b := range Builtins() {
		if b.Key == "" {
			continue
		}
		if _, ok := needsFlag[b.Key]; !ok {
			t.Errorf("%q key %q has no needs mapping", b.Form, b.Key)
		}
		src, ok := samples[b.Form]
		if !ok {
			t.Errorf("%q key %q has no needs sample", b.Form, b.Key)
			continue
		}
		keys := []string{b.Key}
		if info, prob := classify(strings.Fields(src), map[string]*Fn{}); prob == nil && info.key != "" {
			keys = append(keys, info.key)
		}
		out := Needs(src, NeedsOptions{})
		for _, key := range keys {
			if _, ok := needsFlag[key]; !ok {
				t.Errorf("%q: classify key %q has no needs mapping", src, key)
			}
			found := false
			for _, r := range needsTableRows(t, out) {
				if r[0] == key {
					found = true
				}
			}
			if !found {
				t.Errorf("%q: needs does not report %q\n%s", src, key, out.Stdout)
			}
		}
	}
	// classify has no table: these are its keys, read from its branches.
	for _, key := range []string{"file.read", "ps", "journal", "unit.status", "unit.restart", "package.query",
		"package.install", "sysctl.get", "sysctl.set", "config.read", "file.write"} {
		if _, ok := needsFlag[key]; !ok {
			t.Errorf("classify key %q has no needs mapping", key)
		}
	}
	for key := range needsFlag {
		if key != "ps" && key != "journal" && needsFlag[key] == "" {
			t.Errorf("%q has no policy flag", key)
		}
	}
}
