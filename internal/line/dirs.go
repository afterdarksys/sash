package line

// z and jump (docs/plans/2026-11-22-sash-z-jump.md): session commands,
// handled here like abbr, never by a script (lang refuses them there).
//
// Threats: a stored directory is data, never source. It goes through
// lang.MoveTo, which checks the path invariants, the grant's roots and
// stats it as cd does; a candidate outside the roots is never shown or
// used. Bookmark names are [a-z0-9_-]{1,32}. A private session stores and
// reads nothing. The store is the agent's history verb (dirs, visit,
// marks, mark, unmark); an agent that denies those forms leaves this
// session's own visits and bookmarks, with one notice.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

const (
	zUsage    = "usage: z part... or z -l part..."
	jumpUsage = "usage: jump name, jump save name, jump list, jump rm name"
	marksMax  = 64
	dirMax    = 4096
)

var dirsNow = time.Now

type dirRec struct {
	dir   string
	count int64
	last  int64
}

// dirState is the session's view: its own visits and bookmarks, and
// whether the agent's store answered.
type dirState struct {
	local    map[string]dirRec
	marks    map[string]string
	noStore  bool
	notified bool
}

func (e *Editor) dirsState() *dirState {
	if e.dirs == nil {
		e.dirs = &dirState{local: map[string]dirRec{}, marks: map[string]string{}}
	}
	return e.dirs
}

func markNameOK(n string) bool {
	if len(n) < 1 || len(n) > 32 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// storeNotice marks the agent's store missing, noting it once.
func (e *Editor) storeNotice() {
	d := e.dirsState()
	d.noStore = true
	if !d.notified {
		d.notified = true
		e.note = "z: history store unavailable, this session only"
	}
}

// recordVisit stores a directory the session moved to: the agent's store
// and this session's own list. A private session records nothing.
func (e *Editor) recordVisit(dir string) {
	if e.g.AllowPrivate || dir == "" || len(dir) > dirMax {
		return
	}
	d := e.dirsState()
	r := d.local[dir]
	r.dir, r.count, r.last = dir, r.count+1, dirsNow().Unix()
	d.local[dir] = r
	if d.noStore {
		return
	}
	if _, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"visit"}, Body: []byte(dir)}); err != nil {
		e.storeNotice()
	}
}

// candidates are the stored and session visits, merged by directory.
func (e *Editor) candidates() []dirRec {
	d := e.dirsState()
	all := map[string]dirRec{}
	if !e.g.AllowPrivate && !d.noStore {
		res, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"dirs"}})
		if err != nil {
			e.storeNotice()
		} else if res.Kind == parent.KindRows {
			for _, row := range res.Rows {
				c, err1 := strconv.ParseInt(row["count"], 10, 64)
				t, err2 := strconv.ParseInt(row["last"], 10, 64)
				if err1 != nil || err2 != nil || row["dir"] == "" {
					continue
				}
				all[row["dir"]] = dirRec{dir: row["dir"], count: c, last: t}
			}
		}
	}
	for k, r := range d.local {
		if s, ok := all[k]; !ok || r.last > s.last {
			if ok && r.count < s.count {
				r.count = s.count
			}
			all[k] = r
		}
	}
	out := make([]dirRec, 0, len(all))
	for _, r := range all {
		out = append(out, r)
	}
	return out
}

func dirScore(r dirRec, now int64) float64 {
	age := now - r.last
	w := 0.25
	switch {
	case age < 3600:
		w = 4
	case age < 86400:
		w = 2
	case age < 7*86400:
		w = 1
	}
	return float64(r.count) * w
}

// matchParts: each part appears in dir, in order.
func matchParts(dir string, parts []string) bool {
	rest := dir
	for _, p := range parts {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return true
}

// ranked are the matching candidates inside the grant's roots, best first.
func (e *Editor) ranked(parts []string) []dirRec {
	now := dirsNow().Unix()
	var out []dirRec
	for _, r := range e.candidates() {
		if !matchParts(r.dir, parts) || !e.g.Has("file.read") || !e.g.AllowsRead(r.dir) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := dirScore(out[i], now), dirScore(out[j], now)
		if si != sj {
			return si > sj
		}
		if out[i].last != out[j].last {
			return out[i].last > out[j].last
		}
		return out[i].dir < out[j].dir
	})
	return out
}

// moveTo changes the prefix through lang.MoveTo and records the visit.
func (e *Editor) moveTo(dir string) bool {
	out := lang.MoveTo(dir, e.g, e.parent, e.opt)
	e.Last = out
	if out.Status != 0 {
		msg := strings.TrimSpace(out.Stderr)
		if msg == "" {
			msg = "exit " + strconv.Itoa(out.Status)
		}
		e.note = msg
		return false
	}
	e.opt.Prefix = out.Prefix
	e.recordVisit(out.Prefix)
	return true
}

func (e *Editor) zCommand(line string) {
	words, ok := lang.LiteralWords(line)
	if !ok || len(words) < 2 {
		e.note = zUsage
		return
	}
	if words[1] == "-l" {
		if len(words) < 3 {
			e.note = zUsage
			return
		}
		var b strings.Builder
		for _, r := range e.ranked(words[2:]) {
			fmt.Fprintf(&b, "%s  %s  %s\n", lang.Cell(r.dir), strconv.FormatFloat(dirScore(r, dirsNow().Unix()), 'g', 4, 64), time.Unix(r.last, 0).UTC().Format(time.RFC3339))
		}
		if b.Len() == 0 {
			e.note = "z: no match"
			e.Last = lang.Outcome{Status: 1, Prefix: e.opt.Prefix}
			return
		}
		e.note = strings.TrimSuffix(b.String(), "\n")
		return
	}
	for _, r := range e.ranked(words[1:]) {
		if e.moveTo(r.dir) {
			e.note = ""
			return
		}
	}
	if e.note == "" {
		e.note = "z: no match"
	}
	e.Last = lang.Outcome{Status: 1, Prefix: e.opt.Prefix}
}

// loadMarks reads the stored bookmarks into the session's set.
func (e *Editor) loadMarks() map[string]string {
	d := e.dirsState()
	if e.g.AllowPrivate || d.noStore {
		return d.marks
	}
	res, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"marks"}})
	if err != nil {
		e.storeNotice()
		return d.marks
	}
	marks := map[string]string{}
	for _, row := range res.Rows {
		if markNameOK(row["name"]) && row["dir"] != "" {
			marks[row["name"]] = row["dir"]
		}
	}
	return marks
}

func (e *Editor) jumpCommand(line string) {
	words, ok := lang.LiteralWords(line)
	if !ok || len(words) < 2 {
		e.note = jumpUsage
		return
	}
	d := e.dirsState()
	switch {
	case len(words) == 2 && words[1] == "list":
		marks := e.loadMarks()
		names := make([]string, 0, len(marks))
		for n := range marks {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			fmt.Fprintf(&b, "%s  %s\n", n, lang.Cell(marks[n]))
		}
		e.note = strings.TrimSuffix(b.String(), "\n")
		if e.note == "" {
			e.note = "jump: no bookmarks"
		}
	case len(words) == 3 && words[1] == "save":
		name := words[2]
		if !markNameOK(name) {
			e.note = "jump: bookmark names are [a-z0-9_-]{1,32}"
			return
		}
		if e.opt.Prefix == "" {
			e.note = "prefix_unset"
			return
		}
		if _, have := d.marks[name]; !have && len(d.marks) >= marksMax {
			e.note = "jump: too many bookmarks"
			return
		}
		if !e.g.AllowPrivate && !d.noStore {
			if _, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"mark"}, Body: []byte(name + "\t" + e.opt.Prefix)}); err != nil {
				e.storeNotice()
			}
		}
		d.marks[name] = e.opt.Prefix
		if e.note == "" {
			e.note = "jump save " + name
		}
	case len(words) == 3 && words[1] == "rm":
		name := words[2]
		if !markNameOK(name) {
			e.note = "jump: bookmark names are [a-z0-9_-]{1,32}"
			return
		}
		if !e.g.AllowPrivate && !d.noStore {
			if _, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"unmark"}, Body: []byte(name)}); err != nil {
				e.storeNotice()
			}
		}
		delete(d.marks, name)
		if e.note == "" {
			e.note = "jump rm " + name
		}
	case len(words) == 2:
		name := words[1]
		if !markNameOK(name) {
			e.note = jumpUsage
			return
		}
		dir, have := e.loadMarks()[name]
		if !have {
			dir, have = d.marks[name]
		}
		if !have {
			e.note = "jump: no bookmark " + name
			e.Last = lang.Outcome{Status: 1, Prefix: e.opt.Prefix}
			return
		}
		if e.moveTo(dir) {
			e.note = ""
		}
	default:
		e.note = jumpUsage
	}
}
