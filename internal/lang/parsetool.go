package lang

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// parse turns lines of text into rows: a template with {name} fields, or
// parse re with named groups. A line that does not match is dropped. The
// template is compiled to an anchored RE2 pattern, so it runs in linear
// time. sub rewrites one column with a regex.

// templateRE compiles "{user}:{_}:{uid}". Literal text is quoted. Each field
// is shortest-match except the last, which takes the rest of the line. {_}
// matches and is dropped.
func templateRE(t string) (*regexp.Regexp, []string, error) {
	if len(t) > regexMax {
		return nil, nil, fmt.Errorf("usage parse: template too long")
	}
	var b strings.Builder
	var names []string
	seen := map[string]bool{}
	b.WriteString("^")
	rest := t
	fields := strings.Count(t, "{")
	n := 0
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(regexp.QuoteMeta(rest))
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return nil, nil, fmt.Errorf("usage parse: unclosed {")
		}
		b.WriteString(regexp.QuoteMeta(rest[:i]))
		name := rest[i+1 : i+j]
		n++
		capture := "(.*?)"
		if n == fields {
			capture = "(.*)"
		}
		switch {
		case name == "_":
			capture = "(?:" + capture[1:]
		case !rowName(name) || seen[name]:
			return nil, nil, fmt.Errorf("usage parse: field %q", name)
		default:
			seen[name] = true
			names = append(names, name)
		}
		b.WriteString(capture)
		rest = rest[i+j+1:]
	}
	b.WriteString("$")
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("usage parse: no fields")
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, nil, fmt.Errorf("usage parse")
	}
	return re, names, nil
}

func classifyParse(argv []string) *problem {
	switch {
	case len(argv) == 2:
		if _, _, err := templateRE(argv[1]); err != nil {
			return &problem{exitType, err.Error()}
		}
	case len(argv) == 3 && argv[1] == "re":
		re, err := compileRE(argv[2])
		if err != nil {
			return &problem{exitType, err.Error()}
		}
		if len(namedGroups(re)) == 0 {
			return &problem{exitType, "usage parse re: needs (?P<name>...)"}
		}
	default:
		return &problem{exitType, "usage parse"}
	}
	return nil
}

func namedGroups(re *regexp.Regexp) []string {
	var out []string
	for _, n := range re.SubexpNames() {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (m *machine) runParse(argv []string, in value) (value, int) {
	var re *regexp.Regexp
	var names []string
	var err error
	anchored := len(argv) == 2
	if anchored {
		re, names, err = templateRE(argv[1])
	} else {
		re, err = compileRE(argv[2])
		names = namedGroups(re)
	}
	if err != nil {
		m.err.WriteString(err.Error() + "\n")
		return value{}, exitType
	}
	lines := splitLines(asBytes(in))
	if len(lines) > m.st.loopMax {
		m.ctl = ctlHalt
		m.err.WriteString("loop max\n")
		return value{}, exitType
	}
	var rows []parent.Row
	for _, line := range lines {
		sub := re.FindStringSubmatch(line)
		if sub == nil {
			continue
		}
		row := parent.Row{}
		if anchored {
			k := 0
			for i := 1; i < len(sub); i++ {
				row[names[k]] = sub[i]
				k++
			}
		} else {
			for i, n := range re.SubexpNames() {
				if n != "" {
					row[n] = sub[i]
				}
			}
		}
		rows = append(rows, row)
	}
	return value{kind: valRows, rows: rows, cols: names}, 0
}

// sub col re with repl
func classifySub(argv []string) *problem {
	if len(argv) != 5 || argv[3] != "with" {
		return &problem{exitType, "usage sub col re with repl"}
	}
	if _, err := compileRE(argv[2]); err != nil {
		return &problem{exitType, err.Error()}
	}
	return nil
}

func (m *machine) runSub(argv []string, in value) (value, int) {
	re, err := compileRE(argv[2])
	if err != nil {
		m.err.WriteString(err.Error() + "\n")
		return value{}, exitType
	}
	col, repl := argv[1], argv[4]
	out := make([]parent.Row, len(in.rows))
	total := 0
	for i, r := range in.rows {
		nr := parent.Row{}
		for k, v := range r {
			nr[k] = v
		}
		nr[col] = re.ReplaceAllString(r[col], repl)
		total += len(nr[col])
		if total > dataMaxBytes {
			m.err.WriteString("type sub too large\n")
			return value{}, exitType
		}
		out[i] = nr
	}
	return value{kind: valRows, rows: out, cols: in.cols}, 0
}
