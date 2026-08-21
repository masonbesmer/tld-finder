package check

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

var discard = slog.New(slog.DiscardHandler)

const bootstrapDoc = `{"services":[
  [["com","net"],["https://rdap.verisign.example/v1/","http://insecure.example/"]],
  [["dev"],["https://rdap.example.dev"]],
  [["httponly"],["http://only-http.example/"]]
]}`

func bootstrapServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(bootstrapDoc))
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func loader(url, dir string, ttl time.Duration) *BootstrapLoader {
	return &BootstrapLoader{URL: url, HTTP: http.DefaultClient, CacheDir: dir, TTL: ttl, Log: discard}
}

func TestBootstrapFlatten(t *testing.T) {
	srv, _ := bootstrapServer(t)
	b, err := loader(srv.URL, t.TempDir(), time.Hour).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Bootstrap{
		"com": {"https://rdap.verisign.example/v1"},
		"net": {"https://rdap.verisign.example/v1"},
		"dev": {"https://rdap.example.dev"},
	}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("got %v, want %v", b, want)
	}
}

func TestBootstrapCacheHitAndETag(t *testing.T) {
	srv, requests := bootstrapServer(t)
	dir := t.TempDir()

	if _, err := loader(srv.URL, dir, time.Hour).Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Fresh cache: no request.
	if _, err := loader(srv.URL, dir, time.Hour).Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected 1 request after cache hit, got %d", got)
	}
	// Expired cache: revalidates, gets 304, still parses.
	b, err := loader(srv.URL, dir, 0).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected 2 requests after revalidation, got %d", got)
	}
	if len(b["com"]) == 0 {
		t.Fatal("expected com in bootstrap after 304")
	}
}

func TestBootstrapOverride(t *testing.T) {
	b := Bootstrap{"com": {"https://a.example"}}
	if err := b.Override(".IO=https://rdap.example.io/rdap/"); err != nil {
		t.Fatal(err)
	}
	if got := b["io"]; len(got) != 1 || got[0] != "https://rdap.example.io/rdap" {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"io", "io=http://insecure.example", "=https://x.example"} {
		if err := b.Override(bad); err == nil {
			t.Errorf("Override(%q) should fail", bad)
		}
	}
}

func TestBootstrapStaleFallback(t *testing.T) {
	srv, _ := bootstrapServer(t)
	dir := t.TempDir()
	if _, err := loader(srv.URL, dir, time.Hour).Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	b, err := loader(srv.URL, dir, 0).Load(context.Background())
	if err != nil {
		t.Fatalf("expected stale-cache fallback, got %v", err)
	}
	if len(b["com"]) == 0 {
		t.Fatal("stale cache missing com")
	}
}

func TestBootstrapNoCacheNoNetwork(t *testing.T) {
	srv, _ := bootstrapServer(t)
	srv.Close()
	if _, err := loader(srv.URL, t.TempDir(), time.Hour).Load(context.Background()); err == nil {
		t.Fatal("expected fatal error with no cache and no network")
	}
}
