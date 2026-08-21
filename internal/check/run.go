package check

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/masonbesmer/tld-finder/internal/input"
)

type Runner struct {
	Client  *Client
	Rate    time.Duration // minimum interval per RDAP host
	Retries int
	Log     *slog.Logger

	sleep func(context.Context, time.Duration) error // test seam; nil = real sleep
}

// Run checks each domain serially, in input order, emitting one Result per
// domain. It returns the context error if interrupted; a failing domain never
// aborts the run.
func (r *Runner) Run(ctx context.Context, domains []input.Domain, emit func(Result)) error {
	if r.sleep == nil {
		r.sleep = sleepCtx
	}
	p := &pacer{base: r.Rate, sleep: r.sleep}
	r.Client.Pace = func(host string) { p.wait(ctx, host) }
	for _, d := range domains {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		emit(r.checkOne(ctx, d, p))
	}
	return nil
}

func (r *Runner) checkOne(ctx context.Context, d input.Domain, p *pacer) Result {
	var lastErr error
	for attempt := 0; attempt <= r.Retries; attempt++ {
		if attempt > 0 {
			if err := r.sleep(ctx, retryDelay(attempt-1, lastErr)); err != nil {
				break
			}
		}
		// WithoutCancel: on SIGINT the in-flight request is finished (bounded
		// by --timeout); only sleeps and the outer loop abort immediately.
		res, err := r.Client.Check(context.WithoutCancel(ctx), d.FQDN, d.TLD)
		if err == nil {
			res.Input, res.Domain, res.TLD = d.Input, d.FQDN, d.TLD
			return res
		}
		if errors.Is(err, ErrNoRDAP) {
			return Result{
				Input: d.Input, Domain: d.FQDN, TLD: d.TLD,
				Status: StatusUnknown, CheckedAt: time.Now().UTC(),
				Note: "no RDAP endpoint for ." + d.TLD,
			}
		}
		var rl *RateLimitError
		if errors.As(err, &rl) {
			p.slow(rl.Host)
		}
		lastErr = err
		r.Log.Warn("check failed", "domain", d.FQDN, "attempt", attempt+1, "err", err)
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return Result{
		Input: d.Input, Domain: d.FQDN, TLD: d.TLD,
		Status: StatusError, CheckedAt: time.Now().UTC(), Err: lastErr.Error(),
	}
}

// retryDelay is 500ms * 2^n with full jitter, capped at 30s; a rate limit's
// Retry-After wins when larger.
func retryDelay(n int, lastErr error) time.Duration {
	d := min(500*time.Millisecond<<n, 30*time.Second)
	d = time.Duration(rand.Int64N(int64(d) + 1))
	var rl *RateLimitError
	if errors.As(lastErr, &rl) && rl.RetryAfter > d {
		d = rl.RetryAfter
	}
	return d
}

// pacer enforces a minimum interval per RDAP host. A 429 doubles the interval
// for that host for the rest of the run.
type pacer struct {
	base     time.Duration
	interval map[string]time.Duration
	last     map[string]time.Time
	sleep    func(context.Context, time.Duration) error
}

func (p *pacer) wait(ctx context.Context, host string) {
	if p.interval == nil {
		p.interval = map[string]time.Duration{}
		p.last = map[string]time.Time{}
	}
	iv, ok := p.interval[host]
	if !ok {
		iv = p.base
		p.interval[host] = iv
	}
	if last, ok := p.last[host]; ok {
		_ = p.sleep(ctx, iv-time.Since(last))
	}
	p.last[host] = time.Now()
}

func (p *pacer) slow(host string) {
	if p.interval == nil {
		p.interval = map[string]time.Duration{}
	}
	iv, ok := p.interval[host]
	if !ok {
		iv = p.base
	}
	p.interval[host] = iv * 2
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
