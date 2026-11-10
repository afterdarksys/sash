package lang

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

// decodeTOML reads a TOML document as a map in document order. Dates and
// times become RFC 3339 strings. Nesting deeper than 64 is refused before
// the parser sees it. Errors do not echo the input.
func decodeTOML(b []byte) (any, error) {
	if tomlTooDeep(b) {
		return nil, fmt.Errorf("toml too deep")
	}
	var raw map[string]any
	md, err := toml.NewDecoder(bytes.NewReader(b)).Decode(&raw)
	if err != nil {
		return nil, fmt.Errorf("toml syntax")
	}
	order := map[string]int{}
	for i, k := range md.Keys() {
		if _, ok := order[k.String()]; !ok {
			order[k.String()] = i
		}
	}
	return tomlValue(raw, "", order, 0)
}

// tomlTooDeep counts [ and { outside strings.
func tomlTooDeep(b []byte) bool {
	depth := 0
	var q byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if q != 0 {
			if q == '"' && c == '\\' {
				i++
				continue
			}
			if c == q || c == '\n' {
				q = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			q = c
		case '#':
			for i < len(b) && b[i] != '\n' {
				i++
			}
		case '[', '{':
			depth++
			if depth > exprDepthMax {
				return true
			}
		case ']', '}':
			depth--
		}
	}
	return false
}

func tomlValue(v any, at string, order map[string]int, depth int) (any, error) {
	if depth > exprDepthMax {
		return nil, fmt.Errorf("toml too deep")
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		pos := func(k string) int {
			if i, ok := order[tomlPath(at, k)]; ok {
				return i
			}
			return math.MaxInt
		}
		sort.SliceStable(keys, func(i, j int) bool {
			if pi, pj := pos(keys[i]), pos(keys[j]); pi != pj {
				return pi < pj
			}
			return keys[i] < keys[j]
		})
		o := newOmap()
		for _, k := range keys {
			d, err := tomlValue(x[k], tomlPath(at, k), order, depth+1)
			if err != nil {
				return nil, err
			}
			o.set(k, d)
		}
		return o, nil
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			d, err := tomlValue(e, at, order, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			d, err := tomlValue(e, at, order, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, nil
	case int64, bool, string:
		return x, nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return nil, fmt.Errorf("toml float")
		}
		return x, nil
	case time.Time:
		return x.Format(time.RFC3339Nano), nil
	case fmt.Stringer:
		// toml.LocalDate, LocalTime, LocalDateTime.
		return x.String(), nil
	}
	return nil, fmt.Errorf("toml value")
}

// tomlPath matches toml.Key.String for the order lookup.
func tomlPath(at, k string) string {
	key := toml.Key{k}.String()
	if at == "" {
		return key
	}
	return at + "." + key
}

// encodeTOML writes a map as TOML. TOML has no null and its encoder sorts
// keys, so a null is refused and keys come out sorted.
func encodeTOML(d any) ([]byte, error) {
	if _, ok := d.(*omap); !ok {
		return nil, fmt.Errorf("toml needs a map")
	}
	g, err := tomlGo(d, 0)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(g); err != nil {
		return nil, fmt.Errorf("toml encode")
	}
	return b.Bytes(), nil
}

func tomlGo(d any, depth int) (any, error) {
	if depth > exprDepthMax {
		return nil, fmt.Errorf("toml too deep")
	}
	switch x := d.(type) {
	case nil:
		return nil, fmt.Errorf("toml has no null")
	case *omap:
		m := make(map[string]any, len(x.keys))
		for _, k := range x.keys {
			v, err := tomlGo(x.m[k], depth+1)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		return m, nil
	case []any:
		allMaps := len(x) > 0
		out := make([]any, 0, len(x))
		for _, e := range x {
			v, err := tomlGo(e, depth+1)
			if err != nil {
				return nil, err
			}
			if _, ok := v.(map[string]any); !ok {
				allMaps = false
			}
			out = append(out, v)
		}
		if allMaps {
			tables := make([]map[string]any, len(out))
			for i, v := range out {
				tables[i] = v.(map[string]any)
			}
			return tables, nil
		}
		return out, nil
	}
	return d, nil
}
