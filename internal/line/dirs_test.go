package line

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

// dirsParent is the agent's history store for dirs and marks, or an old
// agent that denies them (old).
type dirsParent struct {
	old   bool
	dirs  map[string][2]int64
	marks map[string]string
	calls []string
	stats []string
}

func (p *dirsParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "history" {
		p.calls = append(p.calls, v.Argv[0]+" "+string(v.Body))
		switch v.Argv[0] {
		case "get":
			return parent.Result{Kind: parent.KindRows, Cols: []string{"line"}}, nil
		case "append":
			return parent.Result{Kind: parent.KindBytes}, nil
		}
		if p.old {
			return parent.Result{}, errors.New("denied")
		}
		switch v.Argv[0] {
		case "dirs":
			var rows []parent.Row
			for d, cl := range p.dirs {
				rows = append(rows, parent.Row{"dir": d, "count": strconv.FormatInt(cl[0], 10), "last": strconv.FormatInt(cl[1], 10)})
			}
			return parent.Result{Kind: parent.KindRows, Cols: []string{"dir", "count", "last"}, Rows: rows}, nil
		case "visit":
			c := p.dirs[string(v.Body)]
			p.dirs[string(v.Body)] = [2]int64{c[0] + 1, dirsNow().Unix()}
		case "marks":
			var rows []parent.Row
			for n, d := range p.marks {
				rows = append(rows, parent.Row{"name": n, "dir": d})
			}
			return parent.Result{Kind: parent.KindRows, Cols: []string{"name", "dir"}, Rows: rows}, nil
		case "mark":
			n, d, _ := strings.Cut(string(v.Body), "\t")
			p.marks[n] = d
		case "unmark":
			delete(p.marks, string(v.Body))
		}
		return parent.Result{Kind: parent.KindBytes}, nil
	}
	if v.Key == "file.read" && len(v.Argv) > 0 && v.Argv[0] == "stat" {
		p.stats = append(p.stats, v.Path)
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

func dirsGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"file.read"}, FileRead: []string{"/etc", "/srv"}}
}

func newDirsParent() *dirsParent {
	return &dirsParent{dirs: map[string][2]int64{}, marks: map[string]string{}}
}

func pinDirsClock(t *testing.T) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	dirsNow = func() time.Time { return now }
	t.Cleanup(func() { dirsNow = time.Now })
}

func TestZGoesToTheBestMatchInsideTheRoots(t *testing.T) {
	pinDirsClock(t)
	p := newDirsParent()
	p.dirs["/root/nginx"] = [2]int64{50, 1_800_000_000}    // outside the roots
	p.dirs["/etc/nginx"] = [2]int64{3, 1_800_000_000}      // recent
	p.dirs["/srv/nginx-old"] = [2]int64{5, 1_700_000_000}  // old: x0.25
	p.dirs["/etc/../root/x"] = [2]int64{99, 1_800_000_000} // not clean
	e := New(dirsGrant(), p)
	e.Feed([]byte("z ngi\n"))
	if e.opt.Prefix != "/etc/nginx" {
		t.Fatalf("prefix %q note %q", e.opt.Prefix, e.note)
	}
	if len(p.stats) != 1 || p.stats[0] != "/etc/nginx" {
		t.Fatalf("stats %v", p.stats)
	}
	// The move is a visit.
	if got := p.dirs["/etc/nginx"][0]; got != 4 {
		t.Fatalf("visit count %d", got)
	}
	e.Feed([]byte("z -l ngi\n"))
	if strings.Contains(e.note, "/root") || !strings.Contains(e.note, "/etc/nginx") || !strings.Contains(e.note, "/srv/nginx-old") {
		t.Fatalf("list %q", e.note)
	}
	// Parts match in order.
	e.Feed([]byte("z nginx etc\n"))
	if e.note != "z: no match" || e.Last.Status != 1 {
		t.Fatalf("order: %q %+v", e.note, e.Last)
	}
	e.Feed([]byte("z root\n"))
	if e.note != "z: no match" || e.opt.Prefix != "/etc/nginx" {
		t.Fatalf("outside roots: %q %q", e.note, e.opt.Prefix)
	}
}

func TestCdRecordsVisitsAndPrivateRecordsNothing(t *testing.T) {
	pinDirsClock(t)
	p := newDirsParent()
	e := New(dirsGrant(), p)
	e.Feed([]byte("cd /srv\n"))
	e.Feed([]byte("cd /srv\n")) // same prefix: no new visit
	e.Feed([]byte("cd /etc/nope_denied_by_nothing\n"))
	if p.dirs["/srv"][0] != 1 || p.dirs["/etc/nope_denied_by_nothing"][0] != 1 {
		t.Fatalf("dirs %v", p.dirs)
	}
	g := dirsGrant()
	g.AllowPrivate = true
	pp := newDirsParent()
	pe := New(g, pp)
	pe.Feed([]byte("cd /srv\nz srv\njump save a\njump list\n"))
	for _, c := range pp.calls {
		if !strings.HasPrefix(c, "append") {
			t.Fatalf("private called %q", c)
		}
	}
}

func TestJumpBookmarks(t *testing.T) {
	pinDirsClock(t)
	p := newDirsParent()
	e := New(dirsGrant(), p)
	e.Feed([]byte("cd /etc\njump save cfg\ncd /srv\njump cfg\n"))
	if e.opt.Prefix != "/etc" || p.marks["cfg"] != "/etc" {
		t.Fatalf("prefix %q marks %v note %q", e.opt.Prefix, p.marks, e.note)
	}
	e.Feed([]byte("jump list\n"))
	if !strings.Contains(e.note, "cfg  /etc") {
		t.Fatalf("list %q", e.note)
	}
	e.Feed([]byte("jump rm cfg\njump cfg\n"))
	if e.note != "jump: no bookmark cfg" {
		t.Fatalf("rm %q", e.note)
	}
	for _, bad := range []string{"jump save Bad", "jump save a/b", "jump save " + strings.Repeat("a", 33)} {
		e.Feed([]byte(bad + "\n"))
		if !strings.Contains(e.note, "bookmark names") {
			t.Errorf("%q: %q", bad, e.note)
		}
	}
	// A stored bookmark outside today's roots, or not clean, is not used.
	p.marks["old"] = "/root"
	p.marks["odd"] = "/etc/../root"
	e.Feed([]byte("jump old\n"))
	if e.note != "denied file.read" || e.opt.Prefix != "/etc" {
		t.Fatalf("outside: %q %q", e.note, e.opt.Prefix)
	}
	e.Feed([]byte("jump odd\n"))
	if !strings.Contains(e.note, "path: not clean") || e.opt.Prefix != "/etc" {
		t.Fatalf("not clean: %q %q", e.note, e.opt.Prefix)
	}
	// No prefix: nothing to save.
	e2 := New(dirsGrant(), newDirsParent())
	e2.Feed([]byte("jump save x\n"))
	if e2.note != "prefix_unset" {
		t.Fatalf("%q", e2.note)
	}
}

// An agent without the store: this session's own visits and bookmarks,
// with one notice.
func TestDirsFallBackWithoutTheStore(t *testing.T) {
	pinDirsClock(t)
	p := newDirsParent()
	p.old = true
	e := New(dirsGrant(), p)
	e.Feed([]byte("cd /srv/app\n"))
	if e.note != "z: history store unavailable, this session only" {
		t.Fatalf("notice %q", e.note)
	}
	e.Feed([]byte("cd /etc\nz app\n"))
	if e.opt.Prefix != "/srv/app" {
		t.Fatalf("prefix %q note %q", e.opt.Prefix, e.note)
	}
	e.Feed([]byte("jump save a\ncd /etc\njump a\n"))
	if e.opt.Prefix != "/srv/app" {
		t.Fatalf("jump %q note %q", e.opt.Prefix, e.note)
	}
}

func TestZAndJumpArePromptOnly(t *testing.T) {
	for _, src := range []string{"z etc", "jump cfg"} {
		out := lang.Run(src, dirsGrant(), newDirsParent(), lang.Options{State: lang.NewState()})
		if out.Status != 2 || !strings.Contains(out.Stderr, "prompt only") {
			t.Errorf("%q: %+v", src, out)
		}
	}
}

// A session that is recording refuses z and jump: its replay runs lines.
func TestZRefusedWhileRecording(t *testing.T) {
	g := dirsGrant()
	g.Entitlements = append(g.Entitlements, "file.write")
	g.FileWrite = []string{"/tmp"}
	st := lang.NewState()
	if out := lang.Run("set -o record /tmp/r.jsonl", g, newDirsParent(), lang.Options{State: st, Prompt: true}); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	out := lang.MoveTo("/etc", g, newDirsParent(), lang.Options{State: st, Prompt: true})
	if out.Status != 2 || !strings.Contains(out.Stderr, "not while recording") {
		t.Fatalf("%+v", out)
	}
	if out := lang.MoveTo("/etc", g, newDirsParent(), lang.Options{State: lang.NewState()}); out.Status != 0 || out.Prefix != "/etc" {
		t.Fatalf("not recording: %+v", out)
	}
}
