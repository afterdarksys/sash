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
	"plan: would":         "plan mode's report of what it would do, not a failure",
	"trace":               "set -x output",
	"typed":               "declare -p output",
	"empty":               "an internal error text resolvePath's callers replace with prefix_unset",
	"preview":             "the paint-only parent's error; never printed",
	"uuid":                "crypto/rand failing; surfaces inside a type expr message",
	"cannot read":         "printed after the template_file or use prefix that names it",
	"too large":           "printed after the template_file or use prefix that names it",
	"null":                "a value's text",
	"%w: %v":              "a wrapper around a frame error",
	"<U+003C>":            "TermText's own escape",
	"connect: session on": "sash connect's line naming the host of an opened session",
	"<credential>":        "what sash connect prints in place of the token or the ticket",
	"no flush":            "the flush error a question maps to term needs a terminal; never printed",
	"no parent":           "the hand start's parent error; printed as parent <key>",
}

// notRefusal is the notRefusals key msg is, or begins as a word: a chain
// the scan renders whole ("trace <c>") is still the entry for "trace".
func notRefusal(msg string) (string, bool) {
	for key := range notRefusals {
		if msg == key || (strings.Contains(msg, "<c>") && strings.HasPrefix(msg, key+" ")) {
			return key, true
		}
	}
	return "", false
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

// chainLit is e as it prints, when its left end is a string literal:
// each literal of a + chain as written and any other operand as <c>, the
// catalog's stand-in for a name, so "send " + c + ": no receiver left" is
// seen whole, not as its first literal.
func chainLit(e ast.Expr) (string, bool) {
	if _, ok := leftLit(e); !ok {
		return "", false
	}
	var b strings.Builder
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				walk(x.X)
				walk(x.Y)
				return
			}
		case *ast.ParenExpr:
			walk(x.X)
			return
		case *ast.BasicLit:
			if s, err := strconv.Unquote(x.Value); err == nil && x.Kind == token.STRING {
				b.WriteString(s)
				return
			}
		}
		b.WriteString("<c>")
	}
	walk(e)
	return b.String(), true
}

// errStream reports a writer that is an error stream: &m.err (any
// selector err), os.Stderr, or an identifier named errOut or stderr.
func errStream(e ast.Expr) bool {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "os" {
			return x.Sel.Name == "Stderr"
		}
		return x.Sel.Name == "err"
	case *ast.Ident:
		return x.Name == "errOut" || x.Name == "stderr"
	}
	return false
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
						// Positional, or keyed in any order.
						status, msg := x.Elts[0], x.Elts[1]
						for _, el := range x.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "status" {
									status = kv.Value
								} else if ok && k.Name == "msg" {
									msg = kv.Value
								}
							}
						}
						if s, ok := chainLit(msg); ok {
							exit := -1
							if id, ok := status.(*ast.Ident); ok {
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
					// line's writeAll(w, []byte("...")).
					if fn == "writeAll" && len(x.Args) == 2 {
						if c, ok := x.Args[1].(*ast.CallExpr); ok && len(c.Args) == 1 {
							if s, ok := chainLit(c.Args[0]); ok {
								add(x.Pos(), s, -1)
							}
						}
					}
					// A print to an error stream: a machine's err, a
					// writer named for stderr, or os.Stderr.
					if (fn == "fmt.Fprintf" || fn == "fmt.Fprintln" || fn == "fmt.Fprint") && len(x.Args) > 1 && errStream(x.Args[0]) {
						if s, ok := chainLit(x.Args[1]); ok {
							add(x.Pos(), s, -1)
						}
					}
					if fn == "errors.New" || fn == "fmt.Errorf" || strings.HasSuffix(fn, "WriteString") || fn == "fail" || fn == "halt" {
						for _, a := range x.Args {
							if s, ok := chainLit(a); ok {
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
	for _, dir := range []string{"../lang", "../grant", "../session", "../proto", "../connect", "../line", "../../cmd/sash"} {
		msgs := scanMessages(t, dir)
		if dir == "../connect" && len(msgs) < 10 {
			t.Fatalf("%s: only %d messages found; the scan no longer sees the source", dir, len(msgs))
		}
		if dir == "../lang" && len(msgs) < 300 {
			t.Fatalf("%s: only %d messages found; the scan no longer sees the source", dir, len(msgs))
		}
		for _, m := range msgs {
			total++
			if key, skip := notRefusal(m.msg); skip {
				used[key] = true
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
	for _, dir := range []string{"../lang", "../grant", "../session", "../proto", "../parent", "../connect", "../line", "../../cmd/sash"} {
		all = append(all, scanMessages(t, dir)...)
	}
	// A code sent to sash connect is printed after one of its two fixed
	// prefixes, which the source must hold; connect's own tests print
	// every sent code through the real path.
	prefix := map[string]bool{}
	for _, m := range all {
		prefix[m.msg] = true
	}
	for _, c := range All() {
		found := false
		if c.Area == "connect" && Sent(c.Code) {
			head, _, _ := strings.Cut(c.prints[0], ": ")
			found = prefix[head+":"]
		}
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
		"send jobs: no receiver left":          "chan_no_receiver",
		"send jobs: buffers over 32 MiB":       "chan_send",
		"send c: value over 8 MiB":             "chan_send",
		"type |>":                              "type",
		"parse: term inside par":               "parse",
		"timeout":                              "timeout",
		"refused file.write: bad_mode (the agent refused the file mode)": "refused",
		"a secret cannot leave sash":                                     "secret_cannot_leave",
		"parent file.read":                                               "parent",
		"agent too old for file.write mode":                              "mode_unsupported",
		"usage: sash connect [--root] <host>":                            "connect_usage",
		"connect needs a terminal":                                       "connect_no_terminal",
		"connect: invalid SYSTEMAPI_URL":                                 "connect_invalid_url",
		"connect: token_file_rejected":                                   "token_file_rejected",
		"connect: no login; a person logs in with the systemapi CLI":     "connect_no_login",
		"connect: request_failed":                                        "request_failed",
		"connect refused: scope_denied: this login may not":              "scope_denied",
		"connect refused: grant_expired: the grant's expiry":             "connect_refused",
		"connect refused: evil (not in this sash's list)":                "connect_refused",
		"connect refused: unlisted":                                      "connect_refused",
		"connect: the server does not speak sah/3":                       "connect_not_sah3",
		"session ended: max_duration: the session reached":               "max_duration",
		"session ended: connection lost":                                 "connection_lost",
		"session ended: protocol":                                        "protocol",
		"connect: closed":                                                "connect_closed",
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

// A sent code is found only as one: a code of another area, or one sash
// connect prints itself, lends a sender no sentence. The grant refusals a
// host's agent reports are the grant area's own entries.
func TestConnectHoldsOnlySentCodes(t *testing.T) {
	for _, code := range []string{"ticket_invalid", "scope_denied", "forbidden", "not_found", "approval_required", "token_expired", "fresh_expired", "fresh_token_expired", "exited", "stopped", "grant_expired", "grant_bad_sig", "sash_missing"} {
		if c, ok := Connect(code); !ok || c.Code != code || c.Meaning == "" {
			t.Errorf("Connect(%q) = %v %v", code, c, ok)
		}
	}
	for _, code := range []string{"", "denied", "declined", "usage", "connect_usage", "connect_closed", "request_failed", "protocol", "connection_lost", "session_ended", "connect_refused", "evil", "TICKET_INVALID"} {
		if _, ok := Connect(code); ok {
			t.Errorf("Connect(%q) is found", code)
		}
	}
	// The sentences the contract fixes, exactly.
	for code, want := range map[string]string{
		"token_expired":       "your login expired; a person logs in again with the systemapi CLI",
		"fresh_token_expired": "a fresh login lasts 5 minutes; a person makes a new fresh login with the systemapi CLI just before opening a root session",
		"fresh_expired":       "a fresh login lasts 5 minutes; a person makes a new fresh login with the systemapi CLI just before opening a root session",
		"approval_required":   "a root session on this host needs an approval; an approver decides",
		"scope_denied":        "this login may not open that session; a tenant admin decides who may",
		"forbidden":           "this login may not open that session; a tenant admin decides who may",
	} {
		if c, _ := Connect(code); c.Meaning != want {
			t.Errorf("%s: %q", code, c.Meaning)
		}
	}
	for _, c := range All() {
		if c.Area == "connect" && Sent(c.Code) && c.Exit != 126 && c.Exit != 1 {
			t.Errorf("%s: exit %d", c.Code, c.Exit)
		}
	}
}

// Every error a pty_status may carry, as the control plane lists them
// (PTY_ERRORS in playground/fleet.js), is a sent code. The checkout is
// SASH_CONTROL_PLANE, or systemapi.io-frontier3 beside this repo; without
// one the test is skipped.
func TestConnectHoldsThePTYErrors(t *testing.T) {
	dir := os.Getenv("SASH_CONTROL_PLANE")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "systemapi.io-frontier3")
	}
	src, err := os.ReadFile(filepath.Join(dir, "playground", "fleet.js"))
	if err != nil {
		t.Skip("no control-plane checkout")
	}
	m := regexp.MustCompile(`(?s)const PTY_ERRORS = new Set\(\[(.*?)\]\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("PTY_ERRORS not found in fleet.js")
	}
	names := regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllSubmatch(m[1], -1)
	if len(names) < 18 {
		t.Fatalf("only %d PTY_ERRORS read", len(names))
	}
	for _, n := range names {
		if _, ok := Connect(string(n[1])); !ok {
			t.Errorf("PTY_ERRORS holds %q, which this build does not list", n[1])
		}
	}
}

// The scan sees each form a refusal is written in: a positional or keyed
// problem literal, a write or a formatted print to a machine's err, a
// print to os.Stderr or an errOut writer, and line's writeAll.
func TestScanSeesEveryForm(t *testing.T) {
	dir := t.TempDir()
	src := `package x

import (
	"fmt"
	"os"
)

type problem struct {
	status int
	msg    string
}

const exitType = 2

func f(m *machine, errOut *os.File) {
	_ = &problem{exitType, "novel positional"}
	_ = &problem{msg: "novel keyed", status: exitType}
	m.err.WriteString("novel write\n")
	fmt.Fprintf(&m.err, "novel fprintf %d\n", 1)
	fmt.Fprintln(os.Stderr, "novel stderr")
	fmt.Fprintln(errOut, "novel errout")
	_ = writeAll(errOut, []byte("novel writeall\n"))
	fmt.Fprintln(os.Stdout, "not an error stream")
}
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, m := range scanMessages(t, dir) {
		got[m.msg] = m.exit
	}
	for msg, exit := range map[string]int{"novel positional": 2, "novel keyed": 2, "novel write": -1, "novel fprintf %d": -1, "novel stderr": -1, "novel errout": -1, "novel writeall": -1} {
		if e, ok := got[msg]; !ok || e != exit {
			t.Errorf("%q: found %v exit %d, want exit %d", msg, ok, e, exit)
		}
	}
	if _, ok := got["not an error stream"]; ok {
		t.Errorf("a print to stdout was taken as a refusal")
	}
}
