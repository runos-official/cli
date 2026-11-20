package deploy

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNeedsBroadPreparation pins the route-selection decision finding
// 1352 asks for: a requires entry with no existing id needs the
// broad, cluster-scoped route (the app-scoped route cannot create a
// service inline, for any credential), while an existing app whose
// every requires entry already carries an id fits the app-scoped
// route.
func TestNeedsBroadPreparation(t *testing.T) {
	cases := []struct {
		name string
		cfg  *DeployConfig
		want bool
	}{
		{
			name: "no requires at all",
			cfg:  &DeployConfig{},
			want: false,
		},
		{
			name: "every requires entry already has an id",
			cfg: &DeployConfig{Requires: map[string]ServiceRequirement{
				"db":    {ID: "svc-1", Type: "postgres"},
				"cache": {ID: "svc-2", Type: "redis"},
			}},
			want: false,
		},
		{
			name: "one requires entry has no id (inline creation)",
			cfg: &DeployConfig{Requires: map[string]ServiceRequirement{
				"db":    {ID: "svc-1", Type: "postgres"},
				"cache": {Type: "redis"}, // no ID: class-shorthand creation
			}},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.NeedsBroadPreparation(); got != tc.want {
				t.Errorf("NeedsBroadPreparation() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidatePreparedTarget covers the response-side half of the
// immutable-target check (findings 1328/1354): a mismatched app id or
// an already-expired response must refuse before any local state is
// written or the archive is touched.
func TestValidatePreparedTarget(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	cases := []struct {
		name      string
		wantAppID string
		resp      *PrepareResponse
		wantErr   bool
	}{
		{
			name:      "matching appId and future expiry",
			wantAppID: "app01",
			resp:      &PrepareResponse{AppID: "app01", ExpiresAt: future},
			wantErr:   false,
		},
		{
			name:      "mismatched appId",
			wantAppID: "app01",
			resp:      &PrepareResponse{AppID: "app02", ExpiresAt: future},
			wantErr:   true,
		},
		{
			name:      "empty response appId (new-app case, no local target yet)",
			wantAppID: "",
			resp:      &PrepareResponse{AppID: "new-app", ExpiresAt: future},
			wantErr:   false,
		},
		{
			name:      "already expired",
			wantAppID: "app01",
			resp:      &PrepareResponse{AppID: "app01", ExpiresAt: past},
			wantErr:   true,
		},
		{
			name:      "no expiry field at all: skip that check",
			wantAppID: "app01",
			resp:      &PrepareResponse{AppID: "app01"},
			wantErr:   false,
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

// TestExplainAppScopedPrepareRefusal covers the three refusal codes
// handbook 992 documents for the app-scoped route, plus the pass-
// through cases: an error with no recognized code, and a non-APIError
// error. The original error must remain reachable via errors.Is/As
// (callers like emitJSONError still need the typed *APIError), and
// its own message must still be present in the explained text.
func TestExplainAppScopedPrepareRefusal(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantHintText string
	}{
		{
			name:         "authz.cannot_provision",
			err:          &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.cannot_provision"}`)},
			wantHintText: "existing linked services",
		},
		{
			name:         "authz.not_a_dependency",
			err:          &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.not_a_dependency"}`)},
			wantHintText: "existing linked services",
		},
		{
			name:         "authz.cannot_reconfigure",
			err:          &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.cannot_reconfigure"}`)},
			wantHintText: "resize or reconfigure",
		},
		{
			name:         "authz.conflicting_target",
			err:          &APIError{StatusCode: 403, Body: []byte(`{"code":"authz.conflicting_target"}`)},
			wantHintText: "different account, cluster or app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainAppScopedPrepareRefusal(tc.err)
			if !strings.Contains(got.Error(), tc.wantHintText) {
				t.Errorf("explained error = %q, want it to contain %q", got.Error(), tc.wantHintText)
			}
			var apiErr *APIError
			if !errors.As(got, &apiErr) {
				t.Errorf("explained error lost the underlying *APIError: %v", got)
			}
		})
	}

	t.Run("unrecognized code passes through unchanged", func(t *testing.T) {
		original := &APIError{StatusCode: 400, Body: []byte(`{"code":"deploy.something_else"}`)}
		got := ExplainAppScopedPrepareRefusal(original)
		if got != original {
			t.Errorf("expected the exact original error back, got: %v", got)
		}
	})

	t.Run("non-APIError passes through unchanged", func(t *testing.T) {
		original := errors.New("boom")
		got := ExplainAppScopedPrepareRefusal(original)
		if got != original {
			t.Errorf("expected the exact original error back, got: %v", got)
		}
	})
}
