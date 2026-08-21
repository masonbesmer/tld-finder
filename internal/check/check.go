// Package check queries RDAP for domain registration status.
package check

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Status string

const (
	StatusAvailable  Status = "available" // registry has no object for this name
	StatusRegistered Status = "registered"
	StatusReserved   Status = "reserved" // registry-blocked, not buyable
	StatusUnknown    Status = "unknown"  // no RDAP endpoint for this TLD
	StatusError      Status = "error"    // transport/parse failure after retries
)

type Result struct {
	Input     string        `json:"input"`  // original CSV cell
	Domain    string        `json:"domain"` // normalized A-label FQDN, no trailing dot
	TLD       string        `json:"tld"`
	Status    Status        `json:"status"`
	Authority string        `json:"authority"` // RDAP base URL that answered
	CheckedAt time.Time     `json:"checked_at"`
	Latency   time.Duration `json:"latency_ms"`
	Expiry    *time.Time    `json:"expiry,omitempty"` // when registered
	Note      string        `json:"note,omitempty"`
	Err       string        `json:"error,omitempty"`
}

// MarshalJSON emits Latency in milliseconds, matching its latency_ms tag.
func (r Result) MarshalJSON() ([]byte, error) {
	type alias Result
	return json.Marshal(struct {
		alias
		Latency int64 `json:"latency_ms"`
	}{alias(r), r.Latency.Milliseconds()})
}

// ErrNoRDAP means the TLD has no RDAP endpoint in the bootstrap registry.
var ErrNoRDAP = errors.New("no RDAP endpoint")

// RateLimitError is returned on HTTP 429.
type RateLimitError struct {
	Host       string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited by %s (retry after %s)", e.Host, e.RetryAfter)
}
