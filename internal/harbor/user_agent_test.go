package harbor

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api/apitest"
)

func TestContextUploadAnnouncesTheCLIOnlyToRunOS(t *testing.T) {
	seen := apitest.Record(t)
	svc := NewService("https://api.example.test", "tok", "a1", "c1")

	_ = svc.UploadContext("/upload/1", "tok", strings.NewReader("x"))
	_ = svc.UploadContext("https://upload.example.test/1", "tok", strings.NewReader("x"))

	got := seen.Sent()
	if len(got) != 2 || !got[0].CarriesCLIVersion() || got[1].CarriesCLIVersion() {
		t.Fatalf("sent %+v, want the relative upload to announce the CLI and the absolute one not to", got)
	}
}
