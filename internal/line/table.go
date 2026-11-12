package line

import (
	"strings"

	"github.com/afterdarksys/sash/internal/lang"
)

// completion is one closed-table row. Insert includes a trailing space.
type completion struct {
	insert string
	form   string
	desc   string
	key    string
	// words are the word tokens insert must lex to in the new line; nil
	// is the words of insert (a compiled-in form).
	words []string
}

// table is the Tab menu, built from lang.Builtins so Tab, prompt menu, and
// the builtin command list the same forms. Filters have an empty key and
// are never denied.
var table = buildTable()

func buildTable() []completion {
	var out []completion
	for _, b := range lang.Builtins() {
		if b.Tab {
			out = append(out, completion{insert: b.Form + " ", form: b.Form, desc: b.Desc, key: b.Key})
		}
	}
	return out
}

// nearMiss offers a closed-table form for a mistyped command.
// Equal distance keeps the earlier table row. An exact prefix is not a miss.
func nearMiss(line string) string {
	words := commandWords(line)
	if len(words) == 0 {
		return ""
	}
	bestD := 3
	best := ""
	for _, row := range table {
		form := strings.Fields(row.form)
		if len(form) != len(words) {
			continue
		}
		if exactPrefix(words, form) {
			continue
		}
		d := 0
		skip := false
		for i := range words {
			di := levenshtein(words[i], form[i])
			if di > 2 {
				skip = true
				break
			}
			d += di
		}
		if skip || d == 0 || d >= 3 {
			continue
		}
		if d < bestD {
			bestD = d
			best = row.form
		}
	}
	return best
}

func commandWords(line string) []string {
	cut := line
	if i := strings.IndexAny(line, "|;&<>"); i >= 0 {
		cut = line[:i]
	}
	return strings.Fields(cut)
}

func exactPrefix(words, form []string) bool {
	if len(words) > len(form) {
		return false
	}
	for i := range words {
		if words[i] != form[i] {
			return false
		}
	}
	return true
}

func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}
