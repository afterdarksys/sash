package lang

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// provParent answers ps with rows and every file read with a body
// naming its path; use files are fn and declare -r only.
type provParent struct{}

func (provParent) Do(v parent.Verb) (parent.Result, error) {
	switch {
	case v.Key == "ps":
		return parent.Result{Kind: parent.KindRows, Cols: []string{"user", "cmd"}, Rows: []parent.Row{{"user": "root", "cmd": "sshd"}}}, nil
	case v.Path == "/var/log/lib.sash":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("fn readb() {\n  cat /var/log/b\n}\n")}, nil
	case v.Path == "/var/log/s.yaml":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("a: 1\n")}, nil
	case v.Path == "/var/log/t":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("a={{ $a }}\n")}, nil
	case v.Key == "file.read":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("body " + v.Path + "\n")}, nil
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

func provGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"ps", "file.read"}, FileRead: []string{"/var/log"}}
}

// originRows runs src, then origin name |> to json-free rows: each row
// as "key target line".
func originRows(t *testing.T, src, name string) ([]string, Outcome) {
	t.Helper()
	st := NewState()
	st.verbMax = 30000
	out := Run(src, provGrant(), provParent{}, Options{State: st})
	if out.Status != 0 {
		return nil, out
	}
	o := Run("origin "+name, provGrant(), provParent{}, Options{State: st})
	var rows []string
	for _, l := range strings.Split(strings.TrimSpace(o.Stdout), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 {
			rows = append(rows, strings.Join(f[1:4], " "))
		} else if len(f) > 0 {
			rows = append(rows, strings.Join(f, " "))
		}
	}
	return rows, o
}

func hasOriginRow(rows []string, want string) bool {
	for _, r := range rows {
		if r == want || (want == "ps" && strings.HasPrefix(r, "ps ")) {
			return true
		}
	}
	return false
}

func TestOriginLineage(t *testing.T) {
	cases := []struct {
		name, src, of string
		want          []string
	}{
		{"let from a verb", "let p = (ps)", "p", []string{"ps"}},
		{"let from a read", "let a = (cat /var/log/a)", "a", []string{"file.read /var/log/a 1"}},
		{"a word over names", "let a = (cat /var/log/a)\nlet b = (cat /var/log/b)\nlet c = \"$a-$b\"", "c", []string{"file.read /var/log/a 1", "file.read /var/log/b 2"}},
		{"an expression", "let a = (cat /var/log/a)\nlet n = [ len($a) ]", "n", []string{"file.read /var/log/a 1"}},
		{"a lone reference", "let a = (cat /var/log/a)\nlet b = $a", "b", []string{"file.read /var/log/a 1"}},
		{"declare", "let a = (cat /var/log/a)\ndeclare d = $a", "d", []string{"file.read /var/log/a 1"}},
		{"declare -i", "let a = (cat /var/log/a)\ndeclare -i n = [ len($a) ]", "n", []string{"file.read /var/log/a 1"}},
		{"a for name", "for r in ps { let x = $r.cmd }", "x", []string{"ps"}},
		{"a for name over a value", "let a = [ [1, 2] ]\nlet l = (cat /var/log/a)\nfor i in [ [$l] ] { let y = $i }", "y", []string{"file.read /var/log/a 2"}},
		{"with", "let a = (cat /var/log/a)\nwith w = $a { let z = $w }", "z", []string{"file.read /var/log/a 1"}},
		{"a catch name", "try { cat /var/log/a; nosuch } catch e { let m = $e }", "m", []string{"file.read /var/log/a 1"}},
		{"a fn's return", "fn f(p) {\n  cat /var/log/b\n}\nlet a = (cat /var/log/a)\nlet r = (f $a)", "r", []string{"file.read /var/log/a 4", "file.read /var/log/b 2"}},
		{"a fn parameter", "fn g(p) {\n  let q = $p\n}\nlet a = (cat /var/log/a)\ng $a", "q", []string{"file.read /var/log/a 4"}},
		{"a filter", "let a = (cat /var/log/a)\nlet f = (ps |> filter [ $it.cmd != $a ])", "f", []string{"file.read /var/log/a 1", "ps"}},
		{"a template", "let t = (cat /var/log/s.yaml | from yaml | template /var/log/t)", "t", []string{"file.read /var/log/s.yaml 1", "file.read /var/log/t 1"}},
		{"a use file's fn", "use /var/log/lib.sash\nlet u = (readb)", "u", []string{"file.read /var/log/b 2"}},
	}
	for _, c := range cases {
		rows, out := originRows(t, c.src, c.of)
		if out.Status != 0 {
			t.Errorf("%s: %+v", c.name, out)
			continue
		}
		for _, w := range c.want {
			if !hasOriginRow(rows, w) {
				t.Errorf("%s: no %q in %q", c.name, w, rows)
			}
		}
		if c.want == nil && len(rows) > 0 && rows[0] != "" {
			t.Errorf("%s: rows %q", c.name, rows)
		}
	}
	// The use file's origin names the file, not the script.
	st := NewState()
	Run("use /var/log/lib.sash\nlet u = (readb)", provGrant(), provParent{}, Options{State: st})
	o := Run("origin u", provGrant(), provParent{}, Options{State: st})
	if !strings.Contains(o.Stdout, "/var/log/lib.sash") {
		t.Fatalf("use origin %q", o.Stdout)
	}
	// A fn defined on an earlier line keeps that line's numbers.
	st = NewState()
	Run("fn h() {\n\n  cat /var/log/h\n}", provGrant(), provParent{}, Options{State: st})
	Run("let v = (h)", provGrant(), provParent{}, Options{State: st})
	o = Run("origin v", provGrant(), provParent{}, Options{State: st})
	if !strings.Contains(o.Stdout, "/var/log/h  3") {
		t.Fatalf("earlier line %q", o.Stdout)
	}
}

// The hash is the sha256 of the response bytes.
func TestOriginHash(t *testing.T) {
	st := NewState()
	Run("let a = (cat /var/log/a)", provGrant(), provParent{}, Options{State: st})
	o := Run("origin a", provGrant(), provParent{}, Options{State: st})
	h := sha256.Sum256([]byte("body /var/log/a\n"))
	if !strings.Contains(o.Stdout, hex.EncodeToString(h[:])) || !strings.HasPrefix(o.Stdout, "script  file.read") {
		t.Fatalf("%q", o.Stdout)
	}
}

// Past 16 origins a value keeps 15 and one overflow row; past 10,000 a
// State hands out overflow. Neither fails the run.
func TestOriginCaps(t *testing.T) {
	var b strings.Builder
	var refs []string
	for i := 0; i < 20; i++ {
		b.WriteString("let v" + itoa(i) + " = (cat /var/log/f" + itoa(i) + ")\n")
		refs = append(refs, "$v"+itoa(i))
	}
	b.WriteString("let all = \"" + strings.Join(refs, "") + "\"\n")
	rows, out := originRows(t, b.String(), "all")
	if out.Status != 0 || len(rows) != provPerValue || rows[len(rows)-1] != "overflow truncated" {
		t.Fatalf("value cap: %q %+v", rows, out)
	}
	rows, out = originRows(t, "for i in [ 1..10005 ] { let p = (ps) }", "p")
	if out.Status != 0 || len(rows) != 1 || rows[0] != "overflow truncated" {
		t.Fatalf("state cap: %q %+v", rows, out)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// A secret's rows print hash ***; its target is shown, never secret.
func TestOriginSecretHidesHash(t *testing.T) {
	st := NewState()
	Run("declare -s tok = (cat /var/log/token)\nlet d = [ len($tok) ]", provGrant(), provParent{}, Options{State: st})
	for _, name := range []string{"tok", "d"} {
		o := Run("origin "+name, provGrant(), provParent{}, Options{State: st})
		h := sha256.Sum256([]byte("body /var/log/token\n"))
		if !strings.Contains(o.Stdout, "/var/log/token") || !strings.Contains(o.Stdout, "***") || strings.Contains(o.Stdout, hex.EncodeToString(h[:])) {
			t.Fatalf("%s: %q", name, o.Stdout)
		}
	}
}

// A hostile target is escaped on the terminal and in JSON.
func TestOriginEscapesTargets(t *testing.T) {
	st := NewState()
	Run("let a = (cat '/var/log/x\x1b]0;pwn\x07\u202e')", provGrant(), provParent{}, Options{State: st})
	for _, src := range []string{"origin a", "origin a |> to json"} {
		o := Run(src, provGrant(), provParent{}, Options{State: st})
		if o.Status != 0 || strings.ContainsAny(o.Stdout, "\x1b\x07\u202e") || !strings.Contains(o.Stdout, `x1b]0;pwn`) {
			t.Fatalf("%s: %q", src, o.Stdout)
		}
	}
	o := Run("origin a |> to json", provGrant(), provParent{}, Options{State: st})
	for _, col := range originCols {
		if !strings.Contains(o.Stdout, `"`+col+`"`) {
			t.Fatalf("json %q", o.Stdout)
		}
	}
}

// origin takes a name: an unknown one is exit 2 unset, origin $x is
// usage, a literal value has no rows.
func TestOriginUsage(t *testing.T) {
	st := NewState()
	Run("let a = (cat /var/log/a)\nlet lit = hello", provGrant(), provParent{}, Options{State: st})
	for src, want := range map[string]string{
		"origin nosuch": "unset nosuch",
		"origin $a":     "usage origin name",
		"origin":        "usage origin name",
		"origin a b":    "usage origin name",
	} {
		o := Run(src, provGrant(), provParent{}, Options{State: st})
		if o.Status != exitType || !strings.Contains(o.Stderr, want) {
			t.Errorf("%q: %+v", src, o)
		}
	}
	if o := Run("origin lit", provGrant(), provParent{}, Options{State: st}); o.Status != 0 || o.Stdout != "" {
		t.Fatalf("literal %+v", o)
	}
	// Static readers see a pure builtin with no needs.
	if out := Needs("let a = (ps)\norigin a", NeedsOptions{}); strings.Contains(out.Stdout, "origin") || !strings.Contains(out.Stdout, "ps") {
		t.Fatalf("needs %+v", out)
	}
	if out := Lint("let a = (ps)\norigin a"); out.Status != 0 {
		t.Fatalf("lint %+v", out)
	}
	if out := Why("origin a", provGrant(), Options{}); out.Status != 0 || !strings.Contains(out.Stdout, "in_process") {
		t.Fatalf("why %+v", out)
	}
}

// Provenance never reaches a log line.
func TestOriginNotInLog(t *testing.T) {
	st := NewState()
	out := Run("let a = (cat /var/log/a)\nlog info msg [ {\"v\": $a} ]", provGrant(), provParent{}, Options{State: st})
	h := sha256.Sum256([]byte("body /var/log/a\n"))
	if strings.Contains(out.Stderr, hex.EncodeToString(h[:])) || strings.Contains(out.Stderr, "file.read") {
		t.Fatalf("%q", out.Stderr)
	}
}

// A secret hides the hash of every origin it came from, however and
// whenever it is bound, of any response with the same hash, and of a
// later response holding its text; origin of the queried value need not
// be secret itself.
func TestOriginHidesSecretSources(t *testing.T) {
	h := sha256.Sum256([]byte("body /var/log/token\n"))
	leak := hex.EncodeToString(h[:])
	for name, c := range map[string]struct{ src, of string }{
		"late-bound":             {"let raw = (cat /var/log/token)\ndeclare -s tok = $raw", "raw"},
		"computed from":          {"let raw = (cat /var/log/token)\ndeclare -s tok = [ trim($raw) ]\nlet n = [ len($raw) ]", "n"},
		"a second read":          {"declare -s tok = (cat /var/log/token)\nlet other = (cat /var/log/token)", "other"},
		"an earlier second read": {"let a = (cat /var/log/token)\nlet b = (cat /var/log/token)\ndeclare -s tok = $a", "b"},
		"a let of a secret":      {"let raw = (cat /var/log/token)\ndeclare -s tok = $raw\nlet copy = $tok\nlet n = [ len($raw) ]", "n"},
		"a for over a secret":    {"let raw = (cat /var/log/token)\nfor s in [ [1] ] { declare -s tok = $raw }", "raw"},
		"inside a fn":            {"let raw = (cat /var/log/token)\nfn f(p) { declare -s tok = $raw }\nf x", "raw"},
	} {
		st := NewState()
		if out := Run(c.src, provGrant(), provParent{}, Options{State: st}); out.Status != 0 {
			t.Errorf("%s: %+v", name, out)
			continue
		}
		o := Run("origin "+c.of, provGrant(), provParent{}, Options{State: st})
		if strings.Contains(o.Stdout, leak) || !strings.Contains(o.Stdout, "***") {
			t.Errorf("%s: %q", name, o.Stdout)
		}
	}
	// A later response holding a bound secret's text is hidden too.
	st := NewState()
	Run("declare -s tok = /var/log/zzsecret\nlet other = (cat /var/log/zzsecret)", provGrant(), provParent{}, Options{State: st})
	o := Run("origin other", provGrant(), provParent{}, Options{State: st})
	if !strings.Contains(o.Stdout, "***") {
		t.Fatalf("text: %q", o.Stdout)
	}
	// Queried before the secret exists, hidden once it does.
	st = NewState()
	Run("let raw = (cat /var/log/token)", provGrant(), provParent{}, Options{State: st})
	if o := Run("origin raw", provGrant(), provParent{}, Options{State: st}); !strings.Contains(o.Stdout, leak) {
		t.Fatalf("before: %q", o.Stdout)
	}
	Run("declare -s tok = $raw", provGrant(), provParent{}, Options{State: st})
	if o := Run("origin raw", provGrant(), provParent{}, Options{State: st}); strings.Contains(o.Stdout, leak) {
		t.Fatalf("after: %q", o.Stdout)
	}
	// Unrelated origins keep their hash.
	Run("let a = (cat /var/log/a)", provGrant(), provParent{}, Options{State: st})
	ha := sha256.Sum256([]byte("body /var/log/a\n"))
	if o := Run("origin a", provGrant(), provParent{}, Options{State: st}); !strings.Contains(o.Stdout, hex.EncodeToString(ha[:])) {
		t.Fatalf("unrelated hidden: %q", o.Stdout)
	}
}
