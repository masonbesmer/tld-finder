package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

const maxBody = 1 << 20 // some registries return huge entity graphs; none of it is needed

var reservedRe = regexp.MustCompile(`(?i)reserv|blocked|invalid`)

type Client struct {
	HTTP      *http.Client
	Bootstrap Bootstrap
	UserAgent string
	Timeout   time.Duration
	Pace      func(host string) // called before each request; may be nil
}

// NewHTTPClient returns a client that follows at most 5 redirects.
func NewHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			return nil
		},
	}
}

// Check queries each RDAP base URL for the TLD in order. A returned error is
// retryable (5xx, transport, rate limit); definitive outcomes come back as a
// Result. ErrNoRDAP means the TLD is not in the bootstrap map.
func (c *Client) Check(ctx context.Context, fqdn, tld string) (Result, error) {
	bases := c.Bootstrap[tld]
	if len(bases) == 0 {
		return Result{}, ErrNoRDAP
	}
	var lastErr error
	for _, base := range bases {
		res, err := c.query(ctx, base, fqdn)
		if err == nil {
			return res, nil
		}
		var rl *RateLimitError
		if errors.As(err, &rl) {
			return Result{}, err
		}
		lastErr = err
	}
	return Result{}, lastErr
}

type rdapDomain struct {
	ObjectClassName string   `json:"objectClassName"`
	LDHName         string   `json:"ldhName"`
	Status          []string `json:"status"`
	Events          []struct {
		Action string    `json:"eventAction"`
		Date   time.Time `json:"eventDate"`
	} `json:"events"`
	Title string `json:"title"`
}

func (c *Client) query(ctx context.Context, base, fqdn string) (Result, error) {
	host := base
	if u, err := url.Parse(base); err == nil {
		host = u.Host
	}
	if c.Pace != nil {
		c.Pace(host)
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/domain/"+fqdn, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/rdap+json")
	req.Header.Set("User-Agent", c.UserAgent)
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Result{}, err
	}
	res := Result{Authority: base, CheckedAt: time.Now().UTC(), Latency: time.Since(start)}
	switch {
	case resp.StatusCode == http.StatusOK:
		res.Status = StatusRegistered
		var doc rdapDomain
		if err := json.Unmarshal(body, &doc); err != nil {
			res.Note = "unparseable RDAP body"
			return res, nil
		}
		for _, ev := range doc.Events {
			if ev.Action == "expiration" {
				t := ev.Date
				res.Expiry = &t
			}
		}
		for _, s := range doc.Status {
			if s == "reserved" || s == "pending delete" {
				if res.Note != "" {
					res.Note += "; "
				}
				res.Note += s
			}
		}
		return res, nil
	case resp.StatusCode == http.StatusNotFound:
		res.Status = StatusAvailable // RFC 7480 §5.3: the object does not exist
		return res, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return Result{}, &RateLimitError{Host: host, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode == http.StatusBadRequest:
		var doc rdapDomain
		_ = json.Unmarshal(body, &doc)
		if reservedRe.MatchString(doc.Title) {
			res.Status = StatusReserved
			res.Note = doc.Title
			return res, nil
		}
		res.Status = StatusError
		res.Err = fmt.Sprintf("%s returned 400: %s", base, doc.Title)
		return res, nil
	default:
		return Result{}, fmt.Errorf("%s/domain/%s: unexpected status %d", base, fqdn, resp.StatusCode)
	}
}

// parseRetryAfter accepts both delta-seconds and HTTP-date forms.
func parseRetryAfter(v string) time.Duration {
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}
