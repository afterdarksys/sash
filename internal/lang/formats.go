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

// formatInputMax caps YAML and TOML input.
const formatInputMax = 1 << 20

var formatNames = map[string]bool{"json": true, "yaml": true, "toml": true, "csv": true, "tsv": true}

func runFrom(format string, in value) (value, error) {
	return runFromOpt(format, in, true)
}

func runFromOpt(format string, in value, header bool) (value, error) {
	text := asBytes(in)
	// YAML and TOML parsers build their whole tree first, at many times
	// the input, so their input is capped lower than JSON's, which is
	// charged as it streams.
	limit := dataMaxBytes
	if format == "yaml" || format == "toml" {
		limit = formatInputMax
	}
	if len(text) > limit {
		return value{}, fmt.Errorf("%s too large", format)
	}
	var d any
	var err error
	switch format {
	case "json":
		d, err = decodeJSON([]byte(text))
	case "yaml":
		d, err = decodeYAML([]byte(text))
	case "toml":
		d, err = decodeTOML([]byte(text))
	case "csv", "tsv":
		sep := ','
		if format == "tsv" {
			sep = '\t'
		}
		return decodeCSV(text, sep, header)
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
	if format == "csv" || format == "tsv" {
		sep := ','
		if format == "tsv" {
			sep = '\t'
		}
		out, err := encodeCSV(in, sep)
		if err != nil {
			return value{}, err
		}
		return value{kind: valBytes, b: out}, nil
	}
	d := toDatum(in)
	var out []byte
	var err error
	switch format {
	case "json":
		out, err = encodeJSONIndent(d)
	case "yaml":
		out, err = encodeYAML(d)
	case "toml":
		out, err = encodeTOML(d)
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
	d, err := jsonValue(dec, 0, &evalCtx{budget: dataMaxBytes})
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("json trailing data")
	}
	return d, nil
}

func jsonValue(dec *json.Decoder, depth int, c *evalCtx) (any, error) {
	if depth > exprDepthMax {
		return nil, fmt.Errorf("json too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("json %s", jsonErr(err))
	}
	size := 24
	if s, ok := tok.(string); ok {
		size += len(s)
	}
	if err := charge(c, size); err != nil {
		return nil, fmt.Errorf("json too large")
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '[':
			list := []any{}
			for dec.More() {
				v, err := jsonValue(dec, depth+1, c)
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
				v, err := jsonValue(dec, depth+1, c)
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
