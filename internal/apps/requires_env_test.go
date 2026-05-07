package apps

import (
	"strings"
	"testing"
)

func TestFindServerInjectedEnvCollisions(t *testing.T) {
	tests := []struct {
		name     string
		local    map[string]string
		requires map[string]ServiceRequirement
		want     []ServerInjectedEnvCollision
	}{
		{
			name:     "no requires, no collision",
			local:    map[string]string{"FOO": "bar"},
			requires: nil,
			want:     nil,
		},
		{
			name:  "no local env, no collision",
			local: nil,
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: nil,
		},
		{
			name: "single collision flagged",
			local: map[string]string{
				"DATABASE_URL": "postgresql://hand-authored",
				"LOG_LEVEL":    "info",
			},
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: []ServerInjectedEnvCollision{
				{EnvVar: "DATABASE_URL", Alias: "db", Field: "url"},
			},
		},
		{
			name: "user-only env vars are fine",
			local: map[string]string{
				"LOG_LEVEL":    "debug",
				"FEATURE_FLAG": "1",
			},
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: nil,
		},
		{
			name: "multiple collisions sort by env var then alias",
			local: map[string]string{
				"REDIS_URL":    "redis://hand-authored",
				"DATABASE_URL": "postgresql://hand-authored",
			},
			requires: map[string]ServiceRequirement{
				"cache": {Env: map[string]string{"url": "REDIS_URL"}},
				"db":    {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: []ServerInjectedEnvCollision{
				{EnvVar: "DATABASE_URL", Alias: "db", Field: "url"},
				{EnvVar: "REDIS_URL", Alias: "cache", Field: "url"},
			},
		},
		{
			name: "two aliases mapping to the same env var collide both",
			// Edge case: two requires entries claiming the same env name
			// is itself a yaml authoring bug, but we should still flag
			// both so the user knows the file has dual claimants.
			local: map[string]string{
				"DATABASE_URL": "postgresql://hand-authored",
			},
			requires: map[string]ServiceRequirement{
				"primary":   {Env: map[string]string{"url": "DATABASE_URL"}},
				"secondary": {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: []ServerInjectedEnvCollision{
				{EnvVar: "DATABASE_URL", Alias: "primary", Field: "url"},
				{EnvVar: "DATABASE_URL", Alias: "secondary", Field: "url"},
			},
		},
		{
			name: "empty env name on requires entry is ignored",
			// A requires entry with no env mapping isn't claiming any
			// var; never flag it as a collision even if the local env
			// happens to have something that matches an empty string
			// (which it shouldn't, but defence in depth).
			local: map[string]string{"DATABASE_URL": "x"},
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": ""}},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindServerInjectedEnvCollisions(tt.local, tt.requires)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d:\n got %+v\nwant %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFormatServerInjectedEnvCollisions(t *testing.T) {
	t.Run("empty slice produces empty string", func(t *testing.T) {
		if got := FormatServerInjectedEnvCollisions(nil, ".env"); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("singular noun for one entry", func(t *testing.T) {
		got := FormatServerInjectedEnvCollisions([]ServerInjectedEnvCollision{
			{EnvVar: "DATABASE_URL", Alias: "db", Field: "url"},
		}, ".runos.mycluster3.nrdhg.env")
		if !strings.Contains(got, "1 local env key is") {
			t.Errorf("missing singular header, got: %q", got)
		}
		if !strings.Contains(got, "DATABASE_URL") {
			t.Errorf("missing env var name, got: %q", got)
		}
		if !strings.Contains(got, "requires.db.env.url") {
			t.Errorf("missing requires path, got: %q", got)
		}
		if !strings.Contains(got, ".runos.mycluster3.nrdhg.env") {
			t.Errorf("missing env file path, got: %q", got)
		}
		// Post-conductor-fix message: state that conductor drops
		// these and that the local line is harmless dead config.
		// LLMs reading this should NOT panic-refuse the deploy/sync.
		if !strings.Contains(got, "Conductor drops these") {
			t.Errorf("must explain conductor's filter behaviour, got: %q", got)
		}
		if !strings.Contains(got, "harmless dead config") {
			t.Errorf("must classify the local line as harmless, got: %q", got)
		}
		// Three-step recipe and refusal-style language are gone:
		// no apps_sync prescription, no SHADOWS warning, no urgency.
		if strings.Contains(got, "SHADOWS") {
			t.Errorf("post-conductor-fix message should NOT claim runtime shadowing, got: %q", got)
		}
		if strings.Contains(got, "runos apps sync") {
			t.Errorf("post-conductor-fix message should NOT prescribe apps_sync, got: %q", got)
		}
	})

	t.Run("plural noun for multiple entries", func(t *testing.T) {
		got := FormatServerInjectedEnvCollisions([]ServerInjectedEnvCollision{
			{EnvVar: "DATABASE_URL", Alias: "db", Field: "url"},
			{EnvVar: "REDIS_URL", Alias: "cache", Field: "url"},
		}, "")
		if !strings.Contains(got, "2 local env keys are") {
			t.Errorf("missing plural header, got: %q", got)
		}
		// envFile == "" → the message should fall back to "your env file"
		if !strings.Contains(got, "your env file") {
			t.Errorf("missing fallback target phrase, got: %q", got)
		}
	})
}
