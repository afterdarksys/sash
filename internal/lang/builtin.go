package lang

import (
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// Builtin is one form sash understands. The Tab table, the prompt menu, and
// the builtin command all read this one list, so they cannot disagree. A
// test fails when a reserved or dispatched name is missing from it.
type Builtin struct {
	Form string // "unit status"
	Kind string // keyword, builtin, session, filter, verb
	Key  string // the entitlement; "" for anything in process
	Desc string
	Tab  bool // offered by Tab and prompt menu
}

var builtins = []Builtin{
	{"ps", "verb", "ps", "process table", true},
	{"unit status", "verb", "unit.status", "service status", true},
	{"unit restart", "verb", "unit.restart", "restart a service", true},
	{"journal", "verb", "journal", "journal read", true},
	{"package query", "verb", "package.query", "query a package", true},
	{"package install", "verb", "package.install", "install a package", true},
	{"sysctl get", "verb", "sysctl.get", "read a sysctl", true},
	{"sysctl set", "verb", "sysctl.set", "set a sysctl", true},
	{"config read", "verb", "config.read", "read a config", true},
	{"cat", "verb", "file.read", "read a file", true},
	{"ls", "verb", "file.read", "list a directory", true},
	{"cd", "builtin", "file.read", "change the prefix", true},
	{"pushd", "builtin", "file.read", "push the prefix", true},
	{"echo", "filter", "", "print arguments", true},
	{"egrep", "filter", "", "filter lines", true},
	{"head", "filter", "", "first lines", true},
	{"cut", "filter", "", "cut fields", true},
	{"wc", "filter", "", "count", true},
	{"where", "filter", "", "keep rows", true},
	{"select", "filter", "", "pick columns", true},
	{"rows", "filter", "", "lift text into rows", true},
	{"json", "filter", "", "print JSON", true},
	{"sort", "filter", "", "sort by col [desc], ...", true},
	{"uniq", "filter", "", "drop repeated rows, or by columns", true},
	{"group", "filter", "", "group by col; then count, sum, avg, min, max", true},
	{"count", "filter", "", "count rows, or per group", true},
	{"sum", "filter", "", "sum a column, or per group", true},
	{"avg", "filter", "", "average a column, or per group", true},
	{"min", "filter", "", "smallest of a column, or per group", true},
	{"max", "filter", "", "largest of a column, or per group", true},
	{"first", "filter", "", "first n rows", true},
	{"last", "filter", "", "last n rows", true},
	{"skip", "filter", "", "drop the first n rows", true},
	{"filter", "filter", "", "keep rows where [ expr ] holds; $it is the row", true},
	{"each", "filter", "", "compute [ expr ] per row; a map adds columns", true},
	{"lines", "filter", "", "text to rows with a line column", true},
	{"parse", "filter", "", "text to rows: \"{a}:{_}:{b}\" or re with (?P<name>...)", true},
	{"sub", "filter", "", "sub col re with repl: rewrite one column", true},
	{"from json", "filter", "", "parse JSON text into a value", true},
	{"to json", "filter", "", "print a value as JSON", true},
	{"from yaml", "filter", "", "parse YAML text into a value", true},
	{"to yaml", "filter", "", "print a value as YAML", true},
	{"from toml", "filter", "", "parse TOML text into a map", true},
	{"to toml", "filter", "", "print a map as TOML", true},
	{"from csv", "filter", "", "CSV text to rows; header first, or --noheader", true},
	{"to csv", "filter", "", "rows as CSV; formula cells prefixed", true},
	{"from tsv", "filter", "", "TSV text to rows", true},
	{"to tsv", "filter", "", "rows as TSV", true},
	{"let", "keyword", "", "bind a name", true},
	{"if", "keyword", "", "conditional", true},
	{"for", "keyword", "", "walk rows, a list, a map, or a range", true},
	{"fn", "keyword", "", "define a command", true},
	{"dirs", "builtin", "", "show the prefix stack", true},
	{"popd", "builtin", "", "pop the prefix", true},
	{"why", "session", "", "explain the grant check", true},
	{"while", "keyword", "", "loop while true", true},
	{"do", "keyword", "", "loop, body first", true},
	{"try", "keyword", "", "catch a failure", true},
	{"return", "keyword", "", "leave a function with a value", true},
	{"break", "keyword", "", "leave a loop", true},
	{"continue", "keyword", "", "next iteration", true},
	{"exit", "keyword", "", "end the script or session", true},
	{"builtin", "builtin", "", "list what sash understands", true},
	{"set", "builtin", "", "session options: -e -u -x -f, loopmax, verbmax", true},
	{"shopt", "builtin", "", "set by long name: -s on, -u off", true},
	{"declare", "keyword", "", "read-only or typed names: -r -i -a -A, -p", true},
	{"use", "keyword", "file.read", "load fn and declare -r from a file", true},
	{"template_file", "verb", "file.write", "render spec + tpl to out; --check diffs", true},
	{"template", "verb", "file.read", "render tpl with the piped value", true},
	{"with", "keyword", "", "with set -e, loopmax N, prefix P, x = v { ... }: restored after", true},
	{"else", "keyword", "", "the other branch of if", false},
	{"in", "keyword", "", "the source of for", false},
	{"catch", "keyword", "", "the handler of try", false},
	{"logout", "session", "", "end the session", false},
	{"bind", "session", "", "vi or emacs keys", false},
	{"syntax", "session", "", "line colors on or off", false},
	{"trace", "session", "", "print each stage key first", false},
	{"prompt", "session", "", "menu or line", false},
}

// Builtins returns a copy of the table.
func Builtins() []Builtin { return append([]Builtin(nil), builtins...) }

var builtinCols = []string{"name", "kind", "key", "desc"}

// runBuiltin lists the table, or the forms whose name starts with argv.
func runBuiltin(argv []string) (value, int) {
	want := strings.Join(argv[1:], " ")
	var rows []parent.Row
	for _, b := range builtins {
		if want != "" && b.Form != want && !strings.HasPrefix(b.Form, want+" ") {
			continue
		}
		key := b.Key
		if key == "" {
			key = "-"
		}
		rows = append(rows, parent.Row{"name": b.Form, "kind": b.Kind, "key": key, "desc": b.Desc})
	}
	// Expression functions are listed too; they live only inside [ ].
	for _, f := range funcNames() {
		if want != "" && f != want {
			continue
		}
		rows = append(rows, parent.Row{"name": f + "()", "kind": "function", "key": "-", "desc": "inside [ ]"})
	}
	if len(rows) == 0 {
		return value{kind: valRows, cols: builtinCols}, 1
	}
	return value{kind: valRows, rows: rows, cols: builtinCols}, 0
}
