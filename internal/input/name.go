package input

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/net/idna"
)

type Domain struct {
	Input string // original CSV cell, verbatim
	FQDN  string // normalized A-label FQDN, no trailing dot
	TLD   string
}

// NormalizeTLD lowercases, strips a leading dot, and converts IDN TLDs to A-labels.
func NormalizeTLD(s string) (string, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	if s == "" {
		return "", errors.New("empty TLD")
	}
	return idna.Lookup.ToASCII(s)
}

// Expand produces the deduplicated cartesian product of rows × TLDs, in input
// order. A cell whose suffix is already a configured TLD (or any dotted cell
// when no TLDs are configured) is treated as a full domain. Invalid rows are
// skipped with a warning.
func Expand(rows []Row, tlds []string, log *slog.Logger) []Domain {
	var out []Domain
	seen := map[string]bool{}
	emit := func(input, candidate string) {
		fqdn, ok := normalizeFQDN(candidate, input, log)
		if !ok || seen[fqdn] {
			return
		}
		seen[fqdn] = true
		out = append(out, Domain{Input: input, FQDN: fqdn, TLD: fqdn[strings.LastIndex(fqdn, ".")+1:]})
	}

	for _, row := range rows {
		eff := tlds
		for _, t := range row.TLDs {
			nt, err := NormalizeTLD(t)
			if err != nil {
				log.Warn("skipping invalid TLD", "tld", t, "err", err)
				continue
			}
			eff = append(eff[:len(eff):len(eff)], nt)
		}

		cell := strings.ToLower(strings.TrimSpace(row.Name))
		if cell == "" {
			continue
		}
		if joined := strings.Join(strings.Fields(cell), "-"); joined != cell {
			log.Debug("replaced internal spaces", "input", row.Name, "result", joined)
			cell = joined
		}

		if trimmed := strings.TrimSuffix(cell, "."); strings.Contains(trimmed, ".") {
			suffix := trimmed[strings.LastIndex(trimmed, ".")+1:]
			if nt, err := NormalizeTLD(suffix); err == nil && (len(eff) == 0 || contains(eff, nt)) {
				emit(row.Name, trimmed)
				continue
			}
		}
		if len(eff) == 0 {
			log.Warn("skipping row: no TLDs configured", "input", row.Name)
			continue
		}
		for _, tld := range eff {
			emit(row.Name, strings.TrimSuffix(cell, ".")+"."+tld)
		}
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func normalizeFQDN(s, input string, log *slog.Logger) (string, bool) {
	cleaned := strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r >= 0x80 {
			return r
		}
		return -1
	}, s)
	if cleaned != s {
		log.Debug("dropped disallowed characters", "input", input, "result", cleaned)
	}
	ascii, err := idna.Lookup.ToASCII(cleaned)
	if err != nil {
		log.Warn("skipping row: IDNA conversion failed", "input", input, "err", err)
		return "", false
	}
	if err := validateFQDN(ascii); err != nil {
		log.Warn("skipping row: invalid domain", "input", input, "domain", ascii, "err", err)
		return "", false
	}
	return ascii, true
}

func validateFQDN(s string) error {
	if len(s) > 253 {
		return fmt.Errorf("FQDN exceeds 253 octets")
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("label %q not 1-63 octets", label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("label %q has leading or trailing hyphen", label)
		}
		if len(label) >= 4 && label[2:4] == "--" && !strings.HasPrefix(label, "xn--") {
			return fmt.Errorf("label %q has hyphens in positions 3-4", label)
		}
	}
	return nil
}
