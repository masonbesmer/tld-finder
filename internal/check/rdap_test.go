package check

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func serveFixture(t *testing.T, w http.ResponseWriter, name string, code int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rdap", name))
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/rdap+json")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}

func rdapServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rdap/domain/registered.com", func(w http.ResponseWriter, r *http.Request) {
		serveFixture(t, w, "registered.json", http.StatusOK)
	})
	mux.HandleFunc("/rdap/domain/reservedstatus.com", func(w http.ResponseWriter, r *http.Request) {
		serveFixture(t, w, "reserved-status.json", http.StatusOK)
	})
	mux.HandleFunc("/rdap/domain/free.com", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/rdap/domain/limited.com", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/rdap/domain/limited-date.com", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(90*time.Second).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/rdap/domain/blocked.com", func(w http.ResponseWriter, r *http.Request) {
		serveFixture(t, w, "blocked-400.json", http.StatusBadRequest)
	})
	mux.HandleFunc("/rdap/domain/bad.com", func(w http.ResponseWriter, r *http.Request) {
		serveFixture(t, w, "bad-400.json", http.StatusBadRequest)
	})
	mux.HandleFunc("/rdap/domain/boom.com", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/rdap/domain/big.com", func(w http.ResponseWriter, r *http.Request) {
		// Valid JSON larger than the 1 MiB cap: truncation must break the
		// parse, not hang or OOM.
		fmt.Fprintf(w, `{"objectClassName":"domain","remarks":[{"title":"%s"}]}`, strings.Repeat("x", 2<<20))
	})
	// /redir/{n}/domain/{d}: n redirects, then a 404.
	mux.HandleFunc("/redir/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/") // "", redir, n, domain, d
		n, _ := strconv.Atoi(parts[2])
		if n > 0 {
			http.Redirect(w, r, fmt.Sprintf("/redir/%d/domain/%s", n-1, parts[4]), http.StatusFound)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testClient(boot Bootstrap) *Client {
	return &Client{HTTP: NewHTTPClient(), Bootstrap: boot, UserAgent: "tldfinder/test", Timeout: 5 * time.Second}
}

func TestRDAPStatusMapping(t *testing.T) {
	srv := rdapServer(t)
	c := testClient(Bootstrap{"com": {srv.URL + "/rdap"}})
	ctx := context.Background()

	t.Run("200 registered with expiry", func(t *testing.T) {
		res, err := c.Check(ctx, "registered.com", "com")
		if err != nil || res.Status != StatusRegistered {
			t.Fatalf("got %+v, %v", res, err)
		}
		if res.Expiry == nil || !res.Expiry.Equal(time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC)) {
			t.Fatalf("expiry = %v", res.Expiry)
		}
		if res.Authority != srv.URL+"/rdap" {
			t.Fatalf("authority = %q", res.Authority)
		}
	})
	t.Run("200 with reserved status noted", func(t *testing.T) {
		res, err := c.Check(ctx, "reservedstatus.com", "com")
		if err != nil || res.Status != StatusRegistered || res.Note != "reserved" {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("404 available", func(t *testing.T) {
		res, err := c.Check(ctx, "free.com", "com")
		if err != nil || res.Status != StatusAvailable {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("429 delta-seconds", func(t *testing.T) {
		_, err := c.Check(ctx, "limited.com", "com")
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.RetryAfter != 120*time.Second {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("429 http-date", func(t *testing.T) {
		_, err := c.Check(ctx, "limited-date.com", "com")
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.RetryAfter < 80*time.Second || rl.RetryAfter > 95*time.Second {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("400 blocked title is reserved", func(t *testing.T) {
		res, err := c.Check(ctx, "blocked.com", "com")
		if err != nil || res.Status != StatusReserved {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("400 other title is error status", func(t *testing.T) {
		res, err := c.Check(ctx, "bad.com", "com")
		if err != nil || res.Status != StatusError || res.Err == "" {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("500 is retryable error", func(t *testing.T) {
		if _, err := c.Check(ctx, "boom.com", "com"); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("body capped at 1 MiB", func(t *testing.T) {
		res, err := c.Check(ctx, "big.com", "com")
		if err != nil || res.Status != StatusRegistered || res.Note != "unparseable RDAP body" {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
}

func TestRDAPRedirects(t *testing.T) {
	srv := rdapServer(t)
	ctx := context.Background()

	c := testClient(Bootstrap{"com": {srv.URL + "/redir/3"}})
	res, err := c.Check(ctx, "free.com", "com")
	if err != nil || res.Status != StatusAvailable {
		t.Fatalf("3 redirects should succeed: %+v, %v", res, err)
	}

	c = testClient(Bootstrap{"com": {srv.URL + "/redir/6"}})
	if _, err := c.Check(ctx, "free.com", "com"); err == nil {
		t.Fatal("6 redirects should exceed the 5-redirect cap")
	}
}

func TestRDAPFallbackToSecondBase(t *testing.T) {
	srv := rdapServer(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := testClient(Bootstrap{"com": {dead.URL + "/rdap", srv.URL + "/rdap"}})
	res, err := c.Check(context.Background(), "free.com", "com")
	if err != nil || res.Status != StatusAvailable || res.Authority != srv.URL+"/rdap" {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestRDAPNoEndpoint(t *testing.T) {
	c := testClient(Bootstrap{})
	if _, err := c.Check(context.Background(), "x.de", "de"); !errors.Is(err, ErrNoRDAP) {
		t.Fatalf("got %v, want ErrNoRDAP", err)
	}
}
