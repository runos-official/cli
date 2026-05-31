package deploy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// I27-Y wire-shape probe: capture the exact bytes the CLI sends to
// /prepare-cli-deployment for a monorepo-shaped DeployConfig with
// sourceDir=../../.. and dockerfile=apps/api/Dockerfile. Pin that
// `dockerfile` lands on the wire as the full path (not basename).
func TestPrepareDeployment_WireShape_DockerfilePath(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobId":"j","osid":"o","appId":"a","uploadUrl":"http://x","token":"t","expiresAt":"x"}`))
	}))
	t.Cleanup(srv.Close)

	svc := &Service{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		token:      "t",
		aid:        "aid",
		cid:        "cid",
	}

	cfg := &DeployConfig{
		App:        "iter27-api",
		ID:         "ultbd",
		SourceDir:  "../../..",
		Dockerfile: "apps/api/Dockerfile",
	}
	if _, err := svc.PrepareDeployment(cfg); err != nil {
		t.Fatalf("PrepareDeployment: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(captured, &got); err != nil {
		t.Fatalf("unmarshal wire body: %v\nbody: %s", err, captured)
	}
	df, ok := got["dockerfile"].(string)
	if !ok {
		t.Fatalf("dockerfile field missing or wrong type on wire; body: %s", captured)
	}
	if df != "apps/api/Dockerfile" {
		t.Errorf("dockerfile on wire = %q, want %q (FULL path, not basename)", df, "apps/api/Dockerfile")
	}
	// sourceDir is json:"-" by design (CLI-only). Confirm it's stripped.
	if _, ok := got["sourceDir"]; ok {
		t.Errorf("sourceDir should NOT be on the wire (json:\"-\"); got: %v", got["sourceDir"])
	}
}

// DeploymentStrategy reaches the wire body verbatim when set in the yaml,
// and is omitted entirely when empty (omitempty on the json tag). Pre-
// fix this field was absent from DeployConfig and was silently dropped
// from the wire body, so a user setting `deploymentStrategy: recreate`
// in runos.yaml got a deploy with the conductor's `rolling` default.
func TestPrepareDeployment_WireShape_DeploymentStrategy(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want any // nil means key absent on the wire
	}{
		{"set to recreate", "recreate", "recreate"},
		{"set to zero-downtime", "zero-downtime", "zero-downtime"},
		{"omitted (default)", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				captured = b
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jobId":"j","osid":"o","appId":"a","uploadUrl":"http://x","token":"t","expiresAt":"x"}`))
			}))
			t.Cleanup(srv.Close)
			svc := &Service{
				baseURL:    srv.URL,
				httpClient: &http.Client{Timeout: 5 * time.Second},
				token:      "t",
				aid:        "aid",
				cid:        "cid",
			}
			cfg := &DeployConfig{App: "demo", ID: "abcde", DeploymentStrategy: tc.set}
			if _, err := svc.PrepareDeployment(cfg); err != nil {
				t.Fatalf("PrepareDeployment: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(captured, &got); err != nil {
				t.Fatalf("unmarshal wire body: %v\nbody: %s", err, captured)
			}
			if tc.want == nil {
				if _, ok := got["deploymentStrategy"]; ok {
					t.Errorf("deploymentStrategy should be omitted when empty; got: %v", got["deploymentStrategy"])
				}
				return
			}
			if got["deploymentStrategy"] != tc.want {
				t.Errorf("deploymentStrategy on wire = %v, want %v", got["deploymentStrategy"], tc.want)
			}
		})
	}
}
