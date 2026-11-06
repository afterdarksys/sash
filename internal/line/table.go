package line

import "strings"

// completion is one closed-table row. Insert includes a trailing space.
type completion struct {
	insert string
	form   string
	desc   string
	key    string
}

// table is the Tab menu. Filters have an empty key and are never denied.
var table = []completion{
	{"ps ", "ps", "process table", "ps"},
	{"unit status ", "unit status", "service status", "unit.status"},
	{"unit restart ", "unit restart", "restart a service", "unit.restart"},
	{"journal ", "journal", "journal read", "journal"},
	{"package query ", "package query", "query a package", "package.query"},
	{"package install ", "package install", "install a package", "package.install"},
	{"sysctl get ", "sysctl get", "read a sysctl", "sysctl.get"},
	{"sysctl set ", "sysctl set", "set a sysctl", "sysctl.set"},
	{"config read ", "config read", "read a config", "config.read"},
	{"cat ", "cat", "read a file", "file.read"},
	{"ls ", "ls", "list a directory", "file.read"},
	{"cd ", "cd", "change the prefix", "file.read"},
	{"pushd ", "pushd", "push the prefix", "file.read"},
	{"echo ", "echo", "print arguments", ""},
	{"egrep ", "egrep", "filter lines", ""},
	{"head ", "head", "first lines", ""},
	{"cut ", "cut", "cut fields", ""},
	{"wc ", "wc", "count", ""},
	{"where ", "where", "keep rows", ""},
	{"select ", "select", "pick columns", ""},
	{"let ", "let", "bind a name", ""},
	{"if ", "if", "conditional", ""},
	{"for ", "for", "walk rows", ""},
	{"fn ", "fn", "define a command", ""},
	{"dirs ", "dirs", "show the prefix stack", ""},
	{"popd ", "popd", "pop the prefix", ""},
}

func matches(prefix string) []completion {
	if prefix == "" {
		return nil
	}
	var out []completion
	for _, row := range table {
		if strings.HasPrefix(row.insert, prefix) {
			out = append(out, row)
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
