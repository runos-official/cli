package deploy

import (
	"errors"
	"strings"
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

// TestExplainAppScopedPrepareRefusal covers every documented refusal
// shape of the app-scoped preparation route (handbook 992; findings
// 1352, 1408) plus the control case (an error ExplainAppScopedPrepareRefusal
// does not recognize passes through unchanged, never invented).
func TestExplainAppScopedPrepareRefusal(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantHint   string // substring the hint must add; "" means unchanged
		wantNoHint bool
	}{
		{
			name:     "400 with no code: missing build config or malformed yaml",
			err:      &APIError{StatusCode: 400, Body: []byte(`{"error":"bad request"}`)},
			wantHint: "configure-for-deployment",
		},
		{
			name:     "403 authz.cannot_provision: requires entry with no id",
			err:      &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.cannot_provision"}`)},
			wantHint: "can only redeploy an already-linked service",
		},
		{
			name:     "403 authz.not_a_dependency: id not linked to this app",
			err:      &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.not_a_dependency"}`)},
			wantHint: "can only redeploy an already-linked service",
		},
		{
			name:     "403 authz.cannot_reconfigure: resize/reconfigure attempt",
			err:      &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.cannot_reconfigure"}`)},
			wantHint: "cannot resize or reconfigure",
		},
		{
			name:     "403 authz.conflicting_target",
			err:      &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.conflicting_target"}`)},
			wantHint: "named a different account, cluster or app",
		},
		{
			name:       "403 with an unrecognized code passes through unchanged",
			err:        &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.something_else"}`)},
			wantNoHint: true,
		},
		{
			name:       "404 passes through unchanged",
			err:        &APIError{StatusCode: 404, Body: []byte(`{"error":"not found"}`)},
			wantNoHint: true,
		},
		{
			name:       "503 passes through unchanged",
			err:        &APIError{StatusCode: 503, Body: []byte(`{"error":"deploy.upload_token_unavailable"}`)},
			wantNoHint: true,
		},
		{
			name:       "non-APIError passes through unchanged",
			err:        errors.New("transport error"),
			wantNoHint: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainAppScopedPrepareRefusal(tc.err)
			if !errors.Is(got, tc.err) {
				t.Fatalf("explained error does not wrap the original: %v", got)
			}
			if tc.wantNoHint {
				if got.Error() != tc.err.Error() {
					t.Errorf("expected the error unchanged, got: %v", got)
				}
				return
			}
			if !strings.Contains(got.Error(), tc.wantHint) {
				t.Errorf("error %q does not contain expected hint %q", got.Error(), tc.wantHint)
			}
		})
	}
}
