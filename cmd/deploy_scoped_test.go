package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// TestValidateScopedDeployTarget is the colocated unit test for the
// story 346 S1/S2 local gate: a mutation that dropped the ID check,
// the DeployType check, or the per-alias ID check would pass a case
// this table refuses.
func TestValidateScopedDeployTarget(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *deploy.DeployConfig
		wantErr string
	}{
		{
			name:    "unpinned app",
			cfg:     &deploy.DeployConfig{App: "demo"},
			wantErr: "requires a pinned app",
		},
		{
			name:    "pinned app, no deployType",
			cfg:     &deploy.DeployConfig{App: "demo", ID: "ab123"},
			wantErr: "requires an explicit deployType",
		},
		{
			name: "requires alias with no id",
			cfg: &deploy.DeployConfig{App: "demo", ID: "ab123", DeployType: "cli", Requires: map[string]deploy.ServiceRequirement{
				"db": {Type: "postgres"},
			}},
			wantErr: `"db" has none`,
		},
		{
			name: "pinned, typed, every alias linked: legitimate request passes",
			cfg: &deploy.DeployConfig{App: "demo", ID: "ab123", DeployType: "cli", Requires: map[string]deploy.ServiceRequirement{
				"db": {ID: "svc-1", Type: "postgres"},
			}},
			wantErr: "",
		},
		{
			name:    "pinned, typed, no requires at all: legitimate request passes",
			cfg:     &deploy.DeployConfig{App: "demo", ID: "ab123", DeployType: "vcs"},
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateScopedDeployTarget(tc.cfg, "runos.yaml")
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected no refusal, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a refusal containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
