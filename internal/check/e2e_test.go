//go:build e2e

package check

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"
)

// TestE2E hits real RDAP. Run with: go test -tags e2e ./internal/check/
func TestE2E(t *testing.T) {
	ctx := context.Background()
	boot, err := (&BootstrapLoader{
		URL:      BootstrapURL,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		CacheDir: t.TempDir(),
		TTL:      24 * time.Hour,
		Log:      discard,
	}).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{HTTP: NewHTTPClient(), Bootstrap: boot, UserAgent: "tldfinder/e2e-test", Timeout: 10 * time.Second}

	res, err := c.Check(ctx, "example.com", "com")
	if err != nil || res.Status != StatusRegistered {
		t.Fatalf("example.com: %+v, %v", res, err)
	}

	random := fmt.Sprintf("tldfinder-e2e-%d%d", rand.Int32(), rand.Int32())[:20] + ".com"
	res, err = c.Check(ctx, random, "com")
	if err != nil || res.Status != StatusAvailable {
		t.Fatalf("%s: %+v, %v", random, res, err)
	}
}
