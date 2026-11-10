package lang

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// Row tools (language v3 contract): sort, uniq, group and its aggregates,
// first, last, skip, filter, each, lines. In process on |>, no key. filter
// and each evaluate a [ ] per row with $it bound to the row, and stop at
// loopmax rows like a loop.

var rowTools = map[string]bool{
	"sort": true, "uniq": true, "group": true, "count": true, "sum": true, "avg": true,
	"min": true, "max": true, "first": true, "last": true, "skip": true,
	"filter": true, "each": true,
}

// classifyRowTool checks the words of a row tool.
func classifyRowTool(argv []string) *problem {
	name := argv[0]
	bad := &problem{exitType, "usage " + name}
	switch name {
	case "sort":
		if len(argv) < 3 || argv[1] != "by" {
			return bad
		}
		for i := 2; i < len(argv); i++ {
			if (argv[i] == "asc" || argv[i] == "desc") && i == 2 {
				return bad
			}
		}
	case "uniq", "count":
		if name == "count" && len(argv) != 1 {
			return bad
		}
	case "group":
		if len(argv) != 3 || argv[1] != "by" {
			return bad
		}
	case "sum", "avg", "min", "max":
		if len(argv) != 2 {
			return bad
		}
	case "first", "last", "skip":
		if len(argv) != 2 {
			return bad
		}
		if n, err := strconv.Atoi(argv[1]); err != nil || n < 0 {
			return bad
		}
	case "filter", "each":
		if len(argv) != 2 || argv[1] != exprMark {
			return bad
		}
	}
	return nil
}

// exprMark stands in argv for the [ ] a filter or each keeps unevaluated.
const exprMark = "\x00expr"

// rowCompare orders two fields: as numbers when both are, else as text.
func rowCompare(a, b string) int {
	na, aok := number(a)
	nb, bok := number(b)
	if aok && bok {
		c, _ := dataCompare(na, nb)
		return c
	}
	return strings.Compare(a, b)
}

func (m *machine) runRowTool(st expStage, in value) (value, int) {
	fail := func(msg string) (value, int) {
		m.err.WriteString(msg + "\n")
		return value{}, exitType
	}
	if in.kind != valRows {
		return fail("type " + st.argv[0])
	}
	rows, cols := in.rows, in.cols
	out := func(r []parent.Row, c []string) (value, int) {
		return value{kind: valRows, rows: r, cols: c}, 0
	}
	switch st.argv[0] {
	case "sort":
		type key struct {
			col  string
			desc bool
		}
		var keys []key
		for i := 2; i < len(st.argv); i++ {
			if st.argv[i] == "desc" || st.argv[i] == "asc" {
				keys[len(keys)-1].desc = st.argv[i] == "desc"
				continue
			}
			keys = append(keys, key{col: st.argv[i]})
		}
		sorted := append([]parent.Row(nil), rows...)
		sort.SliceStable(sorted, func(i, j int) bool {
			for _, k := range keys {
				c := rowCompare(sorted[i][k.col], sorted[j][k.col])
				if c == 0 {
					continue
				}
				if k.desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
		return out(sorted, cols)
	case "uniq":
		by := st.argv[1:]
		if len(by) == 0 {
			by = cols
		}
		seen := map[string]bool{}
		var keep []parent.Row
		for _, r := range rows {
			parts := make([]string, len(by))
			for i, c := range by {
				parts[i] = r[c]
			}
			k := strings.Join(parts, "\x00")
			if !seen[k] {
				seen[k] = true
				keep = append(keep, r)
			}
		}
		return out(keep, cols)
	case "group":
		col := st.argv[2]
		idx := map[string]int{}
		var groups [][]parent.Row
		var gr []parent.Row
		for _, r := range rows {
			k := r[col]
			i, ok := idx[k]
			if !ok {
				i = len(groups)
				idx[k] = i
				groups = append(groups, nil)
				gr = append(gr, parent.Row{col: k})
			}
			groups[i] = append(groups[i], r)
		}
		for i := range gr {
			gr[i]["count"] = strconv.Itoa(len(groups[i]))
		}
		return value{kind: valRows, rows: gr, cols: []string{col, "count"}, groups: groups, gcol: col}, 0
	case "count", "sum", "avg", "min", "max":
		return m.aggregate(st.argv, in)
	case "first", "last", "skip":
		n, _ := strconv.Atoi(st.argv[1])
		if n > len(rows) {
			n = len(rows)
		}
		switch st.argv[0] {
		case "first":
			return out(rows[:n], cols)
		case "last":
			return out(rows[len(rows)-n:], cols)
		}
		return out(rows[n:], cols)
	case "filter", "each":
		return m.perRow(st, rows, cols)
	}
	return fail("usage " + st.argv[0])
}

func (m *machine) aggregate(argv []string, in value) (value, int) {
	name := argv[0]
	sets := in.groups
	if sets == nil {
		sets = [][]parent.Row{in.rows}
	}
	var outRows []parent.Row
	for i, set := range sets {
		row := parent.Row{}
		if in.groups != nil {
			row[in.gcol] = in.rows[i][in.gcol]
		}
		v, err := aggregateOne(name, argv, set)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return value{}, exitType
		}
		row[name] = v
		outRows = append(outRows, row)
	}
	cols := []string{name}
	if in.groups != nil {
		cols = []string{in.gcol, name}
	}
	return value{kind: valRows, rows: outRows, cols: cols}, 0
}

func aggregateOne(name string, argv []string, set []parent.Row) (string, error) {
	if name == "count" {
		return strconv.Itoa(len(set)), nil
	}
	col := argv[1]
	if len(set) == 0 {
		return "", nil
	}
	if name == "min" || name == "max" {
		best := set[0][col]
		for _, r := range set[1:] {
			c := rowCompare(r[col], best)
			if (name == "min" && c < 0) || (name == "max" && c > 0) {
				best = r[col]
			}
		}
		return best, nil
	}
	var total any = int64(0)
	for _, r := range set {
		n, ok := number(r[col])
		if !ok {
			return "", fmt.Errorf("type %s %s", name, col)
		}
		var err error
		if total, err = arith("+", total, n); err != nil {
			return "", err
		}
	}
	if name == "avg" {
		return textOf(asFloat(total) / float64(len(set))), nil
	}
	return textOf(total), nil
}

// perRow runs filter or each: the stage's [ ] once per row, $it the row.
func (m *machine) perRow(st expStage, rows []parent.Row, cols []string) (value, int) {
	fail := func(msg string) (value, int) {
		m.err.WriteString(msg + "\n")
		return value{}, exitType
	}
	if len(rows) > m.st.loopMax {
		m.ctl = ctlHalt
		return fail("loop max")
	}
	outCols := append([]string(nil), cols...)
	seenCol := map[string]bool{}
	for _, c := range cols {
		seenCol[c] = true
	}
	var out []parent.Row
	scalar, mapped := false, false
	for _, r := range rows {
		it := rowDatum(r, cols)
		d, err := evalExprStrict(st.x, m.st.nounset, func(name string) (any, bool) {
			if name == "it" {
				return it, true
			}
			v, ok := m.vars[name]
			if !ok {
				return nil, false
			}
			return toDatum(v), true
		})
		if err != nil {
			return fail(err.Error())
		}
		if st.argv[0] == "filter" {
			if truthy(d) {
				out = append(out, r)
			}
			continue
		}
		if o, ok := d.(*omap); ok {
			mapped = true
			nr := parent.Row{}
			for k, v := range r {
				nr[k] = v
			}
			for _, k := range o.keys {
				nr[k] = textOf(o.m[k])
				if !seenCol[k] {
					seenCol[k] = true
					outCols = append(outCols, k)
				}
			}
			out = append(out, nr)
			continue
		}
		scalar = true
		out = append(out, parent.Row{"value": textOf(d)})
	}
	if scalar && mapped {
		return fail("type each: map and value mixed")
	}
	if scalar {
		outCols = []string{"value"}
	}
	return value{kind: valRows, rows: out, cols: outCols}, 0
}
