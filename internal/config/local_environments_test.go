package config

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateEnvironmentCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environments.json")
	if err := os.WriteFile(path, []byte(`{"default":"dev","environments":{"dev":{"domains":{"api":"https://api.dev.example","console":"https://console.dev.example"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNOS_ENVIRONMENTS_FILE", path)
	rc, err := FetchRemoteConfig()
	if err != nil {
		t.Fatal(err)
	}
	if rc.Environments["dev"].Domains.APIURL() != "https://api.dev.example" {
		t.Fatal("private dev environment missing")
	}
}

// blockingTransport counts the HTTP requests it sees and, when blocking,
// never answers until the request's own context is done, so a test can tell
// whether a client reached the network at all and whether it honoured its
// own deadline, without either side touching a real host.
type blockingTransport struct {
	calls int
}

func (b *blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b.calls++
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// TestFetchRemoteConfig_PrivateCatalogSkipsNetworkCDNHonoursTimeout proves
// the merged FetchRemoteConfig together: the RUNOS_ENVIRONMENTS_FILE
// short-circuit never reaches the network, and once that short-circuit does
// not apply, the CDN path (FetchRemoteConfigWithin) is bounded by the
// caller's own timeout rather than some fixed internal one.
func TestFetchRemoteConfig_PrivateCatalogSkipsNetworkCDNHonoursTimeout(t *testing.T) {
	transport := &blockingTransport{}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	path := filepath.Join(t.TempDir(), "environments.json")
	if err := os.WriteFile(path, []byte(`{"default":"dev","environments":{"dev":{"domains":{"api":"https://api.dev.example"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNOS_ENVIRONMENTS_FILE", path)

	rc, err := FetchRemoteConfig()
	if err != nil {
		t.Fatal(err)
	}
	if rc.Environments["dev"].Domains.APIURL() != "https://api.dev.example" {
		t.Fatal("private dev environment missing")
	}
	if transport.calls != 0 {
		t.Fatalf("FetchRemoteConfig made %d HTTP request(s) with RUNOS_ENVIRONMENTS_FILE set, want 0", transport.calls)
	}

	t.Setenv("RUNOS_ENVIRONMENTS_FILE", "")
	start := time.Now()
	if _, err := FetchRemoteConfigWithin(20 * time.Millisecond); err == nil {
		t.Fatal("expected FetchRemoteConfigWithin to time out against a hanging transport, got nil error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("FetchRemoteConfigWithin(20ms) took %s, want it bounded by the given timeout", elapsed)
	}
	if transport.calls != 1 {
		t.Fatalf("expected exactly one HTTP request on the CDN path, got %d", transport.calls)
	}
}
