package lang

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// from and to turn text into values and back, in process, with no key.
// Input is capped at the 8 MiB value bound and nesting at depth 64. A
// decoded string is data: nothing here parses it as sash source.

var formatNames = map[string]bool{"json": true, "yaml": true}

func runFrom(format string, in value) (value, error) {
	text := asBytes(in)
	if len(text) > dataMaxBytes {
		return value{}, fmt.Errorf("%s too large", format)
	}
	var d any
	var err error
	switch format {
	case "json":
		d, err = decodeJSON([]byte(text))
	case "yaml":
		d, err = decodeYAML([]byte(text))
	default:
		return value{}, fmt.Errorf("usage from")
	}
	if err != nil {
		return value{}, err
	}
	if dataSize(d, 0) > dataMaxBytes {
		return value{}, fmt.Errorf("%s too large", format)
	}
	return value{kind: valData, d: d}, nil
}

func runTo(format string, in value) (value, error) {
	d := toDatum(in)
	var out []byte
	var err error
	switch format {
	case "json":
		out, err = encodeJSONIndent(d)
	case "yaml":
		out, err = encodeYAML(d)
	default:
		return value{}, fmt.Errorf("usage to")
	}
	if err != nil {
		return value{}, err
	}
	return value{kind: valBytes, b: out}, nil
}

// decodeJSON keeps object key order and reads whole numbers as int64.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	d, err := jsonValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("json trailing data")
	}
	return d, nil
}

func jsonValue(dec *json.Decoder, depth int) (any, error) {
	if depth > exprDepthMax {
		return nil, fmt.Errorf("json too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("json %s", jsonErr(err))
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '[':
			list := []any{}
			for dec.More() {
				v, err := jsonValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("json %s", jsonErr(err))
			}
			return list, nil
		case '{':
			o := newOmap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("json %s", jsonErr(err))
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("json key")
				}
				v, err := jsonValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("json %s", jsonErr(err))
			}
			return o, nil
		}
		return nil, fmt.Errorf("json syntax")
	case json.Number:
		if n, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			return nil, fmt.Errorf("json number")
		}
		return f, nil
	case string, bool, nil:
		return t, nil
	}
	return nil, fmt.Errorf("json syntax")
}

// jsonErr names the failure without echoing the input.
func jsonErr(err error) string {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "truncated"
	}
	return "syntax"
}

// encodeJSONIndent writes JSON with two-space indent, in insertion order.
func encodeJSONIndent(d any) ([]byte, error) {
	var b strings.Builder
	writeJSON(&b, d)
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(b.String()), "", "  "); err != nil {
		return nil, fmt.Errorf("json encode")
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// datumRows reads a list of maps as rows, for a |> after from. Columns are
// the keys in first-seen order. A nested value is its JSON text.
func datumRows(d any) (value, bool) {
	list, ok := d.([]any)
	if !ok {
		if o, isMap := d.(*omap); isMap {
			list = []any{o}
		} else {
			return value{}, false
		}
	}
	var cols []string
	seen := map[string]bool{}
	rows := make([]parent.Row, 0, len(list))
	for _, e := range list {
		o, ok := e.(*omap)
		if !ok {
			return value{}, false
		}
		row := parent.Row{}
		for _, k := range o.keys {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
			row[k] = textOf(o.m[k])
		}
		rows = append(rows, row)
	}
	return value{kind: valRows, rows: rows, cols: cols}, true
}
