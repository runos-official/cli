package jobs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetDeploymentProgress_RejectsMissingOrMismatchedJobID pins
// finding 1391: the published contract always echoes the exact
// requested job id back (the route is resolved entirely from the path
// segment), so a response that omits jobId is exactly as untrustworthy
// as one naming a different job -- both must refuse. The first cut of
// this check let a missing jobId pass silently.
func TestGetDeploymentProgress_RejectsMissingOrMismatchedJobID(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "matching jobId",
			body:    `{"version":"runos.deployment-progress.v1","jobId":"` + testJobID + `","appId":"app01","state":"running"}`,
			wantErr: false,
		},
		{
			name:    "missing jobId",
			body:    `{"version":"runos.deployment-progress.v1","appId":"app01","state":"running"}`,
			wantErr: true,
		},
		{
			name:    "mismatched jobId",
			body:    `{"version":"runos.deployment-progress.v1","jobId":"66666666-6666-4666-8666-666666666666","appId":"app01","state":"running"}`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			svc := &Service{baseURL: srv.URL, httpClient: srv.Client(), token: "t"}
			_, err := svc.GetDeploymentProgress(testJobID)
			if tc.wantErr && err == nil {
				t.Error("expected a refusal, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no refusal, got: %v", err)
			}
		})
	}
}
