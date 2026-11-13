package lang

// Value provenance (docs/plans/2026-11-12-sash-v6-tier3.md §2).
//
// Each binding carries prov, IDs into its State's origin table. An origin
// is one door call: the verb key, its target, the script and line, and
// the sha256 of the response. A statement gathers the provenance of every
// name it reads and every verb it calls; whatever it binds gets that
// union. A fn's parameters carry their argument's provenance, and what the
// call returns carries what its body read, since the body's statements
// fold into the calling statement.
//
// Threats: provenance shows where a value came from, never what it is. A
// secret's rows print hash ***, because a hash of a low-entropy value can
// be guessed offline; a target is never secret (a verb argument holding a
// secret is refused before the call, and is *** here if one ever got
// through). Targets are escaped and capped at 256 bytes. Provenance never
// goes into the agent audit or a log line, and it never fails a run: past
// 16 origins a value keeps one overflow marker, past 10,000 a State hands
// out overflow. A recording does not carry provenance: replay derives it
// again from the same responses (a redacted response hashes as ***).

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

const (
	provPerValue = 16
	provPerState = 10000
	provTarget   = 256
	// provOverflow marks a value whose origins were cut, or a State out
	// of room. It sorts last.
	provOverflow = ^uint32(0)
)

// fnSource is where a fn was defined: its text's name and line offsets.
type fnSource struct {
	src string
	nl  []int
}

type origin struct {
	src    string // "script", or the use file's path
	line   int
	key    string
	target string // escaped and capped
	hash   [32]byte
}

// unionProv merges two sorted origin lists, keeping at most provPerValue
// entries: past that, the first ones and an overflow marker.
func unionProv(a, b []uint32) []uint32 {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	// When one side already holds the other, return it: no allocation in
	// the common case of a statement reading what it has already read.
	if n := unionLen(a, b); n == len(a) {
		return a
	} else if n == len(b) {
		return b
	}
	out := make([]uint32, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		var x uint32
		switch {
		case j >= len(b) || (i < len(a) && a[i] < b[j]):
			x = a[i]
			i++
		case i >= len(a) || b[j] < a[i]:
			x = b[j]
			j++
		default:
			x = a[i]
			i++
			j++
		}
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	if len(out) > provPerValue {
		out = append(out[:provPerValue-1], provOverflow)
	}
	return out
}

// addProv is unionProv(p, {id}) without building the one-element list.
func addProv(p []uint32, id uint32) []uint32 {
	i := sort.Search(len(p), func(i int) bool { return p[i] >= id })
	if i < len(p) && p[i] == id {
		return p
	}
	out := make([]uint32, 0, len(p)+1)
	out = append(append(append(out, p[:i]...), id), p[i:]...)
	if len(out) > provPerValue {
		out = append(out[:provPerValue-1], provOverflow)
	}
	return out
}

// unionLen is the size of a's and b's union, both sorted and unique.
func unionLen(a, b []uint32) int {
	n, i, j := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case b[j] < a[i]:
			j++
		default:
			i++
			j++
		}
		n++
	}
	return n + len(a) - i + len(b) - j
}

// addOrigin stores o and returns its ID, or overflow once the State holds
// provPerState origins.
func (s *State) addOrigin(o origin) uint32 {
	if len(s.origins) >= provPerState {
		return provOverflow
	}
	s.origins = append(s.origins, o)
	return uint32(len(s.origins) - 1)
}

// readProv adds name's provenance to the current statement's, and
// remembers the name, so binding a secret can mark each name's own
// lineage rather than the capped union.
func (m *machine) readProv(name string) {
	if v, ok := m.vars[name]; ok && len(v.prov) > 0 {
		m.prov = unionProv(m.prov, v.prov)
		m.readNames = append(m.readNames, name)
	}
}

// withProv is v carrying the current statement's provenance too. A
// secret being bound marks its origins secret.
func (m *machine) withProv(v value) value {
	v.prov = unionProv(v.prov, m.prov)
	m.markSecret(v)
	return v
}

// markSecret records, when v is a secret, that every origin in its
// provenance produced secret bytes, the hashes of those responses, and
// the secret's texts. The sets only grow: origin then hides the hash of
// any of those origins, of any other response with the same hash (a
// second read of the same file), and of any later response holding one
// of the texts.
func (m *machine) markSecret(v value) {
	if v.kind != valData {
		return
	}
	inner, ok := unsecret(v.d)
	if !ok {
		return
	}
	st := m.st
	if st.secretOrigin == nil {
		st.secretOrigin, st.secretHash, st.secretText = map[uint32]bool{}, map[[32]byte]bool{}, map[string]bool{}
	}
	// The secret's own provenance is capped at 16, so it also marks the
	// full provenance of every name the binding statement read. If the
	// secret's or any of those overflowed, the lost origins cannot be
	// named, so every origin so far is marked: over-hiding, never a leak.
	overflow := false
	mark := func(prov []uint32) {
		for _, id := range prov {
			if id == provOverflow || int(id) >= len(st.origins) {
				overflow = true
				continue
			}
			st.secretOrigin[id] = true
			st.secretHash[st.origins[id].hash] = true
		}
	}
	mark(v.prov)
	for _, name := range m.readNames {
		if rv, ok := m.vars[name]; ok {
			mark(rv.prov)
		}
	}
	if overflow {
		for id := range st.origins {
			st.secretOrigin[uint32(id)] = true
			st.secretHash[st.origins[id].hash] = true
		}
	}
	var add func(d any)
	add = func(d any) {
		switch x := d.(type) {
		case []any:
			for _, it := range x {
				add(it)
			}
		case *omap:
			for _, k := range x.keys {
				add(x.m[k])
			}
		case nil:
		default:
			if s := textOf(x); s != "" {
				st.secretText[s] = true
			}
		}
	}
	if s := textOf(inner); s != "" {
		st.secretText[s] = true
	}
	add(inner)
}

// respHoldsSecret reports whether a response holds a secret text bound so
// far, by the recording's rule: a text of four runes or more anywhere in
// a field, a shorter one as a whole field.
func (s *State) respHoldsSecret(res parent.Result) bool {
	if len(s.secretText) == 0 {
		return false
	}
	texts := make([]string, 0, len(s.secretText))
	for t := range s.secretText {
		texts = append(texts, t)
	}
	if holdsAny(string(res.Bytes), texts) {
		return true
	}
	for _, r := range res.Rows {
		for _, v := range r {
			if holdsAny(v, texts) {
				return true
			}
		}
	}
	return false
}

// hideHash reports whether origin id's hash must print ***.
func (s *State) hideHash(id uint32) bool {
	return s.secretOrigin[id] || s.secretHash[s.origins[id].hash]
}

func (m *machine) provSrcName() string {
	if m.provSrc == "" {
		return "script"
	}
	return m.provSrc
}

func (m *machine) provLine() int { return sort.SearchInts(m.nl, m.at) + 1 }

// noteOrigin records a door call that answered: its key, target, place,
// and response hash.
func (m *machine) noteOrigin(v parent.Verb, res parent.Result) {
	target := v.Path
	if target == "" && len(v.Argv) > 2 && (v.Argv[0] == "unit" || v.Argv[0] == "package" || v.Argv[0] == "sysctl") {
		target = v.Argv[2]
	}
	if m.sawSecret {
		target = redacted3
	}
	if len(target) > provTarget {
		target = target[:provTarget]
	}
	h := responseHash(res)
	src := m.provSrcName()
	if target != "" {
		target = needsCell(target)
	}
	id := m.st.addOrigin(origin{src: src, line: m.provLine(), key: v.Key, target: target, hash: h})
	// A response holding a secret already bound is secret from the start;
	// the bytes are not kept, so this is checked here, once.
	if id != provOverflow && (m.st.secretHash[h] || m.st.respHoldsSecret(res)) {
		if m.st.secretOrigin == nil {
			m.st.secretOrigin, m.st.secretHash, m.st.secretText = map[uint32]bool{}, map[[32]byte]bool{}, map[string]bool{}
		}
		m.st.secretOrigin[id] = true
	}
	m.prov = addProv(m.prov, id)
}

// responseHash is the sha256 of a response: its bytes, or for rows the
// columns and every cell in order, each length-prefixed so no two row
// sets hash the same. It is streamed, never built into one buffer.
func responseHash(res parent.Result) [32]byte {
	if res.Kind != parent.KindRows {
		return sha256.Sum256(res.Bytes)
	}
	h := sha256.New()
	buf := make([]byte, 0, 4096)
	put := func(s string) {
		l := uint64(len(s))
		for i := 56; i >= 0; i -= 8 {
			buf = append(buf, byte(l>>uint(i)))
		}
		buf = append(buf, s...)
		if len(buf) >= 4096 {
			h.Write(buf)
			buf = buf[:0]
		}
	}
	cols := res.Cols
	if len(cols) == 0 && len(res.Rows) > 0 {
		for k := range res.Rows[0] {
			cols = append(cols, k)
		}
		sort.Strings(cols)
	}
	for _, c := range cols {
		put(c)
	}
	for _, r := range res.Rows {
		for _, c := range cols {
			put(r[c])
		}
	}
	h.Write(buf)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

var originCols = []string{"origin", "key", "target", "line", "hash"}

// runOrigin is origin name: one row per origin of the name's value. A
// secret's hashes are ***. Unknown names are exit 2 unset name.
func (m *machine) runOrigin(name string) (value, int) {
	v, ok := m.vars[name]
	if !ok {
		m.err.WriteString("unset " + needsCell(name) + "\n")
		return value{}, exitType
	}
	secret := isSecret(toDatum(v))
	rows := make([]parent.Row, 0, len(v.prov))
	for _, id := range v.prov {
		if id == provOverflow || int(id) >= len(m.st.origins) {
			rows = append(rows, parent.Row{"origin": "overflow", "key": "", "target": "truncated", "line": "", "hash": ""})
			continue
		}
		o := m.st.origins[id]
		hash := hex.EncodeToString(o.hash[:])
		if secret || m.st.hideHash(id) {
			hash = redacted3
		}
		rows = append(rows, parent.Row{"origin": needsCell(o.src), "key": needsCell(o.key), "target": o.target, "line": strconv.Itoa(o.line), "hash": hash})
	}
	return value{kind: valRows, rows: rows, cols: append([]string(nil), originCols...), escaped: true}, 0
}

// provNewlines indexes text's lines for origin line numbers.
func provNewlines(text string) []int {
	var nl []int
	for i := strings.IndexByte(text, '\n'); i >= 0; {
		nl = append(nl, i)
		j := strings.IndexByte(text[i+1:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
	}
	return nl
}
