package output

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masonbesmer/tld-finder/internal/check"
)

var update = flag.Bool("update", false, "rewrite golden files")

func fixtures() []check.Result {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	exp := time.Date(2027, 8, 13, 4, 0, 0, 0, time.UTC)
	return []check.Result{
		{Input: "acme", Domain: "acme.com", TLD: "com", Status: check.StatusAvailable, Authority: "https://rdap.example/v1", CheckedAt: at, Latency: 123 * time.Millisecond},
		{Input: "acme", Domain: "acme.io", TLD: "io", Status: check.StatusRegistered, Authority: "https://rdap.example/v1", CheckedAt: at, Latency: 45 * time.Millisecond, Expiry: &exp, Note: "pending delete"},
		{Input: "café", Domain: "xn--caf-dma.de", TLD: "de", Status: check.StatusUnknown, CheckedAt: at, Note: "no RDAP endpoint for .de"},
		{Input: "boom", Domain: "boom.com", TLD: "com", Status: check.StatusError, CheckedAt: at, Err: "unexpected status 500"},
	}
}

func render(t *testing.T, format string, only []check.Status, results []check.Result) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := New(&buf, format, only)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGolden(t *testing.T) {
	for _, format := range []string{"csv", "jsonl", "table", "list"} {
		t.Run(format, func(t *testing.T) {
			got := render(t, format, nil, fixtures())
			path := filepath.Join("..", "..", "testdata", "golden", "results."+format)
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("golden mismatch:\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestOnlyFilter(t *testing.T) {
	got := render(t, "csv", []check.Status{check.StatusAvailable}, fixtures())
	lines := bytes.Count(got, []byte("\n"))
	if lines != 2 { // header + 1 available row
		t.Fatalf("expected 2 lines, got %d:\n%s", lines, got)
	}
}

func TestHeaderWithZeroRows(t *testing.T) {
	got := render(t, "csv", nil, nil)
	want := "input,domain,tld,status,authority,checked_at,latency_ms,expiry,note,error\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
}

func TestInvalidFormat(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "xml", nil); err == nil {
		t.Fatal("expected error")
	}
}
