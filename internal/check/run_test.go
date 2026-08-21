package check

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masonbesmer/tld-finder/internal/input"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

type sleepRecorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sleeps = append(s.sleeps, d)
	return nil
}

func newRunner(rt rtFunc, boot Bootstrap, retries int, rate time.Duration) (*Runner, *sleepRecorder) {
	rec := &sleepRecorder{}
	return &Runner{
		Client:  &Client{HTTP: &http.Client{Transport: rt}, Bootstrap: boot, Timeout: time.Second},
		Rate:    rate,
		Retries: retries,
		Log:     discard,
		sleep:   rec.sleep,
	}, rec
}

func dom(fqdn string) input.Domain {
	tld := fqdn[strings.LastIndex(fqdn, ".")+1:]
	return input.Domain{Input: fqdn, FQDN: fqdn, TLD: tld}
}

func collect(t *testing.T, r *Runner, domains ...input.Domain) []Result {
	t.Helper()
	var out []Result
	if err := r.Run(context.Background(), domains, func(res Result) { out = append(out, res) }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRunRetriesThenSucceeds(t *testing.T) {
	calls := 0
	r, _ := newRunner(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return resp(500, nil, ""), nil
		}
		return resp(404, nil, ""), nil
	}, Bootstrap{"com": {"https://rdap.example/rdap"}}, 2, time.Millisecond)
	out := collect(t, r, dom("a.com"))
	if calls != 3 || out[0].Status != StatusAvailable {
		t.Fatalf("calls=%d result=%+v", calls, out[0])
	}
}

func TestRunFailingDomainDoesNotAbort(t *testing.T) {
	calls := 0
	r, _ := newRunner(func(req *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(req.URL.Path, "a.com") {
			return resp(500, nil, ""), nil
		}
		return resp(404, nil, ""), nil
	}, Bootstrap{"com": {"https://rdap.example/rdap"}}, 1, time.Millisecond)
	out := collect(t, r, dom("a.com"), dom("b.com"))
	if len(out) != 2 {
		t.Fatalf("got %d results", len(out))
	}
	if out[0].Status != StatusError || out[0].Err == "" {
		t.Fatalf("first result: %+v", out[0])
	}
	if out[1].Status != StatusAvailable {
		t.Fatalf("second result: %+v", out[1])
	}
	if calls != 3 { // 2 attempts for a.com + 1 for b.com
		t.Fatalf("calls = %d", calls)
	}
}

func TestRun429BacksOffAndSlowsHost(t *testing.T) {
	rate := 200 * time.Millisecond
	calls := 0
	r, rec := newRunner(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			h := http.Header{}
			h.Set("Retry-After", "40")
			return resp(429, h, ""), nil
		}
		return resp(404, nil, ""), nil
	}, Bootstrap{"com": {"https://rdap.example/rdap"}}, 2, rate)
	out := collect(t, r, dom("a.com"))
	if out[0].Status != StatusAvailable || calls != 2 {
		t.Fatalf("calls=%d result=%+v", calls, out[0])
	}
	// Backoff honors Retry-After, and the host interval is doubled for the
	// paced second attempt.
	var sawRetryAfter, sawDoubled bool
	for _, d := range rec.sleeps {
		if d >= 40*time.Second {
			sawRetryAfter = true
		}
		if d > rate && d <= 2*rate {
			sawDoubled = true
		}
	}
	if !sawRetryAfter || !sawDoubled {
		t.Fatalf("sleeps = %v", rec.sleeps)
	}
}

func TestRunPacingKeysOnHostNotTLD(t *testing.T) {
	rate := 200 * time.Millisecond
	// Two TLDs served by one host, as with Verisign.
	boot := Bootstrap{"com": {"https://shared.example/com"}, "net": {"https://shared.example/net"}}
	r, rec := newRunner(func(req *http.Request) (*http.Response, error) {
		return resp(404, nil, ""), nil
	}, boot, 0, rate)
	collect(t, r, dom("a.com"), dom("a.net"))
	if len(rec.sleeps) != 1 || rec.sleeps[0] <= rate/2 || rec.sleeps[0] > rate {
		t.Fatalf("expected one pacing sleep near %v for the shared host, got %v", rate, rec.sleeps)
	}
}

func TestRunNoRDAPEmitsUnknown(t *testing.T) {
	calls := 0
	r, _ := newRunner(func(req *http.Request) (*http.Response, error) {
		calls++
		return resp(404, nil, ""), nil
	}, Bootstrap{}, 2, time.Millisecond)
	out := collect(t, r, dom("a.de"))
	if calls != 0 || out[0].Status != StatusUnknown || out[0].Note != "no RDAP endpoint for .de" {
		t.Fatalf("calls=%d result=%+v", calls, out[0])
	}
}

func TestRetryDelay(t *testing.T) {
	for range 20 {
		if d := retryDelay(0, errors.New("x")); d < 0 || d > 500*time.Millisecond {
			t.Fatalf("retryDelay(0) = %v", d)
		}
		if d := retryDelay(20, errors.New("x")); d > 30*time.Second {
			t.Fatalf("retryDelay(20) = %v exceeds cap", d)
		}
	}
	rl := &RateLimitError{RetryAfter: 40 * time.Second}
	if d := retryDelay(0, rl); d != 40*time.Second {
		t.Fatalf("Retry-After should win: %v", d)
	}
}

func TestRunInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := newRunner(func(req *http.Request) (*http.Response, error) {
		return resp(404, nil, ""), nil
	}, Bootstrap{"com": {"https://rdap.example/rdap"}}, 0, time.Millisecond)
	r.sleep = sleepCtx
	err := r.Run(ctx, []input.Domain{dom("a.com")}, func(Result) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
