package harbor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// FCR 787: conductor warns in the prepare response when the upload host has no public DNS
// record (the RunOS VPN still resolves it). The CLI must read the warnings so it can print
// them before the upload.
func TestPrepareBuildImageReadsWarnings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobId":"j1","uploadUrl":"/u","token":"t","expiresAt":"e","uploadId":"u1","images":["i"],"warnings":["no public DNS record"]}`))
	}))
	defer srv.Close()

	prep, err := NewService(srv.URL, "tok", "a1", "c1").PrepareBuildImage(BuildImageRequest{Repo: "web", Tags: []string{"v1"}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(prep.Warnings) != 1 || prep.Warnings[0] != "no public DNS record" {
		t.Fatalf("warnings = %v, want the one conductor sent", prep.Warnings)
	}
}
