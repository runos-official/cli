package deploy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/api/apitest"
)

// A relative upload path goes to RunOS and announces the CLI. An absolute upload URL is supplied by
// the server and may be any host, so it does not.
func TestUploadAnnouncesTheCLIOnlyToRunOS(t *testing.T) {
	seen := apitest.Record(t)
	svc := &Service{baseURL: "https://api.example.test", httpClient: &http.Client{Transport: api.NewTransport()}}

	_ = svc.UploadTarball("/upload/1", "tok", strings.NewReader("x"))
	_ = svc.UploadTarball("https://upload.example.test/1", "tok", strings.NewReader("x"))

	got := seen.Sent()
	if len(got) != 2 || !got[0].CarriesCLIVersion() || got[1].CarriesCLIVersion() {
		t.Fatalf("sent %+v, want the relative upload to announce the CLI and the absolute one not to", got)
	}
}
