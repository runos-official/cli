package apps

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// SanitizeName
// ---------------------------------------------------------------------------

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "my-app", "my-app"},
		{"spaces become dashes", "My App", "My-App"},
		{"slashes become dashes", "team/service", "team-service"},
		{"mixed awkward chars", "a b/c:d", "a-b-c-d"},
		{"underscores and dots kept", "my_app.v2", "my_app.v2"},
		{"alphanumeric unchanged", "abc123XYZ", "abc123XYZ"},
		{"all-invalid falls back to 'app'", "@@@", "---"},
		{"empty falls back to 'app'", "", "app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeName(tt.in)
			if got != tt.want {
				t.Fatalf("SanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Filename builders
// ---------------------------------------------------------------------------

func TestDefaultBaseName(t *testing.T) {
	tests := []struct {
		name string
		cid  string
		id   string
		want string
	}{
		{"plain", "k1", "ab12c", "runos.k1.ab12c"},
		{"lowercases cid + id", "PROD-01", "AB12C", "runos.prod-01.ab12c"},
		{"two apps in same cluster get distinct bases", "k1", "ab12c", "runos.k1.ab12c"},
		{"two apps in same cluster get distinct bases (2)", "k1", "xy99z", "runos.k1.xy99z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultBaseName(tt.cid, tt.id); got != tt.want {
				t.Errorf("DefaultBaseName(%q, %q) = %q, want %q", tt.cid, tt.id, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Requires (service dependencies)
// ---------------------------------------------------------------------------

// TestPulledApp_RequiresRoundTrip pins the on-disk yaml shape for the
// requires block. Pulled yamls must re-deploy byte-clean, so every
// field the user can author has to round-trip.
func TestPulledApp_RequiresRoundTrip(t *testing.T) {
	app := &PulledApp{
		App: "poll-app", ID: "appid3", CID: "mycluster3", AID: "myacct",
		Requires: map[string]ServiceRequirement{
			"poll-app-db": {
				ID:    "mjn1d",
				Type:  "postgresql",
				Class: "postgresql.c0.tiny", // creation-time spec, preserved if user keeps it
				Config: map[string]any{
					"databaseName":     "pollapp",
					"databaseUsername": "pollapp",
				},
				Env: map[string]string{"url": "DATABASE_URL"},
			},
		},
	}
	out, err := yaml.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		"requires:",
		"poll-app-db:",
		"id: mjn1d",
		"type: postgresql",
		"class: postgresql.c0.tiny",
		"databaseName: pollapp",
		"url: DATABASE_URL",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("marshaled yaml missing %q\n%s", want, out)
		}
	}
	var got PulledApp
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gotReq, ok := got.Requires["poll-app-db"]
	if !ok {
		t.Fatalf("requires.poll-app-db missing after round-trip; got %+v", got.Requires)
	}
	if gotReq.ID != "mjn1d" || gotReq.Type != "postgresql" || gotReq.Class != "postgresql.c0.tiny" {
		t.Errorf("type/id/class mismatch: %+v", gotReq)
	}
	if gotReq.Config["databaseName"] != "pollapp" {
		t.Errorf("config.databaseName lost: %+v", gotReq.Config)
	}
	if gotReq.Env["url"] != "DATABASE_URL" {
		t.Errorf("env mapping lost: %+v", gotReq.Env)
	}
}

func TestPulledApp_RequiresOmittedWhenEmpty(t *testing.T) {
	app := &PulledApp{App: "web", ID: "appid4", CID: "mycluster3", AID: "myacct"}
	out, err := yaml.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "requires:") {
		t.Errorf("empty requires must be omitted; got:\n%s", out)
	}
}

// TestMergeRequiresUserAuthored covers the post-/requires merge:
// server is authoritative for Type, ID, Config, and Env; Class is the
// only local-only field that always wins. Empty server Config/Env trigger
// the legacy fallback.
func TestMergeRequiresUserAuthored(t *testing.T) {
	t.Run("server config/env wins when populated, class merged from local", func(t *testing.T) {
		// Modern flow: server returns the full {type, id, config, env}.
		// Class comes from local only. Local config/env are ignored
		// because the server has truth.
		target := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {
					ID:     "mjn1d",
					Type:   "postgresql",
					Config: map[string]any{"databaseName": "server-side"},
					Env:    map[string]string{"url": "SERVER_DB_URL"},
				},
			},
		}
		local := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {
					ID:     "mjn1d",
					Type:   "postgresql",
					Class:  "postgresql.c0.tiny",
					Config: map[string]any{"databaseName": "local-stale"},
					Env:    map[string]string{"url": "LOCAL_STALE"},
				},
			},
		}
		MergeRequiresUserAuthored(target, local)
		got := target.Requires["poll-app-db"]
		if got.Class != "postgresql.c0.tiny" {
			t.Errorf("class should be merged from local; got %q", got.Class)
		}
		if got.Config["databaseName"] != "server-side" {
			t.Errorf("server config must win over local; got %+v", got.Config)
		}
		if got.Env["url"] != "SERVER_DB_URL" {
			t.Errorf("server env must win over local; got %+v", got.Env)
		}
	})

	t.Run("legacy: empty server config falls back to local", func(t *testing.T) {
		// Apps deployed before /requires landed return empty
		// Config/Env. Until the next deploy populates the server side,
		// the local yaml's values must survive a pull.
		target := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {ID: "mjn1d", Type: "postgresql"},
			},
		}
		local := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {
					ID:    "mjn1d",
					Type:  "postgresql",
					Class: "postgresql.c0.tiny",
					Config: map[string]any{
						"databaseName":     "pollapp",
						"databaseUsername": "pollapp",
					},
					Env: map[string]string{"url": "DATABASE_URL"},
				},
			},
		}
		MergeRequiresUserAuthored(target, local)
		got := target.Requires["poll-app-db"]
		if got.Class != "postgresql.c0.tiny" {
			t.Errorf("class should be merged from local; got %q", got.Class)
		}
		if got.Config["databaseName"] != "pollapp" {
			t.Errorf("legacy: empty server config should fall back to local; got %+v", got.Config)
		}
		if got.Env["url"] != "DATABASE_URL" {
			t.Errorf("legacy: empty server env should fall back to local; got %+v", got.Env)
		}
	})

	t.Run("server type/id wins over local on mismatch", func(t *testing.T) {
		target := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {ID: "mjn1d", Type: "postgresql"},
			},
		}
		local := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {
					ID:    "OLD-ID",
					Type:  "postgresql",
					Class: "postgresql.c0.tiny",
				},
			},
		}
		MergeRequiresUserAuthored(target, local)
		got := target.Requires["poll-app-db"]
		if got.ID != "mjn1d" {
			t.Errorf("server id must win over local; got %q", got.ID)
		}
		if got.Class != "postgresql.c0.tiny" {
			t.Errorf("class should still be merged even on id drift; got %q", got.Class)
		}
	})

	t.Run("server-only alias stays untouched", func(t *testing.T) {
		target := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-cache": {ID: "xY9zW", Type: "valkey"},
			},
		}
		local := &PulledApp{Requires: map[string]ServiceRequirement{}}
		MergeRequiresUserAuthored(target, local)
		got := target.Requires["poll-app-cache"]
		if got.ID != "xY9zW" || got.Type != "valkey" {
			t.Errorf("server-only alias must remain; got %+v", got)
		}
		if got.Class != "" || len(got.Config) != 0 || len(got.Env) != 0 {
			t.Errorf("server-only alias should have no user-authored fields; got %+v", got)
		}
	})

	t.Run("local-only alias is dropped", func(t *testing.T) {
		// The server no longer lists this dependency; pull must not
		// resurrect it. Only aliases conductor reports survive.
		target := &PulledApp{
			Requires: map[string]ServiceRequirement{},
		}
		local := &PulledApp{
			Requires: map[string]ServiceRequirement{
				"poll-app-db": {
					ID: "mjn1d", Type: "postgresql",
					Class: "postgresql.c0.tiny",
				},
			},
		}
		MergeRequiresUserAuthored(target, local)
		if _, ok := target.Requires["poll-app-db"]; ok {
			t.Errorf("alias the server doesn't report must not be merged in; got %+v", target.Requires)
		}
	})

	t.Run("nil-safe", func(t *testing.T) {
		MergeRequiresUserAuthored(nil, nil)
		MergeRequiresUserAuthored(&PulledApp{}, nil)
		MergeRequiresUserAuthored(nil, &PulledApp{})
		// No panic, no crash.
	})
}

// I3-B regression: the secretEnv / env path fields are CLI-side
// bookkeeping (where the user keeps the file on disk), not server-
// authoritative state. Pull must preserve user-explicit values so a
// user who sets `secretEnv: .secret.env` doesn't fight permanent
// drift against the canonical default that BuildServerStateForDiff
// stamps when env content exists.
func TestMergeUserEnvPaths(t *testing.T) {
	t.Run("explicit local paths win over canonical defaults", func(t *testing.T) {
		target := &PulledApp{
			SecretEnv: ".runos.k1.ab12c.env",
			Env:       "runos.k1.ab12c.config.env",
		}
		local := &PulledApp{
			SecretEnv: ".secret.env",
			Env:       "plain.env",
		}
		MergeUserEnvPaths(target, local)
		if target.SecretEnv != ".secret.env" {
			t.Errorf("SecretEnv = %q, want .secret.env", target.SecretEnv)
		}
		if target.Env != "plain.env" {
			t.Errorf("Env = %q, want plain.env", target.Env)
		}
	})

	t.Run("empty local fields leave canonical defaults in place", func(t *testing.T) {
		target := &PulledApp{
			SecretEnv: ".runos.k1.ab12c.env",
			Env:       "runos.k1.ab12c.config.env",
		}
		local := &PulledApp{} // user has neither field set
		MergeUserEnvPaths(target, local)
		if target.SecretEnv != ".runos.k1.ab12c.env" {
			t.Errorf("SecretEnv = %q, want canonical default preserved", target.SecretEnv)
		}
		if target.Env != "runos.k1.ab12c.config.env" {
			t.Errorf("Env = %q, want canonical default preserved", target.Env)
		}
	})

	t.Run("partial: only one side overridden locally", func(t *testing.T) {
		target := &PulledApp{
			SecretEnv: ".runos.k1.ab12c.env",
			Env:       "runos.k1.ab12c.config.env",
		}
		local := &PulledApp{SecretEnv: ".secret.env"} // local custom secret only
		MergeUserEnvPaths(target, local)
		if target.SecretEnv != ".secret.env" {
			t.Errorf("SecretEnv = %q, want .secret.env", target.SecretEnv)
		}
		if target.Env != "runos.k1.ab12c.config.env" {
			t.Errorf("Env = %q, want canonical Env preserved when local empty", target.Env)
		}
	})

	t.Run("nil-safe", func(t *testing.T) {
		MergeUserEnvPaths(nil, nil)
		MergeUserEnvPaths(&PulledApp{}, nil)
		MergeUserEnvPaths(nil, &PulledApp{})
		// No panic, no crash.
	})
}

// TestPulledApp_SourceDirRoundTrip mirrors the DeployConfig round-trip
// guard. The on-disk yaml shape must agree between pull and deploy so
// pulled yamls re-deploy cleanly.
func TestPulledApp_SourceDirRoundTrip(t *testing.T) {
	t.Run("set sourceDir survives marshal -> unmarshal", func(t *testing.T) {
		app := &PulledApp{App: "web", ID: "appid4", CID: "mycluster3", AID: "myacct", SourceDir: ".."}
		out, err := yaml.Marshal(app)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(out), "sourceDir: ..") {
			t.Errorf("marshaled yaml missing sourceDir: ..\n%s", out)
		}
		var got PulledApp
		if err := yaml.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.SourceDir != ".." {
			t.Errorf("round-tripped SourceDir = %q, want %q", got.SourceDir, "..")
		}
	})

	t.Run("empty sourceDir is omitted from output", func(t *testing.T) {
		app := &PulledApp{App: "web", ID: "appid4", CID: "mycluster3", AID: "myacct"}
		out, err := yaml.Marshal(app)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), "sourceDir:") {
			t.Errorf("empty SourceDir must be omitted; got:\n%s", out)
		}
	})
}

func TestFilenameBuilders(t *testing.T) {
	// Empty per-app dir: YAMLFilename returns the canonical "runos.yaml".
	// Multi-yaml resolution is covered by TestYAMLFilename_* below.
	dir := t.TempDir()
	got, err := YAMLFilename(dir, "k1", "ab12c")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.yaml" {
		t.Errorf("YAMLFilename(empty dir) = %q, want runos.yaml", got)
	}
	if got := SuffixedYAMLFilename("k1", "ab12c"); got != "runos.k1.ab12c.yaml" {
		t.Errorf("SuffixedYAMLFilename = %q, want runos.k1.ab12c.yaml", got)
	}
	if got := SecretEnvFilename("k1", "ab12c"); got != ".runos.k1.ab12c.env" {
		t.Errorf("SecretEnvFilename(k1, ab12c) = %q, want .runos.k1.ab12c.env", got)
	}
	if got := SecretEnvFilename("PROD-01", "AB12C"); got != ".runos.prod-01.ab12c.env" {
		t.Errorf("SecretEnvFilename(PROD-01, AB12C) = %q, want .runos.prod-01.ab12c.env", got)
	}
	if got := EnvFilename("k1", "ab12c"); got != "runos.k1.ab12c.config.env" {
		t.Errorf("EnvFilename(k1, ab12c) = %q, want runos.k1.ab12c.config.env", got)
	}
	// Lowercases inputs, matching DefaultBaseName.
	if got := EnvFilename("PROD-01", "AB12C"); got != "runos.prod-01.ab12c.config.env" {
		t.Errorf("EnvFilename(PROD-01, AB12C) = %q, want runos.prod-01.ab12c.config.env", got)
	}
}

// ---------------------------------------------------------------------------
// asInt
// ---------------------------------------------------------------------------

func TestAsInt(t *testing.T) {
	tests := []struct {
		name  string
		in    any
		want  int
		wantOK bool
	}{
		{"int", 42, 42, true},
		{"int64", int64(42), 42, true},
		{"float64 (JSON number)", float64(42), 42, true},
		{"float64 with fraction truncates", float64(42.9), 42, true},
		{"string is not a number", "42", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := asInt(tt.in)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("asInt(%v) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildPulledApp
// ---------------------------------------------------------------------------

func TestBuildPulledApp_CliDeploy_RRCPreset(t *testing.T) {
	// Mirrors the "greenfingers" shape from production data.
	raw := map[string]any{
		"id":                         "appid4",
		"name":                       "greenfingers",
		"replicas":                   float64(1),
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"integrationType":            nil,
		"cpuRequestMc":               float64(0),
		"cpuLimitMc":                 float64(1000),
		"memoryRequestMb":            float64(0),
		"memoryLimitMb":              float64(2048),
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": true},
		},
	}

	p := BuildPulledApp(raw, "mycluster3", "myacct")

	if p.DeployType != "cli" {
		t.Errorf("DeployType = %q, want cli", p.DeployType)
	}
	if p.Integration != nil {
		t.Errorf("Integration should be nil for cli deploy, got %+v", p.Integration)
	}
	if p.ResourceRequirementClassID != "app.sl1.beff" {
		t.Errorf("ResourceRequirementClassID = %q, want app.sl1.beff", p.ResourceRequirementClassID)
	}
	if p.CPURequestMc != nil || p.CPULimitMc != nil || p.MemoryRequestMb != nil || p.MemoryLimitMb != nil {
		t.Errorf("cpu/mem pointers should be nil when rrc preset is set")
	}
	if len(p.ServicePortMappings) != 1 || p.ServicePortMappings[0].Port != 8080 || !p.ServicePortMappings[0].StandardHttps {
		t.Errorf("Ports = %+v, want [{8080 true}]", p.ServicePortMappings)
	}
	if p.HealthCheck != "" {
		t.Errorf("HealthCheck should be empty when server reports none, got %q", p.HealthCheck)
	}
	if p.MetricsPort != nil {
		t.Errorf("MetricsPort should be nil when server reports none")
	}
}

func TestBuildPulledApp_VcsIntegration_PassesThroughIntegrationType(t *testing.T) {
	// Mirrors the "Laravel from Gitlab" shape. Server now emits both
	// `deployType: vcs` (the canonical 'cli' | 'vcs' discriminator) and
	// `integrationType: gitlab-runner` (provider slug). The yaml stores
	// `deployType: vcs`; provider identity flows via the Integration block.
	raw := map[string]any{
		"id":                         "wr7yu",
		"name":                       "Laravel from Gitlab",
		"replicas":                   float64(1),
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"deployType":                 "vcs",
		"integrationType":            "gitlab-runner",
		"vcsIntegrationId":           "tr6mj",
		"repoId":                     8.1034699e+07,
		"repoName":                   "runos-tests/laravel-v1",
		"branchName":                 "main",
		"healthCheck":                "none",
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": false},
		},
	}

	p := BuildPulledApp(raw, "mycluster3", "myacct")

	if p.DeployType != "vcs" {
		t.Errorf("DeployType = %q, want vcs", p.DeployType)
	}
	if p.Integration == nil {
		t.Fatal("Integration should be populated for vcs deployType")
	}
	if p.Integration.ID != "tr6mj" {
		t.Errorf("Integration.ID = %q, want tr6mj", p.Integration.ID)
	}
	if p.Integration.RepoID != 81034699 {
		t.Errorf("Integration.RepoID = %d, want 81034699 (float coercion)", p.Integration.RepoID)
	}
	if p.Integration.RepoName != "runos-tests/laravel-v1" {
		t.Errorf("Integration.RepoName = %q", p.Integration.RepoName)
	}
	if p.Integration.BranchName != "main" {
		t.Errorf("Integration.BranchName = %q", p.Integration.BranchName)
	}
	if p.HealthCheck != "none" {
		t.Errorf("HealthCheck = %q, want %q", p.HealthCheck, "none")
	}
}

// I27-M/N regression: pulled VCS yaml must surface the three
// build-metadata fields (configPath / sourceDir / dockerfile) so a fresh
// `git clone && runos apps pull` produces a yaml that carries every
// breadcrumb the user needs for a subsequent `runos deploy --sha <sha>`
// from any path. Conductor 17.7.0+ stores all three on the AppDocument;
// the CLI's reader has to surface them through BuildPulledApp.
func TestBuildPulledApp_VcsBuildMetadataRoundTrip(t *testing.T) {
	raw := map[string]any{
		"id":                         "ultbd",
		"name":                       "iter27-api",
		"replicas":                   float64(1),
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"deployType":                 "vcs",
		"integrationType":            "gitlab-runner",
		"vcsIntegrationId":           "tr6mj",
		"repoId":                     8.2177108e+07,
		"repoName":                   "runos-tests/iter27-monorepo",
		"branchName":                 "main",
		// I27-M/N canonical monorepo shape: yaml lives in apps/api/infra/
		// and sourceDir traverses up to the repo root.
		"configPath":  "apps/api/infra/runos-prod.yaml",
		"sourceDir":   "../../..",
		"dockerfile":  "apps/api/Dockerfile",
		"healthCheck": "standard",
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": true},
		},
	}

	p := BuildPulledApp(raw, "mycluster2", "myacct")

	if p.ConfigPath != "apps/api/infra/runos-prod.yaml" {
		t.Errorf("ConfigPath = %q, want %q", p.ConfigPath, "apps/api/infra/runos-prod.yaml")
	}
	if p.SourceDir != "../../.." {
		t.Errorf("SourceDir = %q, want %q", p.SourceDir, "../../..")
	}
	if p.Dockerfile != "apps/api/Dockerfile" {
		t.Errorf("Dockerfile = %q, want %q", p.Dockerfile, "apps/api/Dockerfile")
	}
}

// I27-M/N partner: CLI-deploy apps (no configPath stored server-side)
// pull a yaml that omits configPath entirely (omitempty drops the key
// rather than emitting `configPath: ""`). Mirrors the existing
// sourceDir / dockerfile omitempty behaviour for the same reason: a
// CLI-deploy app's yaml should stay uncluttered when no build-metadata
// is set.
func TestBuildPulledApp_CliDeployOmitsConfigPath(t *testing.T) {
	raw := map[string]any{
		"id":                         "y2w1y",
		"name":                       "urlshort",
		"replicas":                   float64(1),
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"deployType":                 "cli",
		"healthCheck":                "none",
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": false},
		},
	}

	p := BuildPulledApp(raw, "mycluster2", "myacct")
	if p.ConfigPath != "" {
		t.Errorf("CLI-deploy app should have empty ConfigPath; got %q", p.ConfigPath)
	}
}

func TestBuildPulledApp_CustomResources_EmitsAllFourFieldsEvenWhenZero(t *testing.T) {
	tests := []struct {
		name           string
		rrc            any
		wantClassLabel string // expected ResourceRequirementClassID after pull
	}{
		// rrc missing / "": pulled yaml carries no class label, just the
		// materialised cpu/memory fields. (Server-side this maps to a
		// "custom" record either way; the wire just omitted the field.)
		{"rrc missing", nil, ""},
		{"rrc empty string", "", ""},
		// rrc "custom": pulled yaml preserves the literal "custom" label
		// so apps_show and apps_pull return symmetric views. Materialised
		// cpu/memory fields are still emitted alongside.
		{"rrc literal 'custom'", "custom", "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{
				"name":            "svc",
				"cpuRequestMc":    float64(0), // zero is still a set value in custom mode
				"cpuLimitMc":      float64(500),
				"memoryRequestMb": float64(0),
				"memoryLimitMb":   float64(1024),
			}
			if tt.rrc != nil {
				raw["resourceRequirementClassId"] = tt.rrc
			}

			p := BuildPulledApp(raw, "k1", "acc-1")

			if p.ResourceRequirementClassID != tt.wantClassLabel {
				t.Errorf("ResourceRequirementClassID = %q, want %q", p.ResourceRequirementClassID, tt.wantClassLabel)
			}
			if p.CPURequestMc == nil || *p.CPURequestMc != 0 {
				t.Errorf("CPURequestMc = %v, want *0", p.CPURequestMc)
			}
			if p.CPULimitMc == nil || *p.CPULimitMc != 500 {
				t.Errorf("CPULimitMc = %v, want *500", p.CPULimitMc)
			}
			if p.MemoryRequestMb == nil || *p.MemoryRequestMb != 0 {
				t.Errorf("MemoryRequestMb = %v, want *0", p.MemoryRequestMb)
			}
			if p.MemoryLimitMb == nil || *p.MemoryLimitMb != 1024 {
				t.Errorf("MemoryLimitMb = %v, want *1024", p.MemoryLimitMb)
			}
		})
	}
}

func TestBuildPulledApp_EmptyPortsForWorker(t *testing.T) {
	raw := map[string]any{"name": "worker", "replicas": float64(3)}

	p := BuildPulledApp(raw, "k1", "acc-1")

	if p.ServicePortMappings == nil {
		t.Fatal("Ports must be an empty slice, not nil, so YAML emits [] instead of null")
	}
	if len(p.ServicePortMappings) != 0 {
		t.Errorf("Ports = %+v, want []", p.ServicePortMappings)
	}
}

func TestBuildPulledApp_SecretFilesNilByDefault(t *testing.T) {
	// SecretFiles must start as nil so yaml omits the key entirely when
	// the app has no secret files. Present-but-empty ([]) would be
	// authoritative for a future up-sync ("delete all secret files"),
	// which is a destructive default we don't want on pull.
	p := BuildPulledApp(map[string]any{"name": "svc"}, "k1", "acc-1")

	if p.SecretFiles != nil {
		t.Fatalf("SecretFiles should be nil (omitted from yaml), got %+v", p.SecretFiles)
	}
}

func TestBuildPulledApp_SecretFilesKeyOmittedInYAMLWhenNil(t *testing.T) {
	p := BuildPulledApp(map[string]any{"name": "svc"}, "k1", "acc-1")

	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	if strings.Contains(string(out), "secretFiles") {
		t.Errorf("yaml should not contain secretFiles key when slice is nil:\n%s", out)
	}
}

func TestPulledApp_EnvKeyOmittedInYAMLWhenEmpty(t *testing.T) {
	p := BuildPulledApp(map[string]any{"name": "svc"}, "k1", "acc-1")
	// Leave Env unset; on a real pull the cmd layer only assigns it when
	// the app has at least one env var.
	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	if strings.Contains(string(out), "env:") {
		t.Errorf("yaml should not contain env key when Env is empty:\n%s", out)
	}
}

func TestPulledApp_EnvKeyPresentInYAMLWhenSet(t *testing.T) {
	p := BuildPulledApp(map[string]any{"name": "svc"}, "k1", "acc-1")
	p.Env = ".runos.k1.svc.env"

	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	if !strings.Contains(string(out), "env: .runos.k1.svc.env") {
		t.Errorf("yaml should contain env key when Env is set:\n%s", out)
	}
}

func TestBuildPulledApp_SecretFilesKeyPresentInYAMLWhenSet(t *testing.T) {
	p := BuildPulledApp(map[string]any{"name": "svc"}, "k1", "acc-1")
	p.SecretFiles = []SecretFile{
		{Filename: "server.crt", MountPath: "/etc/ssl/server.crt", Local: ".x.secret-files/server.crt", MD5: "abc"},
	}

	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "secretFiles:") {
		t.Errorf("yaml should contain secretFiles key when slice is populated:\n%s", text)
	}
	if !strings.Contains(text, "filename: server.crt") {
		t.Errorf("yaml should contain file entry:\n%s", text)
	}
}

func TestBuildPulledApp_HealthCheckFullyPopulated(t *testing.T) {
	raw := map[string]any{
		"name":            "svc",
		"healthCheck":     "standard",
		"healthCheckPort": float64(8080),
		"healthCheckPath": "/healthz",
	}

	p := BuildPulledApp(raw, "k1", "acc-1")

	if p.HealthCheck != "standard" {
		t.Errorf("HealthCheck = %q, want standard", p.HealthCheck)
	}
	if p.HealthCheckPort == nil || *p.HealthCheckPort != 8080 {
		t.Errorf("HealthCheckPort = %v, want *8080", p.HealthCheckPort)
	}
	if p.HealthCheckPath != "/healthz" {
		t.Errorf("HealthCheckPath = %q, want /healthz", p.HealthCheckPath)
	}
}

func TestBuildPulledApp_MetricsPopulatedWhenPortSet(t *testing.T) {
	raw := map[string]any{
		"name":        "svc",
		"metricsPort": float64(9090),
		"metricsPath": "/metrics",
	}

	p := BuildPulledApp(raw, "k1", "acc-1")

	if p.MetricsPort == nil || *p.MetricsPort != 9090 {
		t.Errorf("MetricsPort = %v, want *9090", p.MetricsPort)
	}
	if p.MetricsPath != "/metrics" {
		t.Errorf("MetricsPath = %q, want /metrics", p.MetricsPath)
	}
}

func TestBuildPulledApp_MetricsOmittedWhenPortZero(t *testing.T) {
	raw := map[string]any{
		"name":        "svc",
		"metricsPort": float64(0),
		"metricsPath": "/metrics",
	}

	p := BuildPulledApp(raw, "k1", "acc-1")

	if p.MetricsPort != nil {
		t.Errorf("MetricsPort should be nil when port is 0, got *%d", *p.MetricsPort)
	}
}

// YAML round-trip smoke test: ensures the on-disk file matches the expected
// ordering and shape for a realistic full-featured app.
func TestBuildPulledApp_YAMLOutputMatchesExpectedShape(t *testing.T) {
	raw := map[string]any{
		"id":                         "wr7yu",
		"name":                       "Laravel from Gitlab",
		"replicas":                   float64(1),
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"deployType":                 "vcs",
		"integrationType":            "gitlab-runner",
		"vcsIntegrationId":           "tr6mj",
		"repoId":                     8.1034699e+07,
		"repoName":                   "runos-tests/laravel-v1",
		"branchName":                 "main",
		"healthCheck":                "none",
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": false},
		},
	}
	p := BuildPulledApp(raw, "mycluster3", "myacct")
	p.Env = ".runos.mycluster3.laravel-from-gitlab.env"

	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	got := string(out)

	// Verify ordering: app first, deployType second, id/cid/aid, then env,
	// replicas, clusterDomainId, rrc, integration, ports, healthCheck.
	// secretFiles intentionally absent from this ordering check because this
	// test doesn't populate any; its presence/absence is covered separately.
	expectedOrder := []string{
		"app:", "deployType:", "id:", "cid:", "aid:", "env:", "replicas:",
		"clusterDomainId:", "resourceRequirementClassId:",
		"integration:", "servicePortMappings:", "healthCheck:",
	}
	lastIdx := -1
	for _, key := range expectedOrder {
		idx := strings.Index(got, key)
		if idx == -1 {
			t.Errorf("missing key %q in output:\n%s", key, got)
			continue
		}
		if idx < lastIdx {
			t.Errorf("key %q appeared before expected predecessor in:\n%s", key, got)
		}
		lastIdx = idx
	}

	// Things that must NOT be in the file any more.
	for _, bad := range []string{"osid:", "status:", "domain:", "networkAccess:", "extras:", "dockerfile:", "createdAt:", "updatedAt:"} {
		if strings.Contains(got, bad) {
			t.Errorf("output should not contain %q:\n%s", bad, got)
		}
	}

	// repoId must be a plain integer, not scientific notation.
	if !strings.Contains(got, "repoId: 81034699") {
		t.Errorf("repoId should render as 81034699 (int), got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// SaveYAML + SaveEnv (filesystem behaviour)
// ---------------------------------------------------------------------------

func TestSaveYAML_WritesMarshaledFileWith0644(t *testing.T) {
	dir := t.TempDir()
	app := &PulledApp{
		App:        "web",
		DeployType: "cli",
		ID:         "ab12c",
		CID:        "k1",
		AID:        "acc-1",
		Replicas:   1,
		ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}},
	}
	base := DefaultBaseName(app.CID, app.ID)

	res, err := SaveYAML(dir, base, app)
	if err != nil {
		t.Fatalf("SaveYAML: %v", err)
	}
	if res.InSync {
		t.Errorf("first write should not be InSync, got %+v", res)
	}

	want := filepath.Join(dir, "runos.k1.ab12c", "runos.yaml")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}

	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat yaml: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("yaml perm = %o, want 0644", perm)
		}
	}

	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	var round PulledApp
	if err := yaml.Unmarshal(data, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if round.App != app.App || round.ID != app.ID || round.CID != app.CID {
		t.Errorf("round-trip mismatch: %+v", round)
	}
	if len(round.ServicePortMappings) != 1 || round.ServicePortMappings[0].Port != 3000 || !round.ServicePortMappings[0].StandardHttps {
		t.Errorf("ports round-trip mismatch: %+v", round.ServicePortMappings)
	}
	// Sanity: output should use the expected camelCase top-level keys.
	text := string(data)
	for _, key := range []string{"app:", "deployType:", "id:", "cid:", "aid:", "replicas:", "servicePortMappings:"} {
		if !strings.Contains(text, key) {
			t.Errorf("expected key %q in yaml output, got:\n%s", key, text)
		}
	}
}

func TestSaveYAML_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	app := &PulledApp{
		App:        "web",
		DeployType: "cli",
		ID:         "ab12c",
		CID:        "k1",
		AID:        "acc-1",
		Replicas:   1,
		ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}},
	}
	base := DefaultBaseName(app.CID, app.ID)

	if _, err := SaveYAML(dir, base, app); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Mutate the in-memory app so the second write is distinguishable.
	app.ServicePortMappings = []Port{{Port: 9000, StandardHttps: true}}
	res, err := SaveYAML(dir, base, app)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if res.InSync {
		t.Error("second save should not be InSync when content differs")
	}

	// Content should reflect the new bytes; only one file lives in the dir
	// (no timestamped backup, because rotation is gone).
	newBytes, _ := os.ReadFile(res.Path)
	if !strings.Contains(string(newBytes), "port: 9000") {
		t.Errorf("new file missing updated port, content:\n%s", newBytes)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 file (no backup), got %d: %v", len(entries), names)
	}
}

func TestSaveEnv_WritesSortedKeysWith0600(t *testing.T) {
	dir := t.TempDir()
	envs := map[string]string{
		"DATABASE_URL": "postgres://...",
		"API_KEY":      "secret",
		"LOG_LEVEL":    "info",
	}

	base := DefaultBaseName("k1", "ab12c")
	// The 0600-perm path is the sensitive Secret-backed env file. The
	// plain-file SaveEnv variant uses 0644 by design (committed to VCS).
	res, err := SaveSecretEnv(dir, base, "k1", "ab12c", envs)
	if err != nil {
		t.Fatalf("SaveSecretEnv: %v", err)
	}
	want := filepath.Join(dir, "runos.k1.ab12c", ".runos.k1.ab12c.env")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}

	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat env: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("env perm = %o, want 0600", perm)
		}
	}

	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	want = "API_KEY=secret\nDATABASE_URL=postgres://...\nLOG_LEVEL=info\n"
	if string(data) != want {
		t.Errorf("env content =\n%q\nwant\n%q", data, want)
	}
}

// ---------------------------------------------------------------------------
// YAMLFilename — multi-yaml resolution
// ---------------------------------------------------------------------------

func TestYAMLFilename_EmptyDirIsRunosYaml(t *testing.T) {
	got, err := YAMLFilename(t.TempDir(), "mycluster3", "appid4")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.yaml" {
		t.Errorf("got %q, want runos.yaml (empty dir is back-compat default)", got)
	}
}

func TestYAMLFilename_NonExistentDirIsRunosYaml(t *testing.T) {
	// ensureAppDir will create the dir on the next call; the resolver
	// must not fail just because the dir is absent.
	parent := t.TempDir()
	got, err := YAMLFilename(filepath.Join(parent, "nope"), "mycluster3", "appid4")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.yaml" {
		t.Errorf("got %q, want runos.yaml for non-existent dir", got)
	}
}

func TestYAMLFilename_SameAppReusesRunosYaml(t *testing.T) {
	// Single-app project re-pull: existing runos.yaml parses to the
	// same (cid, id), so the canonical name stays.
	dir := t.TempDir()
	body := "app: web\ndeployType: cli\nid: appid4\ncid: mycluster3\naid: myacct\n"
	if err := os.WriteFile(filepath.Join(dir, "runos.yaml"), []byte(body), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := YAMLFilename(dir, "mycluster3", "appid4")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.yaml" {
		t.Errorf("got %q, want runos.yaml (same app should reuse the canonical leaf)", got)
	}
}

func TestYAMLFilename_OccupiedByDifferentAppSuffixes(t *testing.T) {
	// runos.yaml is for cluster mycluster3 app A; we're pulling cluster mycluster2
	// app B. The resolver must hand back the suffixed name so the
	// new pull does not clobber the existing manifest.
	dir := t.TempDir()
	bodyA := "app: web\ndeployType: cli\nid: appid4\ncid: mycluster3\naid: myacct\n"
	if err := os.WriteFile(filepath.Join(dir, "runos.yaml"), []byte(bodyA), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := YAMLFilename(dir, "mycluster2", "appid5")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.mycluster2.appid5.yaml" {
		t.Errorf("got %q, want runos.mycluster2.appid5.yaml (occupied dir must auto-suffix)", got)
	}
}

func TestYAMLFilename_PerAppFilenameAlreadyExistsWins(t *testing.T) {
	// Re-pull of an app whose manifest already lives under the suffixed
	// name. The resolver must return that name even when a separate
	// runos.yaml exists for a different app.
	dir := t.TempDir()
	bodyA := "app: web\ndeployType: cli\nid: appid4\ncid: mycluster3\naid: myacct\n"
	bodyB := "app: api\ndeployType: cli\nid: appid5\ncid: mycluster2\naid: myacct\n"
	if err := os.WriteFile(filepath.Join(dir, "runos.yaml"), []byte(bodyA), 0644); err != nil {
		t.Fatalf("seed runos.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runos.mycluster2.appid5.yaml"), []byte(bodyB), 0644); err != nil {
		t.Fatalf("seed suffixed: %v", err)
	}
	got, err := YAMLFilename(dir, "mycluster2", "appid5")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.mycluster2.appid5.yaml" {
		t.Errorf("got %q, want runos.mycluster2.appid5.yaml (suffixed file must win when present)", got)
	}
}

func TestYAMLFilename_UnparseableRunosYamlSuffixes(t *testing.T) {
	// A garbage runos.yaml in the dir is treated as occupied. The
	// resolver must not overwrite it with the new app's content.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runos.yaml"), []byte("::: not yaml :::\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := YAMLFilename(dir, "mycluster3", "appid4")
	if err != nil {
		t.Fatalf("YAMLFilename: %v", err)
	}
	if got != "runos.mycluster3.appid4.yaml" {
		t.Errorf("got %q, want runos.mycluster3.appid4.yaml (unparseable runos.yaml must not be clobbered)", got)
	}
}

func TestSaveYAML_AutoSuffixesWhenDirOccupied(t *testing.T) {
	// Integration: SaveYAML resolves the leaf via YAMLFilename and writes
	// to the right file. Two consecutive saves (different apps) must both
	// land on disk without clobber.
	dir := t.TempDir()

	first := &PulledApp{
		App: "web", DeployType: "cli",
		ID: "appid4", CID: "mycluster3", AID: "myacct",
		Replicas: 1,
	}
	resA, err := SaveYAML(dir, "", first)
	if err != nil {
		t.Fatalf("save A: %v", err)
	}
	if filepath.Base(resA.Path) != "runos.yaml" {
		t.Errorf("first save should land on runos.yaml; got %q", resA.Path)
	}

	second := &PulledApp{
		App: "api", DeployType: "cli",
		ID: "appid5", CID: "mycluster2", AID: "myacct",
		Replicas: 1,
	}
	resB, err := SaveYAML(dir, "", second)
	if err != nil {
		t.Fatalf("save B: %v", err)
	}
	if filepath.Base(resB.Path) != "runos.mycluster2.appid5.yaml" {
		t.Errorf("second save should auto-suffix; got %q", resB.Path)
	}

	// Original yaml must be untouched.
	data, err := os.ReadFile(resA.Path)
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	var roundA PulledApp
	if err := yaml.Unmarshal(data, &roundA); err != nil {
		t.Fatalf("unmarshal A: %v", err)
	}
	if roundA.ID != "appid4" || roundA.CID != "mycluster3" {
		t.Errorf("first yaml clobbered; got %+v", roundA)
	}
}

// ---------------------------------------------------------------------------
// EnsureDockerignore
// ---------------------------------------------------------------------------

func TestEnsureDockerignore_CreatesWhenAbsent(t *testing.T) {
	dir := t.TempDir()

	res, err := EnsureDockerignore(dir)
	if err != nil {
		t.Fatalf("EnsureDockerignore: %v", err)
	}
	if res.InSync {
		t.Errorf("first call must report a write (InSync=false), got %+v", res)
	}
	wantPath := filepath.Join(dir, ".dockerignore")
	if res.Path != wantPath {
		t.Errorf("Path = %q, want %q", res.Path, wantPath)
	}

	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	// Spot-check every pattern the security guarantee depends on. The
	// precise text of the header comment is not load-bearing.
	for _, pattern := range []string{
		"runos.yaml",
		"runos.*.yaml",
		"runos.*.yml",
		".runos.*.env",
		".runos*.source-version",
		".secret-files/",
		"overrides/",
	} {
		if !strings.Contains(string(got), pattern) {
			t.Errorf("default .dockerignore missing pattern %q; got:\n%s", pattern, got)
		}
	}
}

func TestEnsureDockerignore_PreservesExisting(t *testing.T) {
	// Pre-existing .dockerignore must never be modified, even if it
	// fails to exclude every RunOS-managed file. The tarball walker is
	// the security boundary; this helper only writes documentation when
	// there's no user file to fight with.
	dir := t.TempDir()
	custom := "node_modules/\n*.log\n"
	path := filepath.Join(dir, ".dockerignore")
	if err := os.WriteFile(path, []byte(custom), 0644); err != nil {
		t.Fatalf("seed .dockerignore: %v", err)
	}

	res, err := EnsureDockerignore(dir)
	if err != nil {
		t.Fatalf("EnsureDockerignore: %v", err)
	}
	if !res.InSync {
		t.Errorf("pre-existing .dockerignore should produce InSync=true; got %+v", res)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	if string(got) != custom {
		t.Errorf("existing .dockerignore must not be rewritten; got %q want %q", got, custom)
	}
}

// ---------------------------------------------------------------------------
// Override helpers
// ---------------------------------------------------------------------------

func TestOverridesDirname(t *testing.T) {
	if got := OverridesDirname(); got != "overrides" {
		t.Errorf("OverridesDirname = %q, want overrides", got)
	}
}

func TestSanitizeOverrideName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Deployed By RunOS", "deployed-by-runos"},
		{"pod-security", "pod-security"},
		{"  leading space", "leading-space"},
		{"Multiple    Spaces", "multiple-spaces"},
		{"!!!only-symbols!!!", "only-symbols"},
		{"unicode: 日本", "unicode"},
		{"CamelCaseName", "camelcasename"},
		{"", ""},
		// Edge cases: filesystem-meaningful names must be rejected.
		{".", ""},
		{"..", ""},
		{".hidden", "hidden"},
		{"...", ""},
		{"..foo", "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitizeOverrideName(tt.in); got != tt.want {
				t.Errorf("sanitizeOverrideName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateIdentifier(t *testing.T) {
	t.Run("rejects empty", func(t *testing.T) {
		if err := ValidateIdentifier("app id", ""); err == nil {
			t.Fatal("expected error for empty value")
		}
	})

	t.Run("accepts alphanumeric, dash, underscore", func(t *testing.T) {
		good := []string{
			"abcde",
			"ABC123",
			"app-id",
			"app_id",
			"a", // single char
			"AppID-123_xyz",
		}
		for _, v := range good {
			if err := ValidateIdentifier("app id", v); err != nil {
				t.Errorf("ValidateIdentifier(%q) returned %v, want nil", v, err)
			}
		}
	})

	t.Run("rejects path traversal and special chars", func(t *testing.T) {
		bad := []string{
			"../etc",
			"foo/bar",
			"foo\\bar",
			"foo bar",        // space
			"foo.bar",        // dot
			"foo\x00bar",     // null byte
			"foo;ls",         // shell metacharacter
			"foo\nbar",       // newline
			"foo:bar",        // colon
			"日本",             // non-ASCII
			"appid4/../tmp",   // documented attack shape
		}
		for _, v := range bad {
			if err := ValidateIdentifier("app id", v); err == nil {
				t.Errorf("ValidateIdentifier(%q) returned nil, want error", v)
			}
		}
	})

	t.Run("error mentions kind label", func(t *testing.T) {
		err := ValidateIdentifier("cluster id", "../bad")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "cluster id") {
			t.Errorf("error %q should mention kind label", err.Error())
		}
	})
}

func TestOverrideFilenames_NoCollisions(t *testing.T) {
	got := OverrideFilenames([]OverrideSummary{
		{ID: "abc123xyz", Name: "Deployed By RunOS"},
		{ID: "defghiuvw", Name: "Priority Class"},
	})
	want := []string{"deployed-by-runos.yaml", "priority-class.yaml"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("filenames = %v, want %v", got, want)
	}
}

func TestOverrideFilenames_DisambiguatesOnCollision(t *testing.T) {
	got := OverrideFilenames([]OverrideSummary{
		{ID: "firstId000", Name: "Same Name"},
		{ID: "secondId00", Name: "Same Name"},
	})
	// Both must have unique suffixes when names collide.
	if got[0] == got[1] {
		t.Fatalf("filenames collided: %v", got)
	}
	for _, name := range got {
		if !strings.HasPrefix(name, "same-name-") {
			t.Errorf("expected collision-suffixed name, got %q", name)
		}
	}
}

func TestOverrideFilenames_FallsBackToIdWhenNameEmpty(t *testing.T) {
	got := OverrideFilenames([]OverrideSummary{
		{ID: "abc123xyz", Name: ""},
	})
	if got[0] != "abc123.yaml" {
		t.Errorf("filename = %q, want abc123.yaml", got[0])
	}
}

func TestSaveOverride_Writes0644InOverridesDir(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")
	content := []byte("spec:\n  replicas: 2\n")

	res, err := SaveOverride(dir, base, "pod-security.yaml", content)
	if err != nil {
		t.Fatalf("SaveOverride: %v", err)
	}

	wantDir := filepath.Join(dir, "runos.k1.ab12c", "overrides")
	wantFile := filepath.Join(wantDir, "pod-security.yaml")
	if res.Path != wantFile {
		t.Errorf("Path = %q, want %q", res.Path, wantFile)
	}

	if runtime.GOOS != "windows" {
		// Overrides are plain manifest fragments, not secrets, so both the
		// directory and file should be world-readable (0755 / 0644).
		dirInfo, err := os.Stat(wantDir)
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o755 {
			t.Errorf("dir perm = %o, want 0755", perm)
		}
		fileInfo, err := os.Stat(res.Path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := fileInfo.Mode().Perm(); perm != 0o644 {
			t.Errorf("file perm = %o, want 0644", perm)
		}
	}

	got, _ := os.ReadFile(res.Path)
	if string(got) != string(content) {
		t.Errorf("content mismatch: %q", got)
	}
}

func TestSaveOverride_RejectsPathSeparators(t *testing.T) {
	_, err := SaveOverride(t.TempDir(), "runos.k1.web", "../escape.yaml", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "path separator") {
		t.Fatalf("expected path separator error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// InSync (no-op) behaviour
// ---------------------------------------------------------------------------

func TestSaveYAML_InSyncWhenContentMatches(t *testing.T) {
	dir := t.TempDir()
	app := &PulledApp{
		App:        "web",
		DeployType: "cli",
		ID:         "ab12c",
		CID:        "k1",
		AID:        "acc-1",
		Replicas:   1,
		ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}},
	}
	base := DefaultBaseName(app.CID, app.ID)

	first, err := SaveYAML(dir, base, app)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if first.InSync {
		t.Fatalf("first save should not be InSync, got %+v", first)
	}

	// Second save with identical content should be a no-op.
	firstInfo, _ := os.Stat(first.Path)
	second, err := SaveYAML(dir, base, app)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if !second.InSync {
		t.Errorf("expected InSync=true, got %+v", second)
	}

	// Verify the file on disk was not rewritten (mod time unchanged) and
	// no backup was created.
	secondInfo, _ := os.Stat(second.Path)
	if !secondInfo.ModTime().Equal(firstInfo.ModTime()) {
		t.Errorf("file was rewritten even though content matched")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 file in dir, got %d: %v", len(entries), names)
	}
}

func TestSaveEnv_InSyncWhenContentMatches(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")
	vars := map[string]string{"A": "1", "B": "2"}

	if _, err := SaveEnv(dir, base, "k1", "ab12c", vars); err != nil {
		t.Fatalf("first: %v", err)
	}

	res, err := SaveEnv(dir, base, "k1", "ab12c", vars)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !res.InSync {
		t.Errorf("expected InSync=true, got %+v", res)
	}
}

func TestSaveSecretFile_InSyncWhenContentMatches(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")
	content := []byte("-----BEGIN CERT-----\n...\n")

	if _, err := SaveSecretFile(dir, base, "server.crt", content); err != nil {
		t.Fatalf("first: %v", err)
	}

	res, err := SaveSecretFile(dir, base, "server.crt", content)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !res.InSync {
		t.Errorf("expected InSync=true, got %+v", res)
	}

	// No backup file should have been created alongside.
	dirEntries, _ := os.ReadDir(filepath.Join(dir, "runos.k1.ab12c", ".secret-files"))
	if len(dirEntries) != 1 {
		names := []string{}
		for _, e := range dirEntries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 file in secret-files dir, got %d: %v", len(dirEntries), names)
	}
}

func TestSaveEnv_EmptyMapProducesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	res, err := SaveEnv(dir, DefaultBaseName("k1", "ab12c"), "k1", "ab12c", map[string]string{})
	if err != nil {
		t.Fatalf("SaveEnv: %v", err)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %q", data)
	}
}

// ---------------------------------------------------------------------------
// Secret file helpers
// ---------------------------------------------------------------------------

func TestValidateSecretFilename(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"plain", "server.crt", ""},
		{"dotted", ".env", ""},
		{"empty", "", "empty"},
		{"forward slash", "sub/foo", "path separator"},
		{"back slash", `sub\foo`, "path separator"},
		{"dot", ".", "cannot be"},
		{"dotdot", "..", "cannot be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSecretFilename(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSecretFilesDirname(t *testing.T) {
	if got := SecretFilesDirname(); got != ".secret-files" {
		t.Errorf("SecretFilesDirname = %q, want .secret-files", got)
	}
}

func TestSaveSecretFile_CreatesDir0700File0600(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")
	content := []byte("-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n")

	res, err := SaveSecretFile(dir, base, "server.crt", content)
	if err != nil {
		t.Fatalf("SaveSecretFile: %v", err)
	}

	wantDir := filepath.Join(dir, "runos.k1.ab12c", ".secret-files")
	wantFile := filepath.Join(wantDir, "server.crt")
	if res.Path != wantFile {
		t.Errorf("Path = %q, want %q", res.Path, wantFile)
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(wantDir)
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir perm = %o, want 0700", perm)
		}
		fileInfo, err := os.Stat(res.Path)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if perm := fileInfo.Mode().Perm(); perm != 0o600 {
			t.Errorf("file perm = %o, want 0600", perm)
		}
	}

	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch\ngot:  %q\nwant: %q", got, content)
	}
}

func TestSaveSecretFile_TightensModeOnExistingLooseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits behave differently on Windows")
	}
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")

	// Pre-seed the secret file at 0644 to mimic something an older tool
	// or a sloppy umask left behind. SaveSecretFile must end at 0600
	// regardless of whether the content actually changed.
	preDir := filepath.Join(dir, "runos.k1.ab12c", ".secret-files")
	if err := os.MkdirAll(preDir, 0700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	prePath := filepath.Join(preDir, "server.crt")
	content := []byte("cert-bytes")
	if err := os.WriteFile(prePath, content, 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if _, err := SaveSecretFile(dir, base, "server.crt", content); err != nil {
		t.Fatalf("SaveSecretFile (in-sync path): %v", err)
	}
	info, err := os.Stat(prePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("after in-sync save: file perm = %o, want 0600", perm)
	}

	// And again with new content (the write path), starting from 0644.
	if err := os.Chmod(prePath, 0644); err != nil {
		t.Fatalf("relax mode: %v", err)
	}
	if _, err := SaveSecretFile(dir, base, "server.crt", []byte("new-bytes")); err != nil {
		t.Fatalf("SaveSecretFile (overwrite path): %v", err)
	}
	info, err = os.Stat(prePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("after overwrite: file perm = %o, want 0600", perm)
	}
}

func TestSaveSecretFile_OverwritesWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")

	if _, err := SaveSecretFile(dir, base, "server.crt", []byte("v1")); err != nil {
		t.Fatalf("first save: %v", err)
	}

	res, err := SaveSecretFile(dir, base, "server.crt", []byte("v2"))
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if res.InSync {
		t.Error("second save should not report InSync when content differs")
	}

	// Only one file in the secret-files dir (no backup file retained).
	entries, _ := os.ReadDir(filepath.Join(dir, "runos.k1.ab12c", ".secret-files"))
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 file, got %d: %v", len(entries), names)
	}
	current, _ := os.ReadFile(res.Path)
	if string(current) != "v2" {
		t.Errorf("current carrying wrong content: %q", current)
	}
}

func TestSaveSecretFile_RejectsPathTraversalFilenames(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")

	_, err := SaveSecretFile(dir, base, "../outside", []byte("x"))
	if err == nil {
		t.Fatal("expected error for path-traversal filename")
	}
	if !strings.Contains(err.Error(), "path separator") {
		t.Errorf("error should mention path separator, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FindPulledYAMLs (auto-detect)
// ---------------------------------------------------------------------------

func TestFindPulledYAMLs_UniqueMatch(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	body := []byte(`app: x
deployType: cli
id: appid5
cid: mycluster3
aid: myacct
replicas: 1
`)
	if err := os.WriteFile(yamlPath, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindPulledYAMLs(dir)
	if err != nil {
		t.Fatalf("FindPulledYAMLs: %v", err)
	}
	if len(got.Valid) != 1 || got.Valid[0] != yamlPath {
		t.Errorf("Valid = %v, want [%q]", got.Valid, yamlPath)
	}
	if len(got.Partial) != 0 {
		t.Errorf("Partial should be empty, got %v", got.Partial)
	}
}

func TestFindPulledYAMLs_OldLayoutFilenameMatches(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.mycluster3.appid5.yaml")
	body := []byte(`app: x
deployType: cli
id: appid5
cid: mycluster3
aid: myacct
replicas: 1
`)
	if err := os.WriteFile(yamlPath, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindPulledYAMLs(dir)
	if err != nil {
		t.Fatalf("FindPulledYAMLs: %v", err)
	}
	if len(got.Valid) != 1 || got.Valid[0] != yamlPath {
		t.Errorf("Valid = %v, want [%q]", got.Valid, yamlPath)
	}
}

func TestFindPulledYAMLs_MultipleCandidates(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`app: x
deployType: cli
id: appid5
cid: mycluster3
aid: myacct
replicas: 1
`)
	for _, name := range []string{"runos.yaml", "runos.prod.yaml", "myrunos.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got, err := FindPulledYAMLs(dir)
	if err != nil {
		t.Fatalf("FindPulledYAMLs: %v", err)
	}
	if len(got.Valid) != 3 {
		t.Errorf("expected 3 valid candidates, got %d: %v", len(got.Valid), got.Valid)
	}
}

func TestFindPulledYAMLs_IgnoresFilesWithoutRunos(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`id: x
cid: mycluster3
aid: myacct
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindPulledYAMLs(dir)
	if err != nil {
		t.Fatalf("FindPulledYAMLs: %v", err)
	}
	if len(got.Valid)+len(got.Partial) != 0 {
		t.Errorf("filename without 'runos' should not match: %+v", got)
	}
}

func TestFindPulledYAMLs_PartialBucketCapturesYamlWithoutRequiredFields(t *testing.T) {
	dir := t.TempDir()
	// Pre-deploy yaml that matches the filename pattern but lacks id/cid/aid.
	// Should land in Partial so the caller can give a "looks like a fresh
	// deploy yaml, run pull first" hint instead of "no yaml found".
	partialPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(partialPath, []byte(`app: my-app
port: 3000
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindPulledYAMLs(dir)
	if err != nil {
		t.Fatalf("FindPulledYAMLs: %v", err)
	}
	if len(got.Valid) != 0 {
		t.Errorf("yaml without id/cid/aid should not be in Valid: %v", got.Valid)
	}
	if len(got.Partial) != 1 || got.Partial[0] != partialPath {
		t.Errorf("Partial = %v, want [%q]", got.Partial, partialPath)
	}
}

func TestFindPulledYAMLs_IgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "runos.mycluster3.appid5")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Yaml inside the subdir; FindPulledYAMLs should not recurse.
	if err := os.WriteFile(filepath.Join(subdir, "runos.yaml"), []byte(`id: x
cid: mycluster3
aid: myacct
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindPulledYAMLs(dir)
	if err != nil {
		t.Fatalf("FindPulledYAMLs: %v", err)
	}
	if len(got.Valid)+len(got.Partial) != 0 {
		t.Errorf("subdirectory yaml should be ignored: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// SaveX with empty base (flat layout)
// ---------------------------------------------------------------------------

func TestSaveYAML_EmptyBaseWritesIntoParentDirectly(t *testing.T) {
	dir := t.TempDir()
	app := &PulledApp{App: "x", DeployType: "cli", ID: "appid5", CID: "mycluster3", AID: "myacct", Replicas: 1}

	res, err := SaveYAML(dir, "", app)
	if err != nil {
		t.Fatalf("SaveYAML: %v", err)
	}
	want := filepath.Join(dir, "runos.yaml")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("yaml not written: %v", err)
	}
}

func TestSaveSecretFile_EmptyBaseWritesIntoParentDotDir(t *testing.T) {
	dir := t.TempDir()
	res, err := SaveSecretFile(dir, "", "tls.key", []byte("secret"))
	if err != nil {
		t.Fatalf("SaveSecretFile: %v", err)
	}
	want := filepath.Join(dir, ".secret-files", "tls.key")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
}

func TestSaveOverride_EmptyBaseWritesIntoParentOverridesDir(t *testing.T) {
	dir := t.TempDir()
	res, err := SaveOverride(dir, "", "pod-security.yaml", []byte("spec: {}\n"))
	if err != nil {
		t.Fatalf("SaveOverride: %v", err)
	}
	want := filepath.Join(dir, "overrides", "pod-security.yaml")
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
}

func TestSaveEnv_OverwritesWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	base := DefaultBaseName("k1", "ab12c")
	if _, err := SaveEnv(dir, base, "k1", "ab12c", map[string]string{"A": "1"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	res, err := SaveEnv(dir, base, "k1", "ab12c", map[string]string{"A": "2"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.InSync {
		t.Error("second save should not report InSync when content differs")
	}
	// Only one env file lives in the dir (no backup file retained).
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 env file, got %d: %v", len(entries), names)
	}
	current, _ := os.ReadFile(res.Path)
	if !strings.Contains(string(current), "A=2") {
		t.Errorf("current file missing updated content: %q", current)
	}
}

// Regression test for V2 (VCS_DEPLOY_TEST_NOTES.md): when apps_pull writes
// a VCS app's local yaml to a path that differs from the server-stored
// `configPath`, the next VCS deploy will fail because the cluster agent
// fetches the yaml from the OLD path on the committed tree. The warning
// nudges the user to call `apps_update --configPath <new>` after committing
// the new layout.
//
// The helper is pure (string compare); the caller pre-computes the
// repo-relative local path so the helper doesn't need to spawn git.
func TestConfigPathMismatchWarning(t *testing.T) {
	cases := []struct {
		name             string
		serverConfigPath string
		localRepoRelPath string
		deployType       string
		wantEmpty        bool
		wantContains     []string
	}{
		{
			name:             "matching paths produce no warning",
			serverConfigPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			wantEmpty:        true,
		},
		{
			name:             "CLI deploys never warn (configPath is VCS-only)",
			serverConfigPath: "anything",
			localRepoRelPath: "different",
			deployType:       "cli",
			wantEmpty:        true,
		},
		{
			name:             "empty server configPath means no value yet, no warning",
			serverConfigPath: "",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			wantEmpty:        true,
		},
		{
			name:             "empty local path means caller couldn't compute it, no warning",
			serverConfigPath: "infra/runos/apps/something.yaml",
			localRepoRelPath: "",
			deployType:       "vcs",
			wantEmpty:        true,
		},
		{
			name:             "VCS app with mismatched paths warns",
			serverConfigPath: "infra/runos/apps/aliens-dev.mycluster2.appid9.yaml",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			wantEmpty:        false,
			wantContains: []string{
				"infra/runos/apps/aliens-dev.mycluster2.appid9.yaml",
				"infra/runos/apps/runos.mycluster2.appid9.yaml",
				"apps_update",
				"configPath",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConfigPathMismatchWarning(tc.serverConfigPath, tc.localRepoRelPath, tc.deployType)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("expected empty warning, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("expected non-empty warning")
			}
			for _, s := range tc.wantContains {
				if !strings.Contains(got, s) {
					t.Errorf("warning missing %q; got: %s", s, got)
				}
			}
		})
	}
}

// Regression test for V17 (VCS_DEPLOY_TEST_NOTES.md): vcsRepoRelPath
// silently returned "" via MCP when the CLI subprocess CWD was outside
// the pull target's repo, because git resolution was anchored at CWD
// instead of the yaml's directory. The fix moved git resolution to
// `git -C <yamlDir>` and split the path math into this pure helper so
// it can be table-tested without a git fixture. The git-resolution half
// is exercised manually per VCS_DEPLOY_TEST_NOTES.md verification step 6.
func TestRepoRelPath(t *testing.T) {
	t.Parallel()
	sep := string(filepath.Separator)
	cases := []struct {
		name     string
		repoRoot string
		absPath  string
		want     string
	}{
		{
			name:     "child path inside repo",
			repoRoot: filepath.Join(sep, "repo"),
			absPath:  filepath.Join(sep, "repo", "apps", "runos.mycluster2.qu5db.yaml"),
			want:     "apps/runos.mycluster2.qu5db.yaml",
		},
		{
			name:     "yaml at repo root",
			repoRoot: filepath.Join(sep, "repo"),
			absPath:  filepath.Join(sep, "repo", "runos.yaml"),
			want:     "runos.yaml",
		},
		{
			name:     "path escapes repo root",
			repoRoot: filepath.Join(sep, "repo"),
			absPath:  filepath.Join(sep, "elsewhere", "runos.yaml"),
			want:     "",
		},
		{
			name:     "exact repo root rejected (no meaningful file path)",
			repoRoot: filepath.Join(sep, "repo"),
			absPath:  filepath.Join(sep, "repo"),
			want:     "",
		},
		{
			name:     "empty repoRoot returns empty",
			repoRoot: "",
			absPath:  filepath.Join(sep, "repo", "foo"),
			want:     "",
		},
		{
			name:     "empty absPath returns empty",
			repoRoot: filepath.Join(sep, "repo"),
			absPath:  "",
			want:     "",
		},
		{
			name:     "sibling directory with shared prefix is rejected (escapes via parent)",
			repoRoot: filepath.Join(sep, "repo"),
			absPath:  filepath.Join(sep, "repository", "foo"),
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RepoRelPath(tc.repoRoot, tc.absPath)
			if got != tc.want {
				t.Errorf("RepoRelPath(%q, %q) = %q, want %q",
					tc.repoRoot, tc.absPath, got, tc.want)
			}
		})
	}
}

// Regression test for V19 (VCS_DEPLOY_TEST_NOTES.md): the V14/V17
// auto-update hook used to PATCH `{configPath}` alone, which conductor's
// V13/V16 containment validator rejected when the new configPath dir
// would invalidate the stored sourceDir's `..`-traversal. RelocateSourceDir
// pre-computes a sourceDir relative to the new dir that preserves the
// absolute repo target, so a single PATCH carries a coherent trio.
//
// Pure-function table test exercises the full surface: trigger case,
// no-op cases (same-dir, default sourceDir, in-repo merge), legacy bad
// state (existing pair already escapes), and edge inputs.
func TestRelocateSourceDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		oldConfigPath   string
		newConfigPath   string
		storedSourceDir string
		want            string
	}{
		{
			name:            "V18 trigger: deep-to-shallow relocation with traversing sourceDir",
			oldConfigPath:   "infra/runos/apps/runos.mycluster2.qu5db.yaml",
			newConfigPath:   "tmp_v18_test/runos.mycluster2.qu5db.yaml",
			storedSourceDir: "../../../apps/frontend",
			want:            "../apps/frontend",
		},
		{
			name:            "shallow move where sourceDir traversal exactly hits root",
			oldConfigPath:   "a/b/c/runos.yaml",
			newConfigPath:   "x/runos.yaml",
			storedSourceDir: "../../../target",
			want:            "../target",
		},
		{
			name:            "same configPath dir (rename only) skips recompute",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "infra/runos/apps/runos.mycluster2.qu5db.yaml",
			storedSourceDir: "../../../apps/frontend",
			want:            "",
		},
		{
			name:            "same-depth move keeps merge inside repo, skips recompute",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "infra/runos/apps_alt/runos.yaml",
			storedSourceDir: "../../../apps/frontend",
			want:            "",
		},
		{
			name:            "sourceDir is . (default) skips recompute",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "tmp/runos.yaml",
			storedSourceDir: ".",
			want:            "",
		},
		{
			name:            "empty stored sourceDir returns empty",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "tmp/runos.yaml",
			storedSourceDir: "",
			want:            "",
		},
		{
			name:            "in-repo subdir sourceDir without traversal stays put",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "tmp/runos.yaml",
			storedSourceDir: "frontend",
			want:            "",
		},
		{
			name:            "legacy bad state (existing pair already escapes) returns empty so caller falls back",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "tmp/runos.yaml",
			storedSourceDir: "../../../../etc",
			want:            "",
		},
		{
			name:            "empty oldConfigPath returns empty",
			oldConfigPath:   "",
			newConfigPath:   "tmp/runos.yaml",
			storedSourceDir: "../foo",
			want:            "",
		},
		{
			name:            "empty newConfigPath returns empty",
			oldConfigPath:   "infra/runos/apps/runos.yaml",
			newConfigPath:   "",
			storedSourceDir: "../foo",
			want:            "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RelocateSourceDir(tc.oldConfigPath, tc.newConfigPath, tc.storedSourceDir)
			if got != tc.want {
				t.Errorf("RelocateSourceDir(%q, %q, %q) = %q, want %q",
					tc.oldConfigPath, tc.newConfigPath, tc.storedSourceDir, got, tc.want)
			}
		})
	}
}

// Regression test for V14 / long-term V2 closure: apps_pull's reaction to
// a server-vs-local configPath divergence should be a tri-state decision
// (Skip, Update via PATCH, fall back to a Warn) rather than the older
// always-Warn shape. This pure helper isolates the decision so the
// pullOne dispatch stays a thin switch over its return.
func TestDecideConfigPathAction(t *testing.T) {
	cases := []struct {
		name             string
		serverConfigPath string
		localRepoRelPath string
		deployType       string
		noUpdate         bool
		want             ConfigPathAction
	}{
		{
			name:             "non-VCS app skips",
			serverConfigPath: "anything",
			localRepoRelPath: "different",
			deployType:       "cli",
			want:             ConfigPathActionSkip,
		},
		{
			name:             "VCS, paths match, skips",
			serverConfigPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			want:             ConfigPathActionSkip,
		},
		{
			name:             "VCS, empty server configPath, skips (server has no value yet)",
			serverConfigPath: "",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			want:             ConfigPathActionSkip,
		},
		{
			name:             "VCS, empty local repo-relative path, skips (caller can't compare)",
			serverConfigPath: "infra/runos/apps/old.yaml",
			localRepoRelPath: "",
			deployType:       "vcs",
			want:             ConfigPathActionSkip,
		},
		{
			name:             "VCS, paths diverge, default -> auto-update",
			serverConfigPath: "infra/runos/apps/aliens-dev.mycluster2.appid9.yaml",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			want:             ConfigPathActionUpdate,
		},
		{
			name:             "VCS, paths diverge, --no-configpath-update -> warn",
			serverConfigPath: "infra/runos/apps/aliens-dev.mycluster2.appid9.yaml",
			localRepoRelPath: "infra/runos/apps/runos.mycluster2.appid9.yaml",
			deployType:       "vcs",
			noUpdate:         true,
			want:             ConfigPathActionWarn,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideConfigPathAction(tc.serverConfigPath, tc.localRepoRelPath, tc.deployType, tc.noUpdate)
			if got != tc.want {
				t.Errorf("DecideConfigPathAction(%q, %q, %q, noUpdate=%v) = %v, want %v",
					tc.serverConfigPath, tc.localRepoRelPath, tc.deployType, tc.noUpdate, got, tc.want)
			}
		})
	}
}

// Regression test for V1 (VCS_DEPLOY_TEST_NOTES.md): SaveYAMLSuffixed must
// always write to the per-app suffixed leaf, regardless of what's already
// in the directory. Used by the parallel-pull mode (id-flat in
// cmd/apps_pull.go) where multiple concurrent apps_pull invocations
// against a shared --out dir would otherwise race for the canonical
// runos.yaml slot. With this function, every concurrent caller writes
// its own deterministically-named file and there's no race to lose.
func TestSaveYAMLSuffixed_AlwaysWritesSuffixed(t *testing.T) {
	dir := t.TempDir()
	app := &PulledApp{
		App:                 "first",
		ID:                  "ab12c",
		CID:                 "k1",
		AID:                 "acc-1",
		Replicas:            1,
		ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}},
	}
	res, err := SaveYAMLSuffixed(dir, "", app)
	if err != nil {
		t.Fatalf("SaveYAMLSuffixed: %v", err)
	}
	wantLeaf := SuffixedYAMLFilename(app.CID, app.ID)
	if filepath.Base(res.Path) != wantLeaf {
		t.Errorf("res.Path leaf = %q, want %q", filepath.Base(res.Path), wantLeaf)
	}
	if _, err := os.Stat(filepath.Join(dir, "runos.yaml")); !os.IsNotExist(err) {
		t.Errorf("runos.yaml should NOT exist; SaveYAMLSuffixed must never use the canonical slot")
	}
}

// V1 race repro: N goroutines call SaveYAMLSuffixed for N different
// (cid, appID) pairs into the same target dir. Pre-fix (when SaveYAML
// is used in this scenario via cmd/apps_pull's id-flat mode), one of
// them would non-deterministically land at runos.yaml; the rest would
// fall back to the suffixed name when YAMLFilename saw the
// just-written runos.yaml. Post-fix, every call writes its own
// suffixed file and there is no canonical-name race.
func TestSaveYAMLSuffixed_ConcurrentWritesDoNotRace(t *testing.T) {
	dir := t.TempDir()
	apps := []*PulledApp{
		{App: "a", ID: "aaaaa", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}}},
		{App: "b", ID: "bbbbb", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}}},
		{App: "c", ID: "ccccc", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}}},
		{App: "d", ID: "ddddd", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}}},
		{App: "e", ID: "eeeee", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}}},
		{App: "f", ID: "fffff", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 3000, StandardHttps: true}}},
	}

	errs := make([]error, len(apps))
	var wg sync.WaitGroup
	wg.Add(len(apps))
	for i, app := range apps {
		go func(i int, app *PulledApp) {
			defer wg.Done()
			_, errs[i] = SaveYAMLSuffixed(dir, "", app)
		}(i, app)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: SaveYAMLSuffixed: %v", i, err)
		}
	}

	// Every app should land at its own suffixed name; no canonical slot.
	for _, app := range apps {
		wantPath := filepath.Join(dir, SuffixedYAMLFilename(app.CID, app.ID))
		if _, err := os.Stat(wantPath); err != nil {
			t.Errorf("expected file %s to exist after concurrent write: %v", wantPath, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "runos.yaml")); !os.IsNotExist(err) {
		t.Errorf("runos.yaml MUST NOT appear from concurrent SaveYAMLSuffixed calls; the V1 race fix breaks if any caller lands on the canonical slot")
	}
}

// I4-L regression: ResolveLocalEnvPath must prefer the user-authored
// path when set and fall back to the canonical default otherwise.
// Pinned per shape so a future caller can't accidentally drop the
// authored field and silently fan out to two env files on disk.
func TestResolveLocalEnvPath(t *testing.T) {
	appDir := "/tmp/runos.k1.ab12c"
	cases := []struct {
		name         string
		authored     string
		canonical    string
		wantLeaf     string
		wantFullPath string
	}{
		{
			name:         "user-authored relative leaf wins",
			authored:     ".secret.env",
			canonical:    ".runos.k1.ab12c.env",
			wantLeaf:     ".secret.env",
			wantFullPath: filepath.Join(appDir, ".secret.env"),
		},
		{
			name:         "user-authored relative path with subdir wins",
			authored:     "config/.env",
			canonical:    ".runos.k1.ab12c.env",
			wantLeaf:     "config/.env",
			wantFullPath: filepath.Join(appDir, "config/.env"),
		},
		{
			name:         "empty authored falls back to canonical",
			authored:     "",
			canonical:    ".runos.k1.ab12c.env",
			wantLeaf:     ".runos.k1.ab12c.env",
			wantFullPath: filepath.Join(appDir, ".runos.k1.ab12c.env"),
		},
		{
			name:         "absolute authored path returned verbatim",
			authored:     "/etc/runos/secret.env",
			canonical:    ".runos.k1.ab12c.env",
			wantLeaf:     "/etc/runos/secret.env",
			wantFullPath: "/etc/runos/secret.env",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			leaf, fullPath := ResolveLocalEnvPath(appDir, c.authored, c.canonical)
			if leaf != c.wantLeaf {
				t.Errorf("leaf = %q, want %q", leaf, c.wantLeaf)
			}
			if fullPath != c.wantFullPath {
				t.Errorf("fullPath = %q, want %q", fullPath, c.wantFullPath)
			}
		})
	}
}

// I4-L regression: SaveSecretEnvAtPath must write to the exact path
// the caller resolves (typically `serverState.SecretEnv` after the
// MergeUserEnvPaths pass). Pre-fix, apps_pull always wrote to
// SecretEnvFilename(cid, appID) so a yaml with `secretEnv: .secret.env`
// ended up with TWO files: the user-authored one (referenced by the
// yaml) and an orphan canonical-name twin (holding the latest server
// state). This test pins the path-honouring write.
func TestSaveSecretEnvAtPath_RespectsAuthoredLeaf(t *testing.T) {
	dir := t.TempDir()
	authoredPath := filepath.Join(dir, ".secret.env")
	envs := map[string]string{"USER_TOKEN": "abc"}

	res, err := SaveSecretEnvAtPath(authoredPath, envs)
	if err != nil {
		t.Fatalf("SaveSecretEnvAtPath: %v", err)
	}
	if res.Path != authoredPath {
		t.Errorf("Path = %q, want %q", res.Path, authoredPath)
	}
	// File must exist at the authored path with 0600 perms.
	info, err := os.Stat(authoredPath)
	if err != nil {
		t.Fatalf("stat authored path: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm = %o, want 0600", perm)
		}
	}
	// No canonical-name file should appear; the user-authored leaf
	// is the only file. (I4-L's symptom was an orphan canonical
	// twin; this assertion is the regression guard.)
	canonical := filepath.Join(dir, ".runos.k1.ab12c.env")
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("canonical-name twin must not appear at %q", canonical)
	}
}

// I4-L plain side: same path-honouring assertion, with 0644 perms
// (committed to VCS).
func TestSaveEnvAtPath_RespectsAuthoredLeaf(t *testing.T) {
	dir := t.TempDir()
	authoredPath := filepath.Join(dir, "plain.env")
	envs := map[string]string{"LOG_LEVEL": "info"}

	res, err := SaveEnvAtPath(authoredPath, envs)
	if err != nil {
		t.Fatalf("SaveEnvAtPath: %v", err)
	}
	if res.Path != authoredPath {
		t.Errorf("Path = %q, want %q", res.Path, authoredPath)
	}
	info, err := os.Stat(authoredPath)
	if err != nil {
		t.Fatalf("stat authored path: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("perm = %o, want 0644", perm)
		}
	}
	canonical := filepath.Join(dir, "runos.k1.ab12c.config.env")
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("canonical-name twin must not appear at %q", canonical)
	}
}

// I4-L: empty path is rejected (defensive — caller should always
// resolve via ResolveLocalEnvPath which always returns a non-empty
// leaf, but if a future caller skips that step we'd rather fail loud
// than silently write to "./" or some other surprising location).
func TestSaveEnvAtPath_EmptyPathRejected(t *testing.T) {
	if _, err := SaveSecretEnvAtPath("", map[string]string{"A": "1"}); err == nil {
		t.Error("expected error on empty secret path")
	}
	if _, err := SaveEnvAtPath("", map[string]string{"A": "1"}); err == nil {
		t.Error("expected error on empty env path")
	}
}
