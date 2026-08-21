package check

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const BootstrapURL = "https://data.iana.org/rdap/dns.json"

// Bootstrap maps a lowercase TLD to its RDAP base URLs (https only, no trailing slash).
type Bootstrap map[string][]string

type BootstrapLoader struct {
	URL      string
	HTTP     *http.Client
	CacheDir string
	TTL      time.Duration
	Log      *slog.Logger
}

// Load returns the bootstrap map, preferring a fresh cache, then the network,
// then a stale cache with a warning. No cache and no network is an error.
func (l *BootstrapLoader) Load(ctx context.Context) (Bootstrap, error) {
	cache := filepath.Join(l.CacheDir, "rdap-bootstrap.json")
	if data, err := os.ReadFile(cache); err == nil {
		if fi, err := os.Stat(cache); err == nil && time.Since(fi.ModTime()) < l.TTL {
			if b, err := parseBootstrap(data); err == nil {
				return b, nil
			}
		}
	}
	b, err := l.fetch(ctx, cache)
	if err == nil {
		return b, nil
	}
	if data, rerr := os.ReadFile(cache); rerr == nil {
		if b, perr := parseBootstrap(data); perr == nil {
			l.Log.Warn("bootstrap fetch failed; using stale cache", "err", err)
			return b, nil
		}
	}
	return nil, fmt.Errorf("no usable RDAP bootstrap: %w", err)
}

func (l *BootstrapLoader) fetch(ctx context.Context, cache string) (Bootstrap, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	if etag, err := os.ReadFile(cache + ".etag"); err == nil {
		req.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		data, err := os.ReadFile(cache)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		_ = os.Chtimes(cache, now, now)
		return parseBootstrap(data)
	case http.StatusOK:
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		b, err := parseBootstrap(data)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(l.CacheDir, 0o755); err == nil {
			_ = os.WriteFile(cache, data, 0o644)
			if etag := resp.Header.Get("ETag"); etag != "" {
				_ = os.WriteFile(cache+".etag", []byte(etag), 0o644)
			}
		}
		return b, nil
	default:
		return nil, fmt.Errorf("bootstrap fetch: unexpected status %d", resp.StatusCode)
	}
}

// Override replaces a TLD's base URLs from a "tld=https://base" spec, for
// registries that serve RDAP without registering it with IANA (e.g. .io).
func (b Bootstrap) Override(spec string) error {
	tld, base, ok := strings.Cut(spec, "=")
	tld = strings.ToLower(strings.TrimPrefix(tld, "."))
	if !ok || tld == "" || !strings.HasPrefix(base, "https://") {
		return fmt.Errorf("invalid --rdap-base %q (want tld=https://base-url)", spec)
	}
	b[tld] = []string{strings.TrimRight(base, "/")}
	return nil
}

func parseBootstrap(data []byte) (Bootstrap, error) {
	var doc struct {
		Services [][2][]string `json:"services"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse bootstrap: %w", err)
	}
	b := Bootstrap{}
	for _, svc := range doc.Services {
		var urls []string
		for _, u := range svc[1] {
			if strings.HasPrefix(u, "https://") {
				urls = append(urls, strings.TrimRight(u, "/"))
			}
		}
		if len(urls) == 0 {
			continue
		}
		for _, tld := range svc[0] {
			b[strings.ToLower(tld)] = urls
		}
	}
	return b, nil
}
