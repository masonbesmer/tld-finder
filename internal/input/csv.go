// Package input reads candidate names from CSV and expands them into FQDNs.
package input

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// autoColumns are tried in order when --column is not given.
var autoColumns = []string{"name", "label", "domain", "keyword", "sld"}

type Row struct {
	Name string
	TLDs []string // from --tld-column; may be nil
}

type CSVOptions struct {
	Delimiter rune   // 0 means ','
	HasHeader string // "auto", "true", or "false"
	Column    string // column name or 0-based index; "" = auto
	TLDColumn string // optional per-row TLD column
}

func ReadCSV(r io.Reader, opt CSVOptions) ([]Row, error) {
	cr := csv.NewReader(r)
	if opt.Delimiter != 0 {
		cr.Comma = opt.Delimiter
	}
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	records[0][0] = strings.TrimPrefix(records[0][0], "\uFEFF")

	hasHeader, err := detectHeader(records[0], opt)
	if err != nil {
		return nil, err
	}
	var header []string
	if hasHeader {
		header = records[0]
		records = records[1:]
	}
	nameCol, err := resolveColumn(opt.Column, header, true)
	if err != nil {
		return nil, err
	}
	tldCol := -1
	if opt.TLDColumn != "" {
		if tldCol, err = resolveColumn(opt.TLDColumn, header, false); err != nil {
			return nil, err
		}
	}

	var rows []Row
	for _, rec := range records {
		if nameCol >= len(rec) {
			continue
		}
		row := Row{Name: rec[nameCol]}
		if tldCol >= 0 && tldCol < len(rec) {
			row.TLDs = splitTLDs(rec[tldCol])
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func detectHeader(first []string, opt CSVOptions) (bool, error) {
	switch opt.HasHeader {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "", "auto":
	default:
		return false, fmt.Errorf("invalid --has-header %q (want auto, true, or false)", opt.HasHeader)
	}
	known := map[string]bool{}
	for _, c := range autoColumns {
		known[c] = true
	}
	for _, spec := range []string{opt.Column, opt.TLDColumn} {
		if _, err := strconv.Atoi(spec); spec != "" && err != nil {
			known[strings.ToLower(spec)] = true
		}
	}
	for _, cell := range first {
		if known[strings.ToLower(strings.TrimSpace(cell))] {
			return true, nil
		}
	}
	return false, nil
}

// resolveColumn maps a name or 0-based index to an index. autoDefault falls
// back to the first known header name, then column 0.
func resolveColumn(spec string, header []string, autoDefault bool) (int, error) {
	find := func(name string) int {
		for i, h := range header {
			if strings.EqualFold(strings.TrimSpace(h), name) {
				return i
			}
		}
		return -1
	}
	if spec == "" {
		if !autoDefault {
			return -1, fmt.Errorf("no column given")
		}
		for _, name := range autoColumns {
			if i := find(name); i >= 0 {
				return i, nil
			}
		}
		return 0, nil
	}
	if n, err := strconv.Atoi(spec); err == nil {
		if n < 0 {
			return -1, fmt.Errorf("column index %d out of range", n)
		}
		return n, nil
	}
	if i := find(spec); i >= 0 {
		return i, nil
	}
	return -1, fmt.Errorf("column %q not found in header", spec)
}

func splitTLDs(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	})
}
