package codes

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/codes.golden.json; the diff is then reviewed")

// The catalog is sorted, each code is one token used once, each area is a
// known one, and each exit is one sash returns.
func TestCatalogShape(t *testing.T) {
	token := regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	areas := map[string]bool{}
	for _, a := range Areas {
		areas[a] = true
	}
	exits := map[int]bool{0: true, 1: true, 2: true, 126: true, 127: true, 129: true, 130: true}
	seen := map[string]bool{}
	printed := map[string]string{}
	all := All()
	if !sort.SliceIsSorted(all, func(i, j int) bool { return all[i].Code < all[j].Code }) {
		t.Errorf("the catalog is not sorted by code")
	}
	for _, c := range all {
		if !token.MatchString(c.Code) || seen[c.Code] {
			t.Errorf("%q: not a token, or listed twice", c.Code)
		}
		seen[c.Code] = true
		if !areas[c.Area] {
			t.Errorf("%s: unknown area %q", c.Code, c.Area)
		}
		if !exits[c.Exit] {
			t.Errorf("%s: exit %d is not one sash returns", c.Code, c.Exit)
		}
		if c.Meaning == "" || c.Fix == "" || len(c.prints) == 0 {
			t.Errorf("%s: an empty field", c.Code)
		}
		for _, p := range c.prints {
			if other, dup := printed[p]; dup {
				t.Errorf("%q is printed for both %s and %s", p, other, c.Code)
			}
			printed[p] = c.Code
		}
	}
	// All returns a copy: a caller cannot edit the table.
	all[0].Fix = "changed"
	if All()[0].Fix == "changed" {
		t.Errorf("All hands out the table itself")
	}
}

// fixSubjects are how a fix sentence may begin: a person, the operator,
// or an admin decides or acts, or nothing is to be changed.
var fixSubjects = []string{"A person ", "The operator ", "A tenant admin ", "Nothing to change: "}

// fixBanned are phrases no fix may hold (agent frontier contract section
// 6, and this contract's fix-text rule): nothing tells a reader to weaken
// a control, widen a grant, or reach for another credential, channel, or
// override.
var fixBanned = []string{
	"run as root", "use sudo", "sudo ", "use an admin key", "admin key", "disable the", "disable ", "turn off", "turn it off", "switch off",
	"bypass", "widen the", "widen ", "drop the sandbox", "skip step-up", "skip the", "override", "--force", "force ",
	"set +o", "confirm off", "+o confirm", "+o plan", "another credential", "another key", "another token", "another channel",
	"another protocol", "another account", "use ssh", "ssh ", "scp ", "insecure", "skip-verify", "raise the", "loosen", "chmod", "as root",
	"ignore the", "retry without", "without confirm", "without the grant", "unset ", "remove the lock", "unlock",
}

// The fix-text rule: every sentence of every fix starts with a person, the
// operator, or an admin as its subject, or says nothing is to change; no
// fix holds a banned phrase or is over 512 bytes; and meaning never
// instructs at all.
func TestFixTextRule(t *testing.T) {
	if FixMax != 512 {
		t.Fatalf("FixMax = %d", FixMax)
	}
	check := func(code, fix string) (problems []string) {
		if len(fix) > FixMax {
			problems = append(problems, "over 512 bytes")
		}
		low := strings.ToLower(fix)
		for _, b := range fixBanned {
			if strings.Contains(low, b) {
				problems = append(problems, "banned phrase "+strconv.Quote(b))
			}
		}
		for _, sentence := range strings.SplitAfter(fix, ". ") {
			ok := false
			for _, s := range fixSubjects {
				ok = ok || strings.HasPrefix(sentence, s)
			}
			if !ok {
				problems = append(problems, "a sentence with no person, operator, or admin as its subject: "+strconv.Quote(sentence))
			}
		}
		if !strings.HasSuffix(fix, ".") {
			problems = append(problems, "not a sentence")
		}
		return problems
	}
	for _, c := range All() {
		for _, p := range check(c.Code, c.Fix) {
			t.Errorf("%s: fix %q: %s", c.Code, c.Fix, p)
		}
		low := strings.ToLower(c.Meaning)
		for _, b := range []string{"turn off", "bypass", "override", "sudo", "you should", "you must", "you can"} {
			if strings.Contains(low, b) {
				t.Errorf("%s: meaning %q holds %q", c.Code, c.Meaning, b)
			}
		}
	}
	// The lint itself refuses what it must.
	for _, bad := range []string{
		"Turn off confirm.",
		"A person can turn off confirm.",
		"Use another key.",
		"A person runs it with another credential.",
		"The operator can override the pin.",
		"Restart the unit.",
		"A person reads the code. Then widen the grant.",
		"a person reads the code.",
		"A person reads the code",
		"A person " + strings.Repeat("reads ", 100) + "the code.",
	} {
		if len(check("x", bad)) == 0 {
			t.Errorf("the lint accepts %q", bad)
		}
	}
}

// Every sentence of the catalog is pinned in a reviewed fixture: a change
// to any code, exit, area, meaning, or fix fails here until the fixture
// is regenerated (go test ./internal/codes -run TestCatalogFixture
// -update) and the diff reviewed. The gate is the reviewed text.
func TestCatalogFixture(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(All()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "codes.golden.json")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("the catalog differs from %s; regenerate it with -update and review the diff", path)
	}
	// The printed form carries the same entries, on one line.
	one, err := JSON()
	if err != nil || bytes.ContainsRune(one, '\n') {
		t.Fatalf("JSON: %v", err)
	}
	var a, b []Code
	if err := json.Unmarshal(one, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) || len(a) != len(All()) {
		t.Fatalf("%d printed, %d pinned, %d in the table", len(a), len(b), len(All()))
	}
	for i := range a {
		if a[i].Code != b[i].Code || a[i].Exit != b[i].Exit || a[i].Area != b[i].Area || a[i].Meaning != b[i].Meaning || a[i].Fix != b[i].Fix {
			t.Errorf("%s: printed and pinned differ", a[i].Code)
		}
	}
	sum, err := SHA256()
	if err != nil || len(sum) != 64 {
		t.Errorf("SHA256 %q %v", sum, err)
	}
}

// scanned is one fixed message found in the source: its text up to the
// first format verb or line end, and the exit the source gives it when
// it stands in a problem literal.
type scanned struct {
	msg  string
	exit int // -1 when the source does not say
	at   string
}

// notRefusals are fixed strings the scan meets in message positions that
// are not something sash prints as a refusal: each has its reason.
var notRefusals = map[string]string{
	"plan: would": "plan mode's report of what it would do, not a failure",
	"trace":       "set -x output",
	"typed":       "declare -p output",
	"empty":       "an internal error text resolvePath's callers replace with prefix_unset",
	"preview":     "the paint-only parent's error; never printed",
	"uuid":        "crypto/rand failing; surfaces inside a type expr message",
	"cannot read": "printed after the template_file or use prefix that names it",
	"too large":   "printed after the template_file or use prefix that names it",
	"null":        "a value's text",
	"%w: %v":      "a wrapper around a frame error",
	"<U+003C>":    "TermText's own escape",
}

var exitIdents = map[string]int{"exitType": 2, "exitDenied": 126, "exitUnknown": 127, "exitHangup": 129, "exitInterrupt": 130}

// leftLit is the string literal at the left of e, through + chains.
func leftLit(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		}
	case *ast.BinaryExpr:
		return leftLit(x.X)
	case *ast.ParenExpr:
		return leftLit(x.X)
	}
	return "", false
}

// scanMessages collects the fixed messages of one package directory: the
// text of every problem literal, TapeError code, errors.New and
// fmt.Errorf, write to a machine's stderr, fail or halt call, msg
// constant, and constant of an Error type, in its non-test files.
func scanMessages(t *testing.T, dir string) []scanned {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []scanned
	add := func(pos token.Pos, s string, exit int) {
		s, _, _ = strings.Cut(s, "\n")
		s = strings.TrimRight(s, " ")
		if len(s) < 3 || !strings.ContainsAny(s[:1], "abcdefghijklmnopqrstuvwxyz%<") {
			// Punctuation and fragments joined onto a message: not a
			// message's first words.
			return
		}
		out = append(out, scanned{msg: s, exit: exit, at: fset.Position(pos).String()})
	}
	for _, p := range pkgs {
		for _, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					name := ""
					if id, ok := x.Type.(*ast.Ident); ok {
						name = id.Name
					}
					if name == "problem" && len(x.Elts) == 2 {
						if s, ok := leftLit(x.Elts[1]); ok {
							exit := -1
							if id, ok := x.Elts[0].(*ast.Ident); ok {
								if e, known := exitIdents[id.Name]; known {
									exit = e
								}
							}
							add(x.Pos(), s, exit)
						}
					}
					if name == "TapeError" && len(x.Elts) >= 1 {
						if s, ok := leftLit(x.Elts[0]); ok {
							add(x.Pos(), s, 2)
						}
					}
				case *ast.CallExpr:
					fn := ""
					switch f := x.Fun.(type) {
					case *ast.SelectorExpr:
						fn = f.Sel.Name
						if id, ok := f.X.(*ast.Ident); ok {
							fn = id.Name + "." + fn
						}
					case *ast.Ident:
						fn = f.Name
					}
					if fn == "errors.New" || fn == "fmt.Errorf" || strings.HasSuffix(fn, "WriteString") || fn == "fail" || fn == "halt" {
						for _, a := range x.Args {
							if s, ok := leftLit(a); ok {
								add(x.Pos(), s, -1)
								break
							}
						}
					}
				case *ast.ValueSpec:
					for i, nm := range x.Names {
						// A constant of an error type (grant.Error) is a code
						// the package returns.
						typed := false
						if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Error" {
							typed = true
						}
						if (typed || strings.HasPrefix(nm.Name, "msg")) && i < len(x.Values) {
							if s, ok := leftLit(x.Values[i]); ok {
								add(x.Pos(), s, -1)
							}
						}
					}
				}
				return true
			})
		}
	}
	return out
}

// The scan: every fixed message of the language, the grant, the session,
// and the wire has an entry, and where the source states its exit, the
// entry's exit is that one. Adding a refusal without cataloguing it fails
// here.
func TestEveryFixedMessageIsCatalogued(t *testing.T) {
	total := 0
	used := map[string]bool{}
	for _, dir := range []string{"../lang", "../grant", "../session", "../proto"} {
		msgs := scanMessages(t, dir)
		if dir == "../lang" && len(msgs) < 300 {
			t.Fatalf("%s: only %d messages found; the scan no longer sees the source", dir, len(msgs))
		}
		for _, m := range msgs {
			total++
			if _, skip := notRefusals[m.msg]; skip {
				used[m.msg] = true
				continue
			}
			c, ok := Find(m.msg)
			if !ok {
				t.Errorf("%s: %q has no catalog entry", m.at, m.msg)
				continue
			}
			if m.exit >= 0 && c.Exit != m.exit {
				t.Errorf("%s: %q exits %d in the source, the catalog says %s exits %d", m.at, m.msg, m.exit, c.Code, c.Exit)
			}
		}
	}
	for s := range notRefusals {
		if !used[s] {
			t.Errorf("notRefusals holds %q, which the source no longer has", s)
		}
	}
	// Every entry is something the source prints: no entry for a message
	// that does not exist.
	var all []scanned
	for _, dir := range []string{"../lang", "../grant", "../session", "../proto", "../parent"} {
		all = append(all, scanMessages(t, dir)...)
	}
	for _, c := range All() {
		found := false
		for _, m := range all {
			if got, ok := Find(m.msg); ok && got.Code == c.Code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: nothing in the source prints %q", c.Code, c.prints)
		}
	}
}

// The words the contract names resolve to the entries it means.
func TestFindNamesTheRightEntry(t *testing.T) {
	for msg, want := range map[string]string{
		"denied file.write":                    "denied",
		"declined unit.restart":                "declined",
		"command_from_value":                   "command_from_value",
		"prefix_unset":                         "prefix_unset",
		"atomic_irreversible: term":            "atomic_irreversible",
		"atomic: rolled back":                  "atomic_rolled_back",
		"atomic unavailable: agent too old":    "atomic_unavailable",
		"replay_diverged 4":                    "replay_diverged",
		"replay_secret 2":                      "replay_secret",
		"grant_bad_sig":                        "grant_bad_sig",
		"confirm needs a terminal":             "confirm_no_terminal",
		"confirm is locked":                    "confirm_locked",
		"term needs a terminal":                "term_no_terminal",
		"term prompt: end of input":            "term_eof",
		"term: interrupted":                    "term_interrupted",
		"term inside par":                      "term_inside_par",
		"usage term: question over 1024 bytes": "usage",
		"unknown scp":                          "unknown",
		"type |>":                              "type",
		"parse: term inside par":               "parse",
		"timeout":                              "timeout",
		"refused file.write: bad_mode (the agent refused the file mode)": "refused",
		"a secret cannot leave sash":                                     "secret_cannot_leave",
		"parent file.read":                                               "parent",
		"agent too old for file.write mode":                              "mode_unsupported",
	} {
		if c, ok := Find(msg); !ok || c.Code != want {
			t.Errorf("Find(%q) = %q %v, want %q", msg, c.Code, ok, want)
		}
	}
	for _, msg := range []string{"", "typeahead", "denying", "unknownx", "evil\x1b[2J", "timeouts"} {
		if c, ok := Find(msg); ok {
			t.Errorf("Find(%q) = %q", msg, c.Code)
		}
	}
}
