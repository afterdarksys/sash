package lang

import (
	"fmt"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type failParent struct{ n int }

func (f *failParent) Do(parent.Verb) (parent.Result, error) {
	f.n++
	return parent.Result{}, fmt.Errorf("no")
}

type listParent struct {
	calls []parent.Verb
	fail  bool
	rows  map[string][]parent.Row
}

func (p *listParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls = append(p.calls, v)
	if p.fail {
		return parent.Result{}, fmt.Errorf("no")
	}
	if len(v.Argv) > 0 && v.Argv[0] == "ls" {
		return parent.Result{Kind: parent.KindRows, Rows: p.rows[v.Path]}, nil
	}
	if len(v.Argv) > 0 && v.Argv[0] == "cat" {
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte(v.Path + "\n")}, nil
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

type depthParent struct{ n int }

func (d *depthParent) Do(parent.Verb) (parent.Result, error) {
	d.n++
	if d.n > 20 {
		return parent.Result{}, fmt.Errorf("loop")
	}
	return parent.Result{Kind: parent.KindRows, Rows: []parent.Row{{"name": "d", "type": "dir"}}}, nil
}

type recParent struct {
	calls []parent.Verb
}

func (r *recParent) Do(v parent.Verb) (parent.Result, error) {
	r.calls = append(r.calls, v)
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
}

func readGrant(roots ...string) grant.Grant {
	return grant.Grant{Entitlements: []string{"file.read"}, FileRead: roots}
}

func lsCount(calls []parent.Verb) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "ls" {
			n++
		}
	}
	return n
}

func catCount(calls []parent.Verb) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "cat" {
			n++
		}
	}
	return n
}

func TestExpandLeavesAbbreviation(t *testing.T) {
	p := &failParent{}
	out, prob := Expand("unt ", grant.Grant{}, p, Options{})
	if prob != nil || out != "unt " || p.n != 0 {
		t.Fatalf("out %q prob %v calls %d", out, prob, p.n)
	}
}

func TestHistoryAndComments(t *testing.T) {
	p := &failParent{}
	got := Run("echo hello\n!!\n", grant.Grant{}, p, Options{})
	if got.Status != 0 || got.Stdout != "hello\nhello\n" || p.n != 0 {
		t.Fatalf("status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, p.n)
	}
	p = &failParent{}
	got = Run("!!", grant.Grant{}, p, Options{})
	if got.Status != exitType || got.Stderr != "type history\n" || p.n != 0 {
		t.Fatalf("status %d stderr %q calls %d", got.Status, got.Stderr, p.n)
	}
	p = &failParent{}
	got = Run("!!foo", grant.Grant{}, p, Options{})
	if got.Status != exitType || !strings.Contains(got.Stderr, "lex: bare !") || p.n != 0 {
		t.Fatalf("status %d stderr %q calls %d", got.Status, got.Stderr, p.n)
	}
	p = &failParent{}
	got = Run("echo hi # !!\n", grant.Grant{}, p, Options{})
	if got.Status != 0 || got.Stdout != "hi\n" || p.n != 0 {
		t.Fatalf("comment bang status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	p = &failParent{}
	got = Run("echo hi # {1..10000}\n", grant.Grant{}, p, Options{})
	if got.Status != 0 || got.Stdout != "hi\n" || p.n != 0 {
		t.Fatalf("comment brace status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	p = &failParent{}
	got = Run("echo hi # *\n", grant.Grant{}, p, Options{})
	if got.Status != 0 || got.Stdout != "hi\n" || p.n != 0 {
		t.Fatalf("comment glob status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	hist := []string{"echo hello"}
	out, prob := Expand("!!", grant.Grant{}, &failParent{}, Options{History: hist})
	if prob != nil || out != "echo hello" || len(hist) != 1 || hist[0] != "echo hello" {
		t.Fatalf("out %q prob %v hist %v", out, prob, hist)
	}
	got = Run("echo !$", grant.Grant{}, &failParent{}, Options{History: []string{"echo hello world"}})
	if got.Status != 0 || got.Stdout != "world\n" {
		t.Fatalf("dollar status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	got = Run("echo !$", grant.Grant{}, &failParent{}, Options{History: []string{"echo $HOME"}})
	if got.Status != 0 || got.Stdout != "$HOME\n" {
		t.Fatalf("quoted dollar status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
}

func TestBraceExpansion(t *testing.T) {
	cases := []struct {
		src, stdout string
	}{
		{"echo {a,b}", "a b\n"},
		{"echo {1..3}", "1 2 3\n"},
		{"echo '{a,b}'", "{a,b}\n"},
		{`echo "{a,b}"`, "{a,b}\n"},
		{"echo {1..5..2}", "1 3 5\n"},
		{"echo {3..1}", "3 2 1\n"},
		{"echo {a..c}", "a b c\n"},
		{"echo {x}", "{x}\n"},
		{"echo pre{a,b}post", "preapost prebpost\n"},
		{"echo {{a,b},c}", "a c b c\n"},
		{"echo {5..1..2}", "5 3 1\n"},
		{"echo {1..5..-2}", "1 3 5\n"},
		{"echo ngin{x}", "nginx\n"},
		{"echo {1..1}", "1\n"},
		{"echo pre{a}post", "preapost\n"},
	}
	for _, tc := range cases {
		p := &failParent{}
		got := Run(tc.src, grant.Grant{}, p, Options{})
		if got.Status != 0 || got.Stdout != tc.stdout || p.n != 0 {
			t.Fatalf("%s status %d stdout %q stderr %q calls %d", tc.src, got.Status, got.Stdout, got.Stderr, p.n)
		}
	}
	p := &failParent{}
	got := Run("echo {1..10000}", grant.Grant{}, p, Options{})
	if got.Status != exitType || got.Stderr != "type brace\n" || p.n != 0 {
		t.Fatalf("huge status %d stderr %q calls %d", got.Status, got.Stderr, p.n)
	}
	p = &failParent{}
	got = Run("echo {1..65}", grant.Grant{}, p, Options{})
	if got.Status != exitType || got.Stderr != "type brace\n" || p.n != 0 {
		t.Fatalf("65 status %d stderr %q calls %d", got.Status, got.Stderr, p.n)
	}
	p = &failParent{}
	got = Run("echo {1..64}", grant.Grant{}, p, Options{})
	if got.Status != 0 || p.n != 0 || !strings.HasPrefix(got.Stdout, "1 ") || !strings.HasSuffix(got.Stdout, "64\n") {
		t.Fatalf("64 status %d stdout prefix %q stderr %q", got.Status, got.Stdout[:min(20, len(got.Stdout))], got.Stderr)
	}
	p = &failParent{}
	got = Run("echo {1..1..0}", grant.Grant{}, p, Options{})
	if got.Status != exitType || got.Stderr != "type brace\n" || p.n != 0 {
		t.Fatalf("step status %d stderr %q", got.Status, got.Stderr)
	}
	out, prob := Expand("echo {a,b}|c", grant.Grant{}, &failParent{}, Options{})
	if prob != nil || out != "echo a|c b|c" {
		t.Fatalf("pipe brace out %q prob %v", out, prob)
	}
	out, prob = Expand("echo pre{01..02}{a,b}", grant.Grant{}, &failParent{}, Options{})
	if prob != nil || out != "echo pre{01..02}a pre{01..02}b" {
		t.Fatalf("skip out %q prob %v", out, prob)
	}
	body := strings.Repeat("a", 20000)
	alts := make([]string, 64)
	for i := range alts {
		alts[i] = body
	}
	p = &failParent{}
	got = Run("echo {"+strings.Join(alts, ",")+"}", grant.Grant{}, p, Options{})
	if got.Status != exitType || got.Stderr != "type brace\n" || p.n != 0 {
		t.Fatalf("oversize status %d stderr %q calls %d", got.Status, got.Stderr, p.n)
	}
}

func TestBraceRunsExpandedMutation(t *testing.T) {
	p := &recParent{}
	g := grant.Grant{Entitlements: []string{"unit.restart"}}
	got := Run("unit restart ngin{x}", g, p, Options{})
	if got.Status != 0 || len(p.calls) != 1 {
		t.Fatalf("status %d calls %d stderr %q", got.Status, len(p.calls), got.Stderr)
	}
	if strings.Join(p.calls[0].Argv, " ") != "unit restart nginx" || p.calls[0].Key != "unit.restart" {
		t.Fatalf("argv %v key %s", p.calls[0].Argv, p.calls[0].Key)
	}
	p = &recParent{}
	got = Run("!!", g, p, Options{History: []string{"unit restart nginx"}})
	if got.Status != 0 || len(p.calls) != 1 || strings.Join(p.calls[0].Argv, " ") != "unit restart nginx" {
		t.Fatalf("bang status %d argv %v stderr %q", got.Status, p.calls, got.Stderr)
	}
}

func logRows() []parent.Row {
	return []parent.Row{
		{"name": "a.log", "type": "file"},
		{"name": "b.log", "type": "file"},
		{"name": "../etc/passwd", "type": "file"},
		{"name": "/etc/passwd", "type": "file"},
		{"name": "link", "type": "symlink"},
		{"name": "foo..bar", "type": "file"},
	}
}

func TestGlobCatAndDrops(t *testing.T) {
	p := &listParent{rows: map[string][]parent.Row{"/var/log": logRows()}}
	g := readGrant("/var/log")
	out, prob := Expand("cat /var/log/*.log", g, p, Options{})
	if prob != nil || out != "cat /var/log/a.log /var/log/b.log" || lsCount(p.calls) != 1 {
		t.Fatalf("out %q prob %v calls %d", out, prob, lsCount(p.calls))
	}
	if p.calls[0].Key != "file.read" || strings.Join(p.calls[0].Argv, " ") != "ls /var/log" || p.calls[0].Path != "/var/log" {
		t.Fatalf("ls %+v", p.calls[0])
	}
	out2, prob := Expand(out, g, p, Options{})
	if prob != nil || out2 != out || lsCount(p.calls) != 1 {
		t.Fatalf("second out %q ls %d", out2, lsCount(p.calls))
	}
	got := Run(out, g, p, Options{})
	if got.Status != 0 || got.Stdout != "/var/log/a.log\n/var/log/b.log\n" || lsCount(p.calls) != 1 || catCount(p.calls) != 2 {
		t.Fatalf("status %d stdout %q ls %d cat %d stderr %q", got.Status, got.Stdout, lsCount(p.calls), catCount(p.calls), got.Stderr)
	}
}

func TestGlobStarStar(t *testing.T) {
	p := &listParent{rows: map[string][]parent.Row{
		"/var/log": {
			{"name": "a.log", "type": "file"},
			{"name": "b.log", "type": "file"},
			{"name": "sub", "type": "dir"},
			{"name": "link", "type": "symlink"},
		},
		"/var/log/sub": {
			{"name": "b.log", "type": "file"},
			{"name": "c.txt", "type": "file"},
		},
	}}
	got := Run("echo /var/log/**/*.log", readGrant("/var/log"), p, Options{})
	want := "/var/log/a.log /var/log/b.log /var/log/sub/b.log\n"
	if got.Status != 0 || got.Stdout != want || lsCount(p.calls) != 2 {
		t.Fatalf("status %d stdout %q ls %d stderr %q", got.Status, got.Stdout, lsCount(p.calls), got.Stderr)
	}
}

func TestGlobDeniedAndClosed(t *testing.T) {
	rows := map[string][]parent.Row{"/var/log": logRows()}
	p := &listParent{rows: rows}
	star := grant.Grant{Entitlements: []string{"*", "file.read"}, FileRead: []string{"/var/log"}}
	got := Run("echo /var/log/*.log", star, p, Options{})
	if got.Status != exitDenied || got.Stderr != "denied file.read\n" || len(p.calls) != 0 {
		t.Fatalf("star status %d stderr %q calls %d", got.Status, got.Stderr, len(p.calls))
	}
	p = &listParent{rows: rows}
	got = Run("echo /var/log/*.log", grant.Grant{FileRead: []string{"/var/log"}}, p, Options{})
	if got.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("empty status %d stderr %q calls %d", got.Status, got.Stderr, len(p.calls))
	}
	p = &listParent{rows: rows, fail: true}
	got = Run("echo /var/log/*.log", readGrant("/var/log"), p, Options{})
	if got.Status != 1 || got.Stderr != "parent file.read\n" || lsCount(p.calls) != 1 || catCount(p.calls) != 0 {
		t.Fatalf("fail status %d stderr %q ls %d", got.Status, got.Stderr, lsCount(p.calls))
	}
	p = &listParent{rows: rows}
	got = Run("cat /etc/*", readGrant("/var/log"), p, Options{})
	if got.Status != exitDenied || got.Stderr != "denied file.read\n" || len(p.calls) != 0 {
		t.Fatalf("etc status %d stderr %q calls %d", got.Status, got.Stderr, len(p.calls))
	}
	p = &listParent{rows: rows}
	got = Run("echo *.log", readGrant("/var/log"), p, Options{})
	if got.Status != exitType || got.Stderr != "prefix_unset\n" || len(p.calls) != 0 {
		t.Fatalf("rel status %d stderr %q calls %d", got.Status, got.Stderr, len(p.calls))
	}
	p = &listParent{}
	got = Run("echo /var/*/x", grant.Grant{}, p, Options{})
	if got.Status != exitType || got.Stderr != "type glob\n" || len(p.calls) != 0 {
		t.Fatalf("shape status %d stderr %q calls %d", got.Status, got.Stderr, len(p.calls))
	}
	p = &listParent{rows: map[string][]parent.Row{"/var/log": nil}}
	g := grant.Grant{Entitlements: []string{"unit.restart", "file.read"}, FileRead: []string{"/var/log"}}
	got = Run("unit restart *", g, p, Options{Prefix: "/var/log"})
	if got.Status != exitType || got.Stderr != "type glob\n" || lsCount(p.calls) != 1 {
		t.Fatalf("nomatch status %d stderr %q calls %+v", got.Status, got.Stderr, p.calls)
	}
	for _, c := range p.calls {
		if c.Key == "unit.restart" {
			t.Fatal("glob ran the mutation")
		}
	}
}

func TestGlobCaps(t *testing.T) {
	rows := make([]parent.Row, 1000)
	for i := range rows {
		rows[i] = parent.Row{"name": fmt.Sprintf("f%04d", i), "type": "file"}
	}
	p := &listParent{rows: map[string][]parent.Row{"/var/log": rows}}
	got := Run("echo /var/log/*", readGrant("/var/log"), p, Options{})
	if got.Status != exitType || got.Stderr != "type glob\n" || lsCount(p.calls) != 1 || catCount(p.calls) != 0 {
		t.Fatalf("1000 status %d stderr %q ls %d", got.Status, got.Stderr, lsCount(p.calls))
	}
	rows = make([]parent.Row, 33)
	for i := range rows {
		rows[i] = parent.Row{"name": fmt.Sprintf("f%02d", i), "type": "file"}
	}
	p = &listParent{rows: map[string][]parent.Row{"/var/log": rows}}
	got = Run("echo /var/log/*", readGrant("/var/log"), p, Options{})
	if got.Status != exitType || got.Stderr != "type glob\n" || lsCount(p.calls) != 1 {
		t.Fatalf("33 status %d stderr %q ls %d", got.Status, got.Stderr, lsCount(p.calls))
	}
	rows = rows[:32]
	p = &listParent{rows: map[string][]parent.Row{"/var/log": rows}}
	got = Run("echo /var/log/*", readGrant("/var/log"), p, Options{})
	if got.Status != 0 || lsCount(p.calls) != 1 {
		t.Fatalf("32 status %d stderr %q ls %d", got.Status, got.Stderr, lsCount(p.calls))
	}
	p = &listParent{rows: map[string][]parent.Row{"/var/log": {{"name": "a'b", "type": "file"}}}}
	got = Run("echo /var/log/*", readGrant("/var/log"), p, Options{})
	if got.Status != exitType || got.Stderr != "type glob\n" || lsCount(p.calls) != 1 {
		t.Fatalf("quote status %d stderr %q", got.Status, got.Stderr)
	}
}

func TestGlobDepthCap(t *testing.T) {
	p := &depthParent{}
	got := Run("echo /var/**", readGrant("/var"), p, Options{})
	if got.Status != 0 || p.n != 8 {
		t.Fatalf("status %d calls %d stdout %q stderr %q", got.Status, p.n, got.Stdout, got.Stderr)
	}
	if strings.Count(got.Stdout, "/var/d") != 8 {
		t.Fatalf("stdout %q", got.Stdout)
	}
}

func TestHereString(t *testing.T) {
	p := &failParent{}
	got := Run("cat <<< hello", grant.Grant{}, p, Options{})
	if got.Status != 0 || got.Stdout != "hello\n" || p.n != 0 {
		t.Fatalf("here status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, p.n)
	}
	p = &failParent{}
	got = Run(`egrep sshd <<< "hello sshd"`, grant.Grant{}, p, Options{})
	if got.Status != 0 || got.Stdout != "hello sshd\n" || p.n != 0 {
		t.Fatalf("egrep status %d stdout %q stderr %q calls %d", got.Status, got.Stdout, got.Stderr, p.n)
	}
	for _, src := range []string{"cat < /no/such <<< hello", "cat <<< hello < /no/such"} {
		p = &failParent{}
		got = Run(src, grant.Grant{}, p, Options{})
		if got.Status != exitType || got.Stderr != "usage <\n" || p.n != 0 {
			t.Fatalf("%s status %d stderr %q calls %d", src, got.Status, got.Stderr, p.n)
		}
	}
	p = &failParent{}
	got = Run("cat << hello", grant.Grant{}, p, Options{})
	if got.Status != exitType || !strings.Contains(got.Stderr, "parse: redirect needs a path") || p.n != 0 {
		t.Fatalf("two status %d stderr %q calls %d", got.Status, got.Stderr, p.n)
	}
}
