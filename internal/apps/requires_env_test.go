package apps

import (
	"reflect"
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
			name: "discrete-field requires.env: every field key produces a distinct collision",
			// Objective 42 regression: the conductor's discrete-field
			// requires.env injection lets an app map host/port/database/
			// user/password/url to arbitrary env names (GrowthCo's
			// adopt-user + link path). FindServerInjectedEnvCollisions
			// iterates the env map field-key-agnostically; a future
			// refactor that tightened the iteration to legacy url-only
			// would silently lose discrete-field collision detection
			// and the user would re-acquire the I3-E false-positive
			// drift gate for the non-url keys.
			local: map[string]string{
				"POSTGRES_SERVER":   "hand-authored.example.com",
				"POSTGRES_PORT":     "5433",
				"POSTGRES_DB":       "old_db_name",
				"POSTGRES_USER":     "hand-authored",
				"POSTGRES_PASSWORD": "hand-authored",
				"DATABASE_URL":      "postgresql://hand-authored",
				"LOG_LEVEL":         "info",
			},
			requires: map[string]ServiceRequirement{
				"growthco-db": {Env: map[string]string{
					"host":     "POSTGRES_SERVER",
					"port":     "POSTGRES_PORT",
					"database": "POSTGRES_DB",
					"user":     "POSTGRES_USER",
					"password": "POSTGRES_PASSWORD",
					"url":      "DATABASE_URL",
				}},
			},
			want: []ServerInjectedEnvCollision{
				{EnvVar: "DATABASE_URL", Alias: "growthco-db", Field: "url"},
				{EnvVar: "POSTGRES_DB", Alias: "growthco-db", Field: "database"},
				{EnvVar: "POSTGRES_PASSWORD", Alias: "growthco-db", Field: "password"},
				{EnvVar: "POSTGRES_PORT", Alias: "growthco-db", Field: "port"},
				{EnvVar: "POSTGRES_SERVER", Alias: "growthco-db", Field: "host"},
				{EnvVar: "POSTGRES_USER", Alias: "growthco-db", Field: "user"},
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

// I3-E regression: server-side platform-injected names (DATABASE_URL,
// REDIS_*, ...) must drop out of the drift comparison so a freshly-
// provisioned app's empty local `.secret.env` doesn't trip the
// pre-deploy gate. Mirrors apps_pull's count-side filter.
func TestFilterPlatformInjectedEnv(t *testing.T) {
	tests := []struct {
		name      string
		serverEnv map[string]string
		requires  map[string]ServiceRequirement
		want      map[string]string
	}{
		{
			name:      "no requires, server env passes through",
			serverEnv: map[string]string{"FOO": "bar"},
			requires:  nil,
			want:      map[string]string{"FOO": "bar"},
		},
		{
			name:      "empty server env, untouched",
			serverEnv: map[string]string{},
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: map[string]string{},
		},
		{
			name: "platform-injected key removed",
			serverEnv: map[string]string{
				"DATABASE_URL": "postgresql://...",
				"USER_TOKEN":   "user-set",
			},
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": "DATABASE_URL"}},
			},
			want: map[string]string{"USER_TOKEN": "user-set"},
		},
		{
			name: "multiple platform names removed across aliases",
			serverEnv: map[string]string{
				"DATABASE_URL": "postgresql://...",
				"REDIS_URL":    "redis://...",
				"REDIS_HOST":   "valkey.svc",
				"USER_TOKEN":   "user-set",
				"LOG_LEVEL":    "info",
			},
			requires: map[string]ServiceRequirement{
				"db":    {Env: map[string]string{"url": "DATABASE_URL"}},
				"cache": {Env: map[string]string{"url": "REDIS_URL", "host": "REDIS_HOST"}},
			},
			want: map[string]string{
				"USER_TOKEN": "user-set",
				"LOG_LEVEL":  "info",
			},
		},
		{
			name: "requires with no env mapping leaves server env untouched",
			serverEnv: map[string]string{
				"DATABASE_URL": "postgresql://...",
				"USER_TOKEN":   "user-set",
			},
			requires: map[string]ServiceRequirement{
				"db": {Type: "postgresql"}, // no Env mapping
			},
			want: map[string]string{
				"DATABASE_URL": "postgresql://...",
				"USER_TOKEN":   "user-set",
			},
		},
		{
			name: "discrete-field requires.env: every injected name drops out",
			// Objective 42 regression: when the adopted app's
			// requires.env maps the full discrete-field set, all six
			// platform-injected names must drop out of the comparison.
			// Locks down today's field-key-agnostic iteration so a
			// future refactor can't silently tighten to url-only.
			serverEnv: map[string]string{
				"POSTGRES_SERVER":   "myosid-rw.mycluster.svc",
				"POSTGRES_PORT":     "5432",
				"POSTGRES_DB":       "growthco_sor",
				"POSTGRES_USER":     "growthco_sor",
				"POSTGRES_PASSWORD": "managed-secret",
				"DATABASE_URL":      "postgresql://managed",
				"USER_TOKEN":        "user-set",
			},
			requires: map[string]ServiceRequirement{
				"growthco-db": {Env: map[string]string{
					"host":     "POSTGRES_SERVER",
					"port":     "POSTGRES_PORT",
					"database": "POSTGRES_DB",
					"user":     "POSTGRES_USER",
					"password": "POSTGRES_PASSWORD",
					"url":      "DATABASE_URL",
				}},
			},
			want: map[string]string{
				"USER_TOKEN": "user-set",
			},
		},
		{
			name: "empty env-name string in requires is ignored",
			serverEnv: map[string]string{
				"DATABASE_URL": "postgresql://...",
				"":             "should-not-match",
			},
			requires: map[string]ServiceRequirement{
				"db": {Env: map[string]string{"url": ""}},
			},
			want: map[string]string{
				"DATABASE_URL": "postgresql://...",
				"":             "should-not-match",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterPlatformInjectedEnv(tt.serverEnv, tt.requires)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// FilterPlatformInjectedEnv must not mutate its input map.
func TestFilterPlatformInjectedEnv_DoesNotMutateInput(t *testing.T) {
	serverEnv := map[string]string{
		"DATABASE_URL": "postgresql://...",
		"USER_TOKEN":   "user-set",
	}
	requires := map[string]ServiceRequirement{
		"db": {Env: map[string]string{"url": "DATABASE_URL"}},
	}
	original := make(map[string]string, len(serverEnv))
	for k, v := range serverEnv {
		original[k] = v
	}
	_ = FilterPlatformInjectedEnv(serverEnv, requires)
	if !reflect.DeepEqual(serverEnv, original) {
		t.Errorf("input mutated: got %+v, want %+v", serverEnv, original)
	}
}

