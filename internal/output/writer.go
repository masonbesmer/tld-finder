// Package output writes Results as csv, jsonl, or a human-readable table.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/masonbesmer/tld-finder/internal/check"
)

var csvHeader = []string{"input", "domain", "tld", "status", "authority", "checked_at", "latency_ms", "expiry", "note", "error"}

type Writer struct {
	only map[check.Status]bool
	csv  *csv.Writer
	tab  *tabwriter.Writer
	enc  *json.Encoder
}

// New returns a writer for format ("csv", "jsonl", or "table"). The CSV header
// and table header are written immediately, so they appear even with zero rows.
// only filters emitted statuses; empty means all.
func New(w io.Writer, format string, only []check.Status) (*Writer, error) {
	o := &Writer{only: map[check.Status]bool{}}
	for _, s := range only {
		o.only[s] = true
	}
	switch format {
	case "csv":
		o.csv = csv.NewWriter(w)
		return o, o.csv.Write(csvHeader)
	case "jsonl":
		o.enc = json.NewEncoder(w)
		return o, nil
	case "table":
		o.tab = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		_, err := fmt.Fprintln(o.tab, "DOMAIN\tSTATUS\tEXPIRY\tAUTHORITY\tNOTE")
		return o, err
	default:
		return nil, fmt.Errorf("invalid format %q (want csv, jsonl, or table)", format)
	}
}

func (o *Writer) Write(r check.Result) error {
	if len(o.only) > 0 && !o.only[r.Status] {
		return nil
	}
	expiry := ""
	if r.Expiry != nil {
		expiry = r.Expiry.UTC().Format(time.RFC3339)
	}
	switch {
	case o.csv != nil:
		return o.csv.Write([]string{
			r.Input, r.Domain, r.TLD, string(r.Status), r.Authority,
			r.CheckedAt.UTC().Format(time.RFC3339),
			strconv.FormatInt(r.Latency.Milliseconds(), 10),
			expiry, r.Note, r.Err,
		})
	case o.enc != nil:
		return o.enc.Encode(r)
	default:
		note := r.Note
		if r.Err != "" {
			note = r.Err
		}
		_, err := fmt.Fprintf(o.tab, "%s\t%s\t%s\t%s\t%s\n", r.Domain, r.Status, expiry, r.Authority, note)
		return err
	}
}

func (o *Writer) Flush() error {
	switch {
	case o.csv != nil:
		o.csv.Flush()
		return o.csv.Error()
	case o.tab != nil:
		return o.tab.Flush()
	}
	return nil
}
