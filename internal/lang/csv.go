package lang

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/parent"
)

// from csv and from tsv read the first line as the header (--noheader names
// columns c1, c2, ...). A small file can hold very many rows, so every row
// and cell is charged as it is read. to csv and to tsv write rows, and
// prefix a cell a spreadsheet would run as a formula.

func decodeCSV(text string, sep rune, header bool) (value, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = sep
	r.FieldsPerRecord = -1
	r.ReuseRecord = false
	c := &evalCtx{budget: 4 * dataMaxBytes}
	var cols []string
	var rows []parent.Row
	for line := 0; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return value{}, fmt.Errorf("csv syntax")
		}
		// A short row is padded to the header, so charge the header width.
		width := len(rec)
		if len(cols) > width {
			width = len(cols)
		}
		size := 64 + 32*width
		for _, f := range rec {
			size += len(f)
		}
		if err := charge(c, size); err != nil {
			return value{}, fmt.Errorf("csv too large")
		}
		if cols == nil {
			cols = make([]string, len(rec))
			seen := map[string]bool{}
			for i := range rec {
				name := "c" + strconv.Itoa(i+1)
				if header && rec[i] != "" {
					name = rec[i]
				}
				if seen[name] {
					return value{}, fmt.Errorf("csv duplicate column")
				}
				seen[name] = true
				cols[i] = name
			}
			if header {
				continue
			}
		}
		if len(rec) > len(cols) {
			return value{}, fmt.Errorf("csv row %d is wider than the header", line+1)
		}
		row := parent.Row{}
		for i, col := range cols {
			if i < len(rec) {
				row[col] = rec[i]
			} else {
				row[col] = ""
			}
		}
		rows = append(rows, row)
	}
	return value{kind: valRows, rows: rows, cols: cols}, nil
}

// csvSafe keeps a spreadsheet from running a cell (CSV injection). A cell
// that is a plain number is left alone: it cannot be a formula.
// Leading spaces are skipped first, since some importers trim them, and
// the fullwidth forms count too, since some normalize them.
func csvSafe(s string) string {
	t := strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsSpace(r) && r != '\t' && r != '\r' })
	if t == "" {
		return s
	}
	r, _ := utf8.DecodeRuneInString(t)
	if !strings.ContainsRune("=+-@\t\r\uFF1D\uFF0B\uFF0D\uFF20", r) {
		return s
	}
	if _, ok := number(s); ok {
		return s
	}
	return "'" + s
}

func encodeCSV(in value, sep rune) ([]byte, error) {
	v := in
	if v.kind == valData {
		rows, ok := datumRows(v.d)
		if !ok {
			return nil, fmt.Errorf("csv needs rows")
		}
		v = rows
	}
	if v.kind != valRows {
		return nil, fmt.Errorf("csv needs rows")
	}
	cols := v.cols
	if len(cols) == 0 && len(v.rows) > 0 {
		cols = rowDatum(v.rows[0], nil).keys
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Comma = sep
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = csvSafe(c)
	}
	if err := w.Write(head); err != nil {
		return nil, fmt.Errorf("csv encode")
	}
	for _, r := range v.rows {
		rec := make([]string, len(cols))
		for i, c := range cols {
			rec[i] = csvSafe(r[c])
		}
		if err := w.Write(rec); err != nil {
			return nil, fmt.Errorf("csv encode")
		}
		if b.Len() > dataMaxBytes {
			return nil, fmt.Errorf("csv too large")
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("csv encode")
	}
	return b.Bytes(), nil
}
