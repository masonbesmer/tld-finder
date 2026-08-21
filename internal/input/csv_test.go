package input

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readFile(t *testing.T, name string, opt CSVOptions) []Row {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "input", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := ReadCSV(f, opt)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func names(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func TestReadCSV(t *testing.T) {
	tests := []struct {
		file string
		opt  CSVOptions
		want []string
	}{
		{"header.csv", CSVOptions{}, []string{"acme", "north star", "café", "ACME ", "acme.io"}},
		{"noheader.csv", CSVOptions{}, []string{"acme", "widget"}},
		{"noheader.csv", CSVOptions{Column: "0"}, []string{"acme", "widget"}},
		{"semicolon.csv", CSVOptions{Delimiter: ';'}, []string{"acme"}},
		{"tab.csv", CSVOptions{Delimiter: '\t'}, []string{"acme"}},
		{"bom_crlf.csv", CSVOptions{}, []string{"acme"}},
		{"quoted.csv", CSVOptions{}, []string{"a,b"}},
		{"header.csv", CSVOptions{HasHeader: "false"}, []string{"name", "acme", "north star", "café", "ACME ", "acme.io"}},
	}
	for _, tt := range tests {
		got := names(readFile(t, tt.file, tt.opt))
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s %+v: got %v, want %v", tt.file, tt.opt, got, tt.want)
		}
	}
}

func TestReadCSVTLDColumn(t *testing.T) {
	rows := readFile(t, "tldcol.csv", CSVOptions{TLDColumn: "tlds"})
	want := []Row{
		{Name: "acme", TLDs: []string{"com", "io"}},
		{Name: "widget", TLDs: []string{"dev"}},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %+v, want %+v", rows, want)
	}
}

func TestReadCSVColumnByName(t *testing.T) {
	rows := readFile(t, "header.csv", CSVOptions{Column: "notes"})
	if rows[0].Name != "client A" {
		t.Fatalf("got %+v", rows[0])
	}
}

func TestReadCSVMissingColumn(t *testing.T) {
	f, _ := os.Open(filepath.Join("..", "..", "testdata", "input", "header.csv"))
	defer f.Close()
	if _, err := ReadCSV(f, CSVOptions{Column: "nope"}); err == nil {
		t.Fatal("expected error for missing column")
	}
}
