package deploy

import (
	"testing"
	"time"
)

// TestValidatePreparedTarget covers the response-side half of the
// immutable-target check (findings 1328, 1354, 1391): a mismatched or
// MISSING app id, an already-expired or MISSING expiry, or a missing
// job/upload-url/token must all refuse before any local state is
// written or the archive is touched. Only the first (fully complete)
// row and the "new-app, osid only" row are legitimate-request rows
// that must NOT refuse; every other row pins one specific missing or
// wrong required field.
func TestValidatePreparedTarget(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	complete := func() PrepareResponse {
		return PrepareResponse{AppID: "app01", JobID: "job-1", UploadURL: "/u", Token: "t", ExpiresAt: future}
	}

	cases := []struct {
		name      string
		wantAppID string
		resp      *PrepareResponse
		wantErr   bool
	}{
		{
			name:      "complete response, matching appId and future expiry",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); return &r }(),
			wantErr:   false,
		},
		{
			name:      "new-app request, osid only (no appId yet)",
			wantAppID: "",
			resp: func() *PrepareResponse {
				r := complete()
				r.AppID = ""
				r.OSID = "app-new"
				return &r
			}(),
			wantErr: false,
		},
		{
			name:      "mismatched appId",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.AppID = "app02"; return &r }(),
			wantErr:   true,
		},
		{
			name:      "missing appId on an existing-app request",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.AppID = ""; return &r }(),
			wantErr:   true,
		},
		{
			name:      "missing appId AND osid on a new-app request",
			wantAppID: "",
			resp:      func() *PrepareResponse { r := complete(); r.AppID = ""; return &r }(),
			wantErr:   true,
		},
		{
			name:      "missing jobId",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.JobID = ""; return &r }(),
			wantErr:   true,
		},
		{
			name:      "missing uploadUrl",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.UploadURL = ""; return &r }(),
			wantErr:   true,
		},
		{
			name:      "missing token",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.Token = ""; return &r }(),
			wantErr:   true,
		},
		{
			name:      "already expired",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.ExpiresAt = past; return &r }(),
			wantErr:   true,
		},
		{
			name:      "missing expiresAt",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.ExpiresAt = ""; return &r }(),
			wantErr:   true,
		},
		{
			name:      "malformed expiresAt",
			wantAppID: "app01",
			resp:      func() *PrepareResponse { r := complete(); r.ExpiresAt = "not-a-timestamp"; return &r }(),
			wantErr:   true,
		},
		{
			name:      "nil response",
			wantAppID: "app01",
			resp:      nil,
			wantErr:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePreparedTarget(tc.wantAppID, tc.resp)
			if tc.wantErr && err == nil {
				t.Error("expected a refusal, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no refusal, got: %v", err)
			}
		})
	}
}
