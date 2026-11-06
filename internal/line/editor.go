package line

// Enter on a Block line does not call Run. Denial is the authorization gate
// in the editor; lang.Run repeats it. Syntax colors off does not clear a
// denial. Do not log the line, the grant, or a path. The editor holds no
// signing key.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

const (
	maxLine = 8192
	maxHist = 200
)

// MenuRow is one Tab row. Denied rows stay visible and insert nothing.
type MenuRow struct {
	Insert string
	Desc   string
	Key    string
	Denied bool
}

// View is the painted prompt. Block means Enter does not call Run.
type View struct {
	Line       string
	Cursor     int
	Suggestion string
	Spans      []lang.Span
	Smile      string
	Menu       []MenuRow
	MenuOpen   bool
	MenuIndex  int
	Syntax     bool
	Block      bool
	Denied     bool
}

// Editor is the live line. Suggestion history comes from the agent for
// this actor on this host. A private session loads none.
type Editor struct {
	g      grant.Grant
	opt    lang.Options
	parent parent.Parent
	run    func(string) lang.Outcome

	line   string
	cursor int
	yank   string

	history   []string
	browse    []string
	browseAt  int
	origin    string
	originCur int

	menu     []completion
	menuOpen bool
	menuIdx  int

	searching      bool
	searchQ        string
	searchSaved    string
	searchSavedCur int
	searchHit      bool
	searchIdx      int

	pending []byte
	note    string
	hold    string

	Syntax bool
	Vi     bool
	viCmd  bool

	Runs   int
	Last   lang.Outcome
	hasOut bool
	done   bool
	status int
}

type closedParent struct{}

func (closedParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{}, fmt.Errorf("preview")
}

// New starts an editor. A nil parent fails closed and does not call a toolkit.
func New(g grant.Grant, p parent.Parent) *Editor {
	if p == nil {
		p = closedParent{}
	}
	e := &Editor{
		g:        g,
		parent:   p,
		Syntax:   true,
		browseAt: -1,
	}
	e.run = func(src string) lang.Outcome {
		opt := e.opt
		opt.History = append([]string(nil), e.history...)
		return lang.Run(src, e.g, e.parent, opt)
	}
	e.loadHist()
	return e
}

func (e *Editor) loadHist() {
	if e.g.AllowPrivate {
		return
	}
	res, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"get"}})
	if err != nil || res.Exit != 0 || res.Kind != parent.KindRows {
		return
	}
	for _, row := range res.Rows {
		line := row["line"]
		if line == "" || strings.ContainsAny(line, "\n\r\x00") || len(line) > maxLine {
			continue
		}
		e.history = append(e.history, line)
	}
	if len(e.history) > maxHist {
		e.history = append([]string(nil), e.history[len(e.history)-maxHist:]...)
	}
}

func (e *Editor) persist(s string) {
	if _, err := e.parent.Do(parent.Verb{Key: "history", Argv: []string{"append"}, Body: []byte(s)}); err != nil {
		return
	}
}

// Feed applies a byte stream. A lone Esc stays pending until the next call.
func (e *Editor) Feed(in []byte) int {
	buf := make([]byte, 0, len(e.pending)+len(in))
	buf = append(buf, e.pending...)
	buf = append(buf, in...)
	e.pending = nil
	i := 0
	for i < len(buf) && !e.done {
		n, k, ok := decode(buf[i:])
		if !ok {
			e.pending = append([]byte{}, buf[i:]...)
			break
		}
		if n <= 0 {
			e.pending = append([]byte{}, buf[i:]...)
			break
		}
		e.handle(k)
		i += n
	}
	return i
}

// View paints the current line with the real parser.
func (e *Editor) View() View {
	pv := lang.Preview(e.line, e.g, e.opt)
	smile := pv.Smile
	spans := pv.Spans
	denied := pv.Denied
	block := pv.Block
	paint, exact := sessionKind(e.line)
	if paint {
		spans = []lang.Span{{Start: 0, End: len(e.line), Face: lang.FaceFilter}}
		if strings.HasPrefix(smile, "unknown ") || strings.HasPrefix(smile, "usage ") {
			smile = ""
		}
		denied = false
		block = false
	} else if !denied && (smile == "" || strings.HasPrefix(smile, "unknown ")) {
		if miss := nearMiss(e.line); miss != "" {
			smile = miss
		}
	}
	if exact != "" {
		smile = ""
		denied = false
		block = false
	}
	if e.line == "" && e.note != "" && !e.searching {
		smile = e.note
	}
	if e.hold != "" {
		smile = e.hold
		if strings.HasPrefix(e.hold, "denied ") {
			denied = true
		}
		block = true
	}
	sug := ""
	if !e.menuOpen && !e.searching && e.browseAt < 0 && e.cursor == len(e.line) && e.line != "" {
		sug = e.suggestionSuffix()
	}
	var rows []MenuRow
	if e.menuOpen {
		rows = make([]MenuRow, len(e.menu))
		for i, c := range e.menu {
			rows[i] = MenuRow{
				Insert: c.insert,
				Desc:   c.desc,
				Key:    c.key,
				Denied: c.key != "" && !e.g.Has(c.key),
			}
		}
	}
	return View{
		Line:       e.line,
		Cursor:     e.cursor,
		Suggestion: sug,
		Spans:      spans,
		Smile:      smile,
		Menu:       rows,
		MenuOpen:   e.menuOpen,
		MenuIndex:  e.menuIdx,
		Syntax:     e.Syntax,
		Block:      block,
		Denied:     denied,
	}
}

func (e *Editor) handle(k key) {
	if e.searching {
		e.handleSearch(k)
		return
	}
	switch k.kind {
	case keyCtrlC:
		e.ctrlC()
		return
	case keyCtrlG:
		e.menuOpen = false
		return
	case keyEsc:
		if e.menuOpen {
			e.menuOpen = false
		}
		if e.Vi {
			e.viCmd = true
		}
		return
	}
	if e.Vi && e.viCmd {
		e.handleVi(k)
		return
	}
	e.handleInsert(k)
}

func (e *Editor) handleSearch(k key) {
	switch k.kind {
	case keyCtrlC:
		e.ctrlC()
	case keyEsc, keyCtrlG:
		e.cancelSearch()
	case keyEnter:
		e.submit()
	case keyBackspace:
		e.searchBack()
	case keyCtrlR:
		e.findSearch(true)
	case keyRune:
		e.searchQ += string(k.r)
		e.searchHit = false
		e.findSearch(false)
	}
}

func (e *Editor) handleInsert(k key) {
	switch k.kind {
	case keyRune:
		e.insert(k.r)
	case keyEnter:
		e.submit()
	case keyBackspace:
		e.backspace()
	case keyCtrlA:
		e.home()
	case keyCtrlE:
		e.end()
	case keyCtrlB, keyLeft:
		e.left()
	case keyCtrlF, keyRight:
		e.right()
	case keyCtrlD:
		e.ctrlD()
	case keyCtrlK:
		e.kill(e.cursor, len(e.line))
	case keyCtrlU:
		e.kill(0, e.cursor)
	case keyCtrlW:
		e.killWord()
	case keyCtrlY:
		e.yankPut()
	case keyAltB:
		e.wordLeft()
	case keyAltF:
		e.wordRight()
	case keyAltDot:
		e.insertLastArg()
	case keyCtrlR:
		e.startSearch()
	case keyTab:
		e.tab()
	case keyUp:
		e.histUp()
	case keyDown:
		e.histDown()
	}
}

func (e *Editor) handleVi(k key) {
	switch k.kind {
	case keyRune:
		switch k.r {
		case 'h':
			e.left()
		case 'l':
			e.right()
		case 'k':
			e.histUp()
		case 'j':
			e.histDown()
		case '0':
			e.home()
		case '$':
			e.end()
		case 'x':
			e.deleteForward()
		case 'i':
			e.viCmd = false
		case 'a':
			e.rightMove()
			e.viCmd = false
		}
	case keyLeft:
		e.left()
	case keyRight:
		e.right()
	case keyUp:
		e.histUp()
	case keyDown:
		e.histDown()
	case keyEnter:
		e.submit()
	case keyCtrlR:
		e.startSearch()
	case keyTab:
		e.tab()
	case keyCtrlD:
		e.ctrlD()
	case keyAltB:
		e.wordLeft()
	case keyAltF:
		e.wordRight()
	case keyAltDot:
		e.insertLastArg()
	}
}

func (e *Editor) insert(r rune) {
	e.insertText(string(r))
}

func (e *Editor) insertText(s string) {
	if s == "" || len(e.line)+len(s) > maxLine {
		return
	}
	e.touch()
	e.line = e.line[:e.cursor] + s + e.line[e.cursor:]
	e.cursor += len(s)
}

func (e *Editor) insertLastArg() {
	if len(e.history) == 0 {
		return
	}
	fields := strings.Fields(e.history[len(e.history)-1])
	if len(fields) == 0 {
		return
	}
	e.insertText(fields[len(fields)-1])
}

func (e *Editor) backspace() {
	if e.cursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(e.line[:e.cursor])
	if size < 1 {
		return
	}
	e.touch()
	e.line = e.line[:e.cursor-size] + e.line[e.cursor:]
	e.cursor -= size
}

func (e *Editor) deleteForward() {
	if e.cursor >= len(e.line) {
		return
	}
	_, size := utf8.DecodeRuneInString(e.line[e.cursor:])
	if size < 1 {
		return
	}
	e.touch()
	e.line = e.line[:e.cursor] + e.line[e.cursor+size:]
}

func (e *Editor) left() {
	if e.cursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(e.line[:e.cursor])
	if size > 0 {
		e.cursor -= size
	}
}

func (e *Editor) rightMove() {
	if e.cursor >= len(e.line) {
		return
	}
	_, size := utf8.DecodeRuneInString(e.line[e.cursor:])
	if size > 0 {
		e.cursor += size
	}
}

func (e *Editor) right() {
	if e.menuOpen {
		e.acceptMenu()
		return
	}
	if e.cursor == len(e.line) {
		suf := e.suggestionSuffix()
		if suf != "" && e.browseAt < 0 && !e.searching {
			if len(e.line)+len(suf) > maxLine {
				suf = suf[:maxLine-len(e.line)]
			}
			e.line += suf
			e.cursor = len(e.line)
			e.touch()
			return
		}
	}
	e.rightMove()
}

func (e *Editor) home() { e.cursor = 0 }

func (e *Editor) end() { e.cursor = len(e.line) }

func (e *Editor) wordLeft() {
	i := e.cursor
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(e.line[:i])
		if r != ' ' && r != '\t' {
			break
		}
		i -= size
	}
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(e.line[:i])
		if r == ' ' || r == '\t' {
			break
		}
		i -= size
	}
	e.cursor = i
}

func (e *Editor) wordRight() {
	i := e.cursor
	for i < len(e.line) {
		r, size := utf8.DecodeRuneInString(e.line[i:])
		if r == ' ' || r == '\t' {
			break
		}
		i += size
	}
	for i < len(e.line) {
		r, size := utf8.DecodeRuneInString(e.line[i:])
		if r != ' ' && r != '\t' {
			break
		}
		i += size
	}
	e.cursor = i
}

func (e *Editor) kill(from, to int) {
	if from > to {
		from, to = to, from
	}
	if from < 0 {
		from = 0
	}
	if to > len(e.line) {
		to = len(e.line)
	}
	if from == to {
		return
	}
	e.yank = e.line[from:to]
	e.touch()
	e.line = e.line[:from] + e.line[to:]
	e.cursor = from
}

func (e *Editor) killWord() {
	i := e.cursor
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(e.line[:i])
		if r != ' ' && r != '\t' {
			break
		}
		i -= size
	}
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(e.line[:i])
		if r == ' ' || r == '\t' {
			break
		}
		i -= size
	}
	e.kill(i, e.cursor)
}

func (e *Editor) yankPut() {
	if e.yank == "" {
		return
	}
	y := e.yank
	if len(e.line)+len(y) > maxLine {
		room := maxLine - len(e.line)
		if room <= 0 {
			return
		}
		for room > 0 && !utf8.ValidString(y[:room]) {
			room--
		}
		y = y[:room]
	}
	if y == "" {
		return
	}
	e.touch()
	e.line = e.line[:e.cursor] + y + e.line[e.cursor:]
	e.cursor += len(y)
}

func (e *Editor) touch() {
	e.menuOpen = false
	e.browseAt = -1
	e.browse = nil
	e.hold = ""
}

func (e *Editor) ctrlC() {
	e.line = ""
	e.cursor = 0
	e.searching = false
	e.searchQ = ""
	e.searchHit = false
	e.menuOpen = false
	e.browseAt = -1
	e.browse = nil
}

func (e *Editor) ctrlD() {
	if e.line == "" && !e.searching {
		e.done = true
		e.status = 0
		return
	}
	if e.cursor < len(e.line) {
		e.deleteForward()
	}
}

func (e *Editor) tab() {
	if e.menuOpen {
		if len(e.menu) == 0 {
			return
		}
		e.menuIdx = (e.menuIdx + 1) % len(e.menu)
		return
	}
	prefix := strings.TrimRight(e.line, " \t")
	got := matches(prefix)
	if len(got) == 0 {
		return
	}
	entitled := 0
	var one completion
	for _, row := range got {
		if row.key == "" || e.g.Has(row.key) {
			entitled++
			one = row
		}
	}
	if len(got) == 1 && entitled == 1 {
		e.line = one.insert
		e.cursor = len(e.line)
		e.touch()
		return
	}
	e.menu = got
	e.menuOpen = true
	e.menuIdx = 0
	e.browseAt = -1
}

func (e *Editor) acceptMenu() {
	if !e.menuOpen || e.menuIdx < 0 || e.menuIdx >= len(e.menu) {
		return
	}
	row := e.menu[e.menuIdx]
	if row.key != "" && !e.g.Has(row.key) {
		return
	}
	e.line = row.insert
	e.cursor = len(e.line)
	e.touch()
}

func (e *Editor) histUp() {
	if e.menuOpen {
		if e.menuIdx > 0 {
			e.menuIdx--
		}
		return
	}
	if e.browseAt < 0 {
		e.origin = e.line
		e.originCur = e.cursor
		e.browse = e.prefixMatches(e.origin)
		if len(e.browse) == 0 {
			return
		}
		e.browseAt = 0
	} else if e.browseAt < len(e.browse)-1 {
		e.browseAt++
	}
	e.line = e.browse[e.browseAt]
	e.cursor = len(e.line)
}

func (e *Editor) histDown() {
	if e.menuOpen {
		if e.menuIdx < len(e.menu)-1 {
			e.menuIdx++
		}
		return
	}
	if e.browseAt < 0 {
		return
	}
	if e.browseAt == 0 {
		e.line = e.origin
		e.cursor = e.originCur
		e.browseAt = -1
		e.browse = nil
		return
	}
	e.browseAt--
	e.line = e.browse[e.browseAt]
	e.cursor = len(e.line)
}

func (e *Editor) prefixMatches(origin string) []string {
	var out []string
	for i := len(e.history) - 1; i >= 0; i-- {
		h := e.history[i]
		if origin == "" || strings.HasPrefix(h, origin) {
			out = append(out, h)
		}
	}
	return out
}

func (e *Editor) suggestionSuffix() string {
	if e.line == "" || e.cursor != len(e.line) || e.menuOpen || e.searching || e.browseAt >= 0 {
		return ""
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		h := e.history[i]
		if strings.HasPrefix(h, e.line) && len(h) > len(e.line) {
			return h[len(e.line):]
		}
	}
	return ""
}

func (e *Editor) startSearch() {
	if !e.searching {
		e.searching = true
		e.searchSaved = e.line
		e.searchSavedCur = e.cursor
		e.searchQ = ""
		e.searchHit = false
		e.menuOpen = false
		e.browseAt = -1
		e.browse = nil
	}
	e.findSearch(true)
}

func (e *Editor) findSearch(next bool) {
	if e.searchQ == "" {
		e.searchHit = false
		e.line = e.searchSaved
		e.cursor = e.searchSavedCur
		return
	}
	start := len(e.history) - 1
	if next && e.searchHit {
		start = e.searchIdx - 1
	}
	for i := start; i >= 0; i-- {
		if strings.Contains(e.history[i], e.searchQ) {
			e.line = e.history[i]
			e.cursor = len(e.line)
			e.searchHit = true
			e.searchIdx = i
			return
		}
	}
	e.searchHit = false
	e.line = e.searchSaved
	e.cursor = e.searchSavedCur
}

func (e *Editor) searchBack() {
	if e.searchQ == "" {
		return
	}
	r := []rune(e.searchQ)
	e.searchQ = string(r[:len(r)-1])
	e.searchHit = false
	e.findSearch(false)
}

func (e *Editor) cancelSearch() {
	e.searching = false
	e.searchQ = ""
	e.searchHit = false
	e.line = e.searchSaved
	e.cursor = e.searchSavedCur
}

func (e *Editor) submit() {
	if e.searching {
		hit := e.searchHit
		line := e.line
		cur := e.cursor
		e.searching = false
		e.searchQ = ""
		e.searchHit = false
		if !hit {
			e.line = e.searchSaved
			e.cursor = e.searchSavedCur
			return
		}
		e.line = line
		e.cursor = cur
	}
	if e.line == "" {
		return
	}
	e.menuOpen = false
	if exact := sessionExact(e.line); exact != "" {
		e.hold = ""
		e.applySession(exact)
		e.pushHist(exact)
		e.note = exact
		e.line = ""
		e.cursor = 0
		e.browseAt = -1
		return
	}
	expanded, ep := lang.Expand(e.line, e.g, e.parent, lang.Options{
		Prefix:  e.opt.Prefix,
		History: append([]string(nil), e.history...),
	})
	if ep != nil {
		e.hold = ep.Msg
		return
	}
	if len(expanded) > maxLine {
		e.hold = "type brace"
		return
	}
	if expanded != e.line {
		e.line = expanded
		e.cursor = len(expanded)
	}
	e.hold = ""
	v := e.View()
	if v.Block {
		return
	}
	out := e.run(e.line)
	e.Runs++
	e.Last = out
	e.opt.Prefix = out.Prefix
	e.pushHist(e.line)
	e.line = ""
	e.cursor = 0
	e.browseAt = -1
	e.browse = nil
	e.hasOut = true
}

func (e *Editor) applySession(cmd string) {
	switch cmd {
	case "bind vi":
		e.Vi = true
		e.viCmd = false
	case "bind emacs":
		e.Vi = false
		e.viCmd = false
	case "syntax off":
		e.Syntax = false
	case "syntax on":
		e.Syntax = true
	case "trace off":
		e.opt.Trace = false
	case "trace on":
		e.opt.Trace = true
	}
}

func (e *Editor) pushHist(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	e.history = append(e.history, s)
	if len(e.history) > maxHist {
		e.history = append([]string(nil), e.history[len(e.history)-maxHist:]...)
	}
	e.persist(s)
}

func sessionKind(line string) (paint bool, exact string) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false, ""
	}
	switch f[0] {
	case "bind", "syntax", "trace":
	default:
		return false, ""
	}
	if len(f) == 1 {
		return true, ""
	}
	if len(f) > 2 {
		return false, ""
	}
	second := f[1]
	switch f[0] {
	case "bind":
		if second == "vi" || second == "emacs" {
			return true, "bind " + second
		}
		if strings.HasPrefix("vi", second) || strings.HasPrefix("emacs", second) {
			return true, ""
		}
		return false, ""
	default:
		if second == "on" || second == "off" {
			return true, f[0] + " " + second
		}
		if strings.HasPrefix("on", second) || strings.HasPrefix("off", second) {
			return true, ""
		}
		return false, ""
	}
}

func sessionExact(line string) string {
	_, exact := sessionKind(line)
	return exact
}

// Loop is the login prompt. It sets raw mode only on a terminal.
// Write, read, and restore errors return 1.
func Loop(in, out, errOut *os.File, g grant.Grant, p parent.Parent) int {
	e := New(g, p)
	restore, err := enterRaw(int(in.Fd()))
	if err != nil {
		if _, werr := fmt.Fprintln(errOut, "sash line failed"); werr != nil {
			return 1
		}
		return 1
	}
	buf := make([]byte, 256)
	for {
		view := e.View()
		if err := writeAll(out, []byte(Frame(view))); err != nil {
			return finishRaw(restore, 1)
		}
		n, err := in.Read(buf)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			st := 1
			if errors.Is(err, io.EOF) {
				st = e.status
			}
			return finishRaw(restore, st)
		}
		before := view
		e.Feed(buf[:n])
		if e.done {
			if err := writeAll(out, []byte("\r\n")); err != nil {
				return finishRaw(restore, 1)
			}
			if err := restore(); err != nil {
				return 1
			}
			return e.status
		}
		if e.hasOut {
			if err := writeSubmit(out, errOut, before, e.Last); err != nil {
				return finishRaw(restore, 1)
			}
			e.hasOut = false
		}
	}
}

func finishRaw(restore func() error, status int) int {
	if err := restore(); err != nil {
		return 1
	}
	return status
}

func writeSubmit(out, errOut *os.File, before View, last lang.Outcome) error {
	extra := 0
	if before.MenuOpen {
		extra = len(before.Menu)
	} else if before.Smile != "" {
		extra = 1
	}
	var b strings.Builder
	if extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dB", extra)
	}
	b.WriteString("\r\n")
	if err := writeAll(out, []byte(b.String())); err != nil {
		return err
	}
	if err := writeAll(out, []byte(last.Stdout)); err != nil {
		return err
	}
	if err := writeAll(errOut, []byte(last.Stderr)); err != nil {
		return err
	}
	return nil
}

func writeAll(f *os.File, b []byte) error {
	for len(b) > 0 {
		n, err := f.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
