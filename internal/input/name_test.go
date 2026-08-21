package input

import (
	"log/slog"
	"strings"
	"testing"
)

var discard = slog.New(slog.DiscardHandler)

func fqdns(rows []Row, tlds []string) []string {
	var got []string
	for _, d := range Expand(rows, tlds, discard) {
		got = append(got, d.FQDN)
	}
	return got
}

func one(name string) []Row { return []Row{{Name: name}} }

func TestExpand(t *testing.T) {
	tests := []struct {
		name string
		rows []Row
		tlds []string
		want []string
	}{
		{"cartesian", one("acme"), []string{"com", "io"}, []string{"acme.com", "acme.io"}},
		{"uppercase and trim", one(" ACME "), []string{"com"}, []string{"acme.com"}},
		{"internal spaces", one("north star"), []string{"com"}, []string{"north-star.com"}},
		{"idn", one("café"), []string{"com"}, []string{"xn--caf-dma.com"}},
		{"already qualified", one("acme.io"), []string{"com", "io"}, []string{"acme.io"}},
		{"unknown suffix keeps dots", one("acme.corp"), []string{"com"}, []string{"acme.corp.com"}},
		{"trailing dot", one("acme."), []string{"com"}, []string{"acme.com"}},
		{"junk characters dropped", one("ac_me!"), []string{"com"}, []string{"acme.com"}},
		{"xn-- allowed", one("xn--caf-dma"), []string{"com"}, []string{"xn--caf-dma.com"}},
		{"hyphens 3-4 rejected", one("ab--cd"), []string{"com"}, nil},
		{"leading hyphen rejected", one("-acme"), []string{"com"}, nil},
		{"overlong label rejected", one(strings.Repeat("a", 64)), []string{"com"}, nil},
		{"emoji punycoded", one("☕"), []string{"com"}, []string{"xn--53h.com"}},
		{"empty cell skipped", one(""), []string{"com"}, nil},
		{"dedupe", []Row{{Name: "acme"}, {Name: "ACME"}}, []string{"com"}, []string{"acme.com"}},
		{"per-row tlds add to global", []Row{{Name: "acme", TLDs: []string{"dev"}}}, []string{"com"}, []string{"acme.com", "acme.dev"}},
		{"dotted cell with no tlds is full domain", one("foo.bar"), nil, []string{"foo.bar"}},
		{"bare cell with no tlds skipped", one("acme"), nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fqdns(tt.rows, tt.tlds)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestExpandFieldsAndTLD(t *testing.T) {
	ds := Expand([]Row{{Name: " Café "}}, []string{"com"}, discard)
	if len(ds) != 1 {
		t.Fatalf("got %d domains", len(ds))
	}
	d := ds[0]
	if d.Input != " Café " || d.FQDN != "xn--caf-dma.com" || d.TLD != "com" {
		t.Fatalf("got %+v", d)
	}
}

func TestNormalizeTLD(t *testing.T) {
	for in, want := range map[string]string{".COM": "com", "io": "io", "京都": "xn--1lqs03n"} {
		got, err := NormalizeTLD(in)
		if err != nil || got != want {
			t.Errorf("NormalizeTLD(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeTLD(""); err == nil {
		t.Error("NormalizeTLD(\"\") should fail")
	}
}
