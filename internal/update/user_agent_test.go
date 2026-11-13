package update

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/api/apitest"
)

// The version check goes to RunOS and announces the CLI; the binary download goes to a release host
// and must not.
func TestOnlyTheRunOSCallAnnouncesTheCLIVersion(t *testing.T) {
	seen := apitest.Record(t)
	u := &Updater{baseURL: "https://api.example.test", httpClient: &http.Client{Transport: api.NewTransport()}, progress: io.Discard}

	_ = u.DownloadAndInstall("1.2.3")
	for _, s := range seen.Sent() {
		if s.CarriesCLIVersion() {
			t.Fatalf("the release download to %s carried the CLI user agent %q", s.Host, s.UserAgent)
		}
	}
	if len(seen.Sent()) == 0 || !strings.Contains(seen.Sent()[0].Host, "github.com") {
		t.Fatalf("setup: the download did not go out as expected: %v", seen.Sent())
	}

	req, _ := http.NewRequest(http.MethodGet, "https://api.example.test/x", nil)
	_, _ = u.httpClient.Do(req)
	last := seen.Sent()[len(seen.Sent())-1]
	if !last.CarriesCLIVersion() {
		t.Fatalf("the call to RunOS carried %q, want the CLI user agent", last.UserAgent)
	}
}
