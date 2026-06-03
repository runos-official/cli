package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/jobs"
)

// foreman objective 43 / story 74: pin enforceBuildVCSDeployType's
// VCS-only refusal so the verb fails close to argv (not after a
// conductor round-trip) when targeting a CLI-deploy app.
func TestEnforceBuildVCSDeployType(t *testing.T) {
	cases := []struct {
		name       string
		deployType string
		wantErr    bool
		wantSub    string
	}{
		{name: "vcs accepted", deployType: "vcs", wantErr: false},
		{name: "cli refused", deployType: "cli", wantErr: true, wantSub: `deployType="cli"`},
		{name: "empty refused with pull hint", deployType: "", wantErr: true, wantSub: "runos apps pull"},
		{name: "unknown refused", deployType: "garbage", wantErr: true, wantSub: `deployType="garbage"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := enforceBuildVCSDeployType(c.deployType)
			if c.wantErr && err == nil {
				t.Fatalf("expected error for deployType=%q", c.deployType)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error for deployType=%q: %v", c.deployType, err)
			}
			if c.wantSub != "" && !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q missing substring %q", err.Error(), c.wantSub)
			}
		})
	}
}

// foreman objective 43 / story 74: buildAppsBuildSummary renders empty
// configPath as "<server default>" so users can see the AppDocument
// fallback is in play (mirrors buildVCSDeploySummary's contract).
func TestBuildAppsBuildSummary(t *testing.T) {
	cases := []struct {
		name        string
		configPath  string
		wantConfig  string
	}{
		{name: "explicit configPath rendered", configPath: "apps/billing/runos.yaml", wantConfig: "configPath: apps/billing/runos.yaml"},
		{name: "empty configPath renders server-default sentinel", configPath: "", wantConfig: "configPath: <server default>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildAppsBuildSummary("appid7", "mycluster", "myacct", "abc1234567890abc1234567890abc1234567890a", c.configPath)
			if !strings.Contains(got, c.wantConfig) {
				t.Errorf("summary missing %q:\n%s", c.wantConfig, got)
			}
			// Always includes the short SHA + app/cluster/account anchors.
			for _, anchor := range []string{"App:", "Cluster:", "Account:", "SHA:", "abc1234"} {
				if !strings.Contains(got, anchor) {
					t.Errorf("summary missing anchor %q:\n%s", anchor, got)
				}
			}
		})
	}
}

// foreman objective 43 / story 74: summarizeBuildResult emits the right
// human one-liner for each terminal-result shape. The cached path is
// distinct so CI logs read "Image already cached." instead of pretending
// work happened.
func TestSummarizeBuildResult(t *testing.T) {
	cases := []struct {
		name string
		in   *jobs.BuildResult
		want string
	}{
		{
			name: "nil result falls back to generic completion",
			in:   nil,
			want: "Build completed.",
		},
		{
			name: "cached path",
			in:   &jobs.BuildResult{SkippedBecauseCached: true, ImageTag: "appid7:abc", DurationMs: 12},
			want: "Image already cached.",
		},
		{
			name: "fresh build with image tag + duration",
			in:   &jobs.BuildResult{ImageTag: "appid7:abc1234", DurationMs: 75_500},
			want: "Built appid7:abc1234 in 1m16s.",
		},
		{
			name: "fresh build without image tag falls back to duration only",
			in:   &jobs.BuildResult{DurationMs: 4_000},
			want: "Build completed in 4s.",
		},
		{
			name: "fresh build with zero duration prints <1s",
			in:   &jobs.BuildResult{ImageTag: "appid7:abc"},
			want: "Built appid7:abc in <1s.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			summarizeBuildResult(&buf, c.in)
			if !strings.Contains(buf.String(), c.want) {
				t.Errorf("summary missing %q:\n%s", c.want, buf.String())
			}
		})
	}
}

// foreman objective 43 / story 74: the --json envelope marshals to the
// shape the brief's success criterion #6 names. SkippedBecauseCached is
// *bool so omitempty distinguishes "absent" (no --follow / no result)
// from explicit false (built fresh, not cached). The three optional
// fields are emitted only when the conductor's structured result is
// populated.
func TestAppsBuildJSONResponseShape(t *testing.T) {
	t.Run("fire-and-forget shape omits result fields", func(t *testing.T) {
		env := appsBuildJSONResponse{
			JobID: "j-1", AppID: "appid7", SHA: "abc1234567890abc1234567890abc1234567890a", ConfigPath: "apps/billing/runos.yaml",
		}
		got, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(got)
		for _, must := range []string{`"jobId":"j-1"`, `"appId":"appid7"`, `"sha":"abc1234567890abc1234567890abc1234567890a"`, `"configPath":"apps/billing/runos.yaml"`} {
			if !strings.Contains(s, must) {
				t.Errorf("envelope missing %q in %s", must, s)
			}
		}
		for _, mustNot := range []string{"imageTag", "skippedBecauseCached", "durationMs"} {
			if strings.Contains(s, mustNot) {
				t.Errorf("fire-and-forget envelope unexpectedly emits %q in %s", mustNot, s)
			}
		}
	})

	t.Run("fresh build emits all result fields with cached=false explicit", func(t *testing.T) {
		cached := false
		env := appsBuildJSONResponse{
			JobID: "j-1", AppID: "appid7", SHA: "abc1234567890abc1234567890abc1234567890a",
			ImageTag: "appid7:abc1234", SkippedBecauseCached: &cached, DurationMs: 75_500,
		}
		got, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(got)
		for _, must := range []string{`"imageTag":"appid7:abc1234"`, `"skippedBecauseCached":false`, `"durationMs":75500`} {
			if !strings.Contains(s, must) {
				t.Errorf("fresh-build envelope missing %q in %s", must, s)
			}
		}
	})

	t.Run("cached path emits skippedBecauseCached=true", func(t *testing.T) {
		cached := true
		env := appsBuildJSONResponse{
			JobID: "j-1", AppID: "appid7", SHA: "abc1234567890abc1234567890abc1234567890a",
			ImageTag: "appid7:abc1234", SkippedBecauseCached: &cached, DurationMs: 12,
		}
		got, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(got)
		if !strings.Contains(s, `"skippedBecauseCached":true`) {
			t.Errorf("cached envelope missing skippedBecauseCached=true in %s", s)
		}
	})
}
