package deploy

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPrepareResponse_RedactedClearsSecrets pins the fix for the
// upload-bearer leak in cmd/deploy.go's `--json` success envelope
// (PrepareResponse.Token, the single-use upload bearer) and finding
// 1330 (PrepareResponse.UploadURL, which can itself carry a secret in
// a presigned query string or path). Neither has any legitimate use
// once UploadTarball has already consumed them, so both must be
// cleared before this response is printed. Other fields must survive
// unchanged.
func TestPrepareResponse_RedactedClearsSecrets(t *testing.T) {
	resp := &PrepareResponse{
		JobID:       "job-1",
		AppID:       "app01",
		UploadURL:   "https://objectstore.example.com/upload/abc?signature=super-secret-signature",
		Token:       "super-secret-upload-bearer",
		ExpiresAt:   "2026-01-01T00:00:00Z",
		CliUploadID: "cu-1",
	}
	redacted := resp.Redacted()

	if redacted.Token != "" {
		t.Errorf("Token = %q, want empty", redacted.Token)
	}
	if redacted.UploadURL != "" {
		t.Errorf("UploadURL = %q, want empty", redacted.UploadURL)
	}
	if redacted.JobID != resp.JobID || redacted.AppID != resp.AppID || redacted.ExpiresAt != resp.ExpiresAt || redacted.CliUploadID != resp.CliUploadID {
		t.Errorf("Redacted() changed a non-secret field: %+v", redacted)
	}

	out, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"super-secret-upload-bearer", "super-secret-signature", "objectstore.example.com"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("redacted JSON still contains %q: %s", secret, out)
		}
	}

	// The original is untouched: Redacted returns a copy.
	if resp.Token != "super-secret-upload-bearer" {
		t.Errorf("Redacted() mutated the original response's Token")
	}
	if resp.UploadURL == "" {
		t.Errorf("Redacted() mutated the original response's UploadURL")
	}
}

// TestRedactUploadURL pins the transport-error sanitizer (finding
// 1330): the exact upload URL a failed request used must never
// survive into the returned error message, even when the underlying
// *url.Error or net error formatted it in verbatim.
func TestRedactUploadURL(t *testing.T) {
	url := "https://objectstore.example.com/upload/abc?signature=super-secret-signature"
	msg := `Post "` + url + `": dial tcp 203.0.113.5:443: connect: connection refused`

	got := redactUploadURL(msg, url)
	if strings.Contains(got, url) {
		t.Errorf("redactUploadURL left the URL in place: %s", got)
	}
	if strings.Contains(got, "super-secret-signature") {
		t.Errorf("redactUploadURL left the signature in place: %s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("redactUploadURL dropped the useful cause: %s", got)
	}
}
