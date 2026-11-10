package lang

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// A datum is the v2 value tree: nil, bool, int64, float64, string, []any, or
// *omap. It comes from a [ ] expression or a from-format filter. It is data:
// nothing here parses a string as sash source.

// omap is a map that keeps insertion order, so output is stable.
type omap struct {
	keys []string
	m    map[string]any
}

func newOmap() *omap { return &omap{m: map[string]any{}} }

func (o *omap) set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *omap) get(k string) (any, bool) {
	v, ok := o.m[k]
	return v, ok
}

// dataMaxBytes bounds one datum built by an expression: the same 8 MiB as one
// verb result.
const dataMaxBytes = 8 << 20

// exprDepthMax bounds nesting in a [ ] expression and in decoded data.
const exprDepthMax = 64

func rowDatum(row parent.Row, cols []string) *omap {
	o := newOmap()
	if len(cols) == 0 {
		for k := range row {
			cols = append(cols, k)
		}
		sort.Strings(cols)
	}
	for _, c := range cols {
		o.set(c, row[c])
	}
	return o
}

// toDatum views a value as data. Rows become a list of maps in column order.
func toDatum(v value) any {
	switch v.kind {
	case valStr:
		return v.s
	case valBytes:
		return string(v.b)
	case valRow:
		return rowDatum(v.row, nil)
	case valRows:
		out := make([]any, len(v.rows))
		for i, r := range v.rows {
			out[i] = rowDatum(r, v.cols)
		}
		return out
	case valData:
		return v.d
	}
	return nil
}

// fromDatum stores a datum. A string stays a plain string value.
func fromDatum(d any) value {
	if s, ok := d.(string); ok {
		return value{kind: valStr, s: s}
	}
	return value{kind: valData, d: d}
}

func typeName(d any) string {
	switch d.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int64, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "list"
	case *omap:
		return "map"
	}
	return "unknown"
}

// textOf is a datum as one word: scalars as text, lists and maps as JSON.
func textOf(d any) string {
	switch x := d.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return formatFloat(x)
	case string:
		return x
	}
	var b strings.Builder
	writeJSON(&b, d)
	return b.String()
}

func formatFloat(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "null"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// writeJSON writes compact JSON in insertion order.
func writeJSON(b *strings.Builder, d any) {
	switch x := d.(type) {
	case nil:
		b.WriteString("null")
	case bool, int64, float64:
		b.WriteString(textOf(x))
	case string:
		b.WriteString(jsonString(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSON(b, e)
		}
		b.WriteByte(']')
	case *omap:
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsonString(k))
			b.WriteByte(':')
			writeJSON(b, x.m[k])
		}
		b.WriteByte('}')
	}
}

func jsonString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func truthy(d any) bool {
	switch x := d.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *omap:
		return len(x.keys) > 0
	}
	return false
}

// number reads a number, or a string that is exactly a decimal number. Row
// fields are strings, so `[ $r.pid > 100 ]` compares numbers.
func number(d any) (any, bool) {
	switch x := d.(type) {
	case int64, float64:
		return x, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" || s != x {
			return nil, false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
		if strings.ContainsAny(s, "xXpP_") {
			return nil, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return f, true
		}
	}
	return nil, false
}

func asFloat(n any) float64 {
	if i, ok := n.(int64); ok {
		return float64(i)
	}
	return n.(float64)
}

func dataEqual(a, b any) bool {
	if na, ok := number(a); ok {
		if nb, ok := number(b); ok {
			ia, aInt := na.(int64)
			ib, bInt := nb.(int64)
			if aInt && bInt {
				return ia == ib
			}
			return asFloat(na) == asFloat(nb)
		}
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !dataEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case *omap:
		y, ok := b.(*omap)
		if !ok || len(x.keys) != len(y.keys) {
			return false
		}
		for _, k := range x.keys {
			v, ok := y.m[k]
			if !ok || !dataEqual(x.m[k], v) {
				return false
			}
		}
		return true
	}
	return false
}

// dataCompare orders two numbers or two strings.
func dataCompare(a, b any) (int, error) {
	if na, ok := number(a); ok {
		if nb, ok := number(b); ok {
			ia, aInt := na.(int64)
			ib, bInt := nb.(int64)
			if aInt && bInt {
				return cmpInt(ia, ib), nil
			}
			fa, fb := asFloat(na), asFloat(nb)
			switch {
			case fa < fb:
				return -1, nil
			case fa > fb:
				return 1, nil
			}
			return 0, nil
		}
	}
	sa, aok := a.(string)
	sb, bok := b.(string)
	if aok && bok {
		return strings.Compare(sa, sb), nil
	}
	return 0, fmt.Errorf("type expr compare %s %s", typeName(a), typeName(b))
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// dataSize is a cheap upper bound on the bytes a datum renders to.
func dataSize(d any, depth int) int {
	if depth > exprDepthMax {
		return dataMaxBytes + 1
	}
	switch x := d.(type) {
	case string:
		return len(x) + 2
	case []any:
		n := 2
		for _, e := range x {
			n += dataSize(e, depth+1) + 1
			if n > dataMaxBytes {
				return n
			}
		}
		return n
	case *omap:
		n := 2
		for _, k := range x.keys {
			n += len(k) + 4 + dataSize(x.m[k], depth+1)
			if n > dataMaxBytes {
				return n
			}
		}
		return n
	}
	return 24
}
