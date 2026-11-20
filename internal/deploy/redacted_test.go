package deploy

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPrepareResponse_RedactedClearsToken pins the fix for the
// upload-bearer leak in cmd/deploy.go's `--json` success envelope:
// PrepareResponse.Token (the single-use upload bearer) must never
// appear in a printed JSON response once it has done its one job
// (plan "CLI upload" section, story 346). Other fields must survive
// unchanged.
func TestPrepareResponse_RedactedClearsToken(t *testing.T) {
	resp := &PrepareResponse{
		JobID:       "job-1",
		AppID:       "app01",
		UploadURL:   "/upload/abc",
		Token:       "super-secret-upload-bearer",
		ExpiresAt:   "2026-01-01T00:00:00Z",
		CliUploadID: "cu-1",
	}
	redacted := resp.Redacted()

	if redacted.Token != "" {
		t.Errorf("Token = %q, want empty", redacted.Token)
	}
	if redacted.JobID != resp.JobID || redacted.AppID != resp.AppID || redacted.UploadURL != resp.UploadURL || redacted.ExpiresAt != resp.ExpiresAt {
		t.Errorf("Redacted() changed a non-secret field: %+v", redacted)
	}

	out, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "super-secret-upload-bearer") {
		t.Errorf("redacted JSON still contains the upload bearer: %s", out)
	}

	// The original is untouched: Redacted returns a copy.
	if resp.Token != "super-secret-upload-bearer" {
		t.Errorf("Redacted() mutated the original response's Token")
	}
}
