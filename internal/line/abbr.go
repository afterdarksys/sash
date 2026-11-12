package line

// Abbreviations (v5 section 6) are a visible rewrite, interactive only:
// abbr name 'text' and abbr -e name are handled here, like bind, and never
// reach lang.State, so scripts, use, --needs, and --lint never see one
// (abbr in a script is an unknown command, exit 127). Threats: an
// abbreviation never hides a verb. It is session-only, with no file to
// plant one in (CVE-2022-20001). Its text is plain words only, so it
// cannot carry a separator, a pipe, a redirect, a comment, a $ or [ ]
// expansion, a quote, a control character, or a declare -s. Space after
// it at command position rewrites it on the line, where the reader and the
// preview see the verb; Enter on an unexpanded one rewrites it and stops,
// so a second Enter runs what is shown. Expansion is not recursive.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/afterdarksys/sash/internal/lang"
)

const (
	abbrTextMax = 256
	abbrMax     = 256
)

var (
	abbrNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	abbrWordRe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)
	// abbrSession are the editor's own words; an abbreviation cannot be one.
	abbrSession = map[string]bool{"abbr": true, "bind": true, "syntax": true, "trace": true, "prompt": true, "why": true, "exit": true, "logout": true}
)

const abbrUsage = "usage abbr name 'text' or abbr -e name"

// abbrCommand runs an abbr line and says what happened in the note.
func (e *Editor) abbrCommand(line string) {
	words, ok := lang.LiteralWords(line)
	switch {
	case !ok:
		e.note = abbrUsage
	case len(words) == 1:
		e.note = e.abbrList()
	case len(words) == 3 && words[1] == "-e":
		if _, ok := e.abbrs[words[2]]; !ok {
			e.note = "abbr: no " + lang.Cell(words[2])
			return
		}
		delete(e.abbrs, words[2])
		e.note = "abbr -e " + words[2]
	case len(words) == 3:
		if msg := abbrCheck(words[1], words[2]); msg != "" {
			e.note = msg
			return
		}
		if _, ok := e.abbrs[words[1]]; !ok && len(e.abbrs) >= abbrMax {
			e.note = "abbr: too many"
			return
		}
		if e.abbrs == nil {
			e.abbrs = map[string]string{}
		}
		e.abbrs[words[1]] = words[2]
		e.note = "abbr " + words[1]
	default:
		e.note = abbrUsage
	}
}

// abbrCheck is why name and text cannot be an abbreviation, or "".
func abbrCheck(name, text string) string {
	if !abbrNameRe.MatchString(name) || lang.Reserved(name) || abbrSession[name] {
		return "abbr: the name must be a plain word that is not a keyword or command"
	}
	for _, b := range lang.Builtins() {
		if strings.Fields(b.Form)[0] == name {
			return "abbr: the name must be a plain word that is not a keyword or command"
		}
	}
	if text == "" || len(text) > abbrTextMax {
		return "abbr: the text is 1 to 256 bytes"
	}
	words := strings.Fields(text)
	for _, w := range words {
		if !abbrWordRe.MatchString(w) {
			return "abbr: the text is plain words only (no $, [ ], quotes, ; | & # < > or controls)"
		}
	}
	if strings.Join(words, " ") != text || lang.Cell(text) != text || lang.DeclaresSecret(text) {
		return "abbr: the text is plain words only (no $, [ ], quotes, ; | & # < > or controls)"
	}
	return ""
}

func (e *Editor) abbrList() string {
	if len(e.abbrs) == 0 {
		return "abbr: none"
	}
	names := make([]string, 0, len(e.abbrs))
	for n := range e.abbrs {
		names = append(names, n)
	}
	sort.Strings(names)
	return "abbr " + strings.Join(names, " ")
}

// expandAtCursor rewrites the word before the cursor when it is a whole
// command (Space after it): the reader sees the text before a key runs it.
func (e *Editor) expandAtCursor() {
	if len(e.abbrs) == 0 {
		return
	}
	line, cur := e.line, e.cursor
	start := cur
	for start > 0 && !strings.ContainsRune(wordStop, rune(line[start-1])) {
		start--
	}
	word := line[start:cur]
	text, ok := e.abbrs[word]
	if !ok || commandStart(line, start, cur) != start || !lang.LexOK(line[:start]) {
		return
	}
	e.line = line[:start] + text + line[cur:]
	e.cursor = start + len(text)
}

// expandLine rewrites every command word of the line that is an
// abbreviation, for Enter. It reports whether it changed anything; then
// Enter stops, so the rewritten line is read before it runs.
func (e *Editor) expandLine() bool {
	if len(e.abbrs) == 0 {
		return false
	}
	spans := lang.CommandWords(e.line)
	changed := false
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		if text, ok := e.abbrs[e.line[s[0]:s[1]]]; ok {
			e.line = e.line[:s[0]] + text + e.line[s[1]:]
			changed = true
		}
	}
	if changed {
		e.cursor = len(e.line)
	}
	return changed
}
