package apps

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// firstDeployFailedYAML writes the yaml a first deploy leaves on disk
// when the upload fails: the ids are written back, but the refresh that
// fills server defaults never runs, so standardHttps stays omitted.
func firstDeployFailedYAML(t *testing.T, dir string) string {
	t.Helper()
	local := &PulledApp{
		App:                        "web",
		DeployType:                 "cli",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		ResourceRequirementClassID: "app.sl1.small",
		ServicePortMappings:        []Port{{Port: 3000}},
	}
	b, err := yaml.Marshal(local)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func serverAppRaw(standardHTTPS bool) map[string]any {
	return map[string]any{
		"id":                         "ab12c",
		"name":                       "web",
		"deployType":                 "cli",
		"replicas":                   float64(1),
		"resourceRequirementClassId": "app.sl1.small",
		"servicePortMappings": []any{
			map[string]any{"port": float64(3000), "standardHttps": standardHTTPS},
		},
	}
}

// FCR 716: a deploy after a failed first deploy must not be blocked by
// the server's default standardHttps: true against a yaml that omits it.
func TestBuildDiffReport_OmittedStandardHttpsMatchesServerDefault(t *testing.T) {
	dir := t.TempDir()
	yamlPath := firstDeployFailedYAML(t, dir)
	srv := fakeConductorForDiff(t, serverAppRaw(true), nil, nil, nil)
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp, err := LoadLocalApp(yamlPath)
	if err != nil {
		t.Fatalf("LoadLocalApp: %v", err)
	}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.HasDrift() {
		t.Errorf("expected no drift; yaml=%s diff:\n%s", report.YAML.Status, report.YAML.UnifiedDiff)
	}
	if report.NeedsForceToDeploy() {
		t.Error("deploy gate must not refuse on the default standardHttps")
	}
}

// A real server-side change away from the default still drifts.
func TestBuildDiffReport_ServerStandardHttpsFalseStillDrifts(t *testing.T) {
	dir := t.TempDir()
	yamlPath := firstDeployFailedYAML(t, dir)
	srv := fakeConductorForDiff(t, serverAppRaw(false), nil, nil, nil)
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp, err := LoadLocalApp(yamlPath)
	if err != nil {
		t.Fatalf("LoadLocalApp: %v", err)
	}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if !report.HasDrift() {
		t.Fatal("expected drift when the server holds standardHttps: false")
	}
}

func TestAlignOmittedPortDefaults(t *testing.T) {
	cases := []struct {
		name   string
		local  []Port
		server []Port
		want   []*bool
	}{
		{name: "omitted local, server true", local: []Port{{Port: 80}}, server: []Port{{Port: 80, StandardHttps: BoolPtr(true)}}, want: []*bool{nil}},
		{name: "omitted local, server false", local: []Port{{Port: 80}}, server: []Port{{Port: 80, StandardHttps: BoolPtr(false)}}, want: []*bool{BoolPtr(false)}},
		{name: "explicit local true", local: []Port{{Port: 80, StandardHttps: BoolPtr(true)}}, server: []Port{{Port: 80, StandardHttps: BoolPtr(true)}}, want: []*bool{BoolPtr(true)}},
		{name: "port only on server", local: []Port{{Port: 80}}, server: []Port{{Port: 81, StandardHttps: BoolPtr(true)}}, want: []*bool{BoolPtr(true)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &PulledApp{ServicePortMappings: tc.server}
			alignOmittedPortDefaults(server, &PulledApp{ServicePortMappings: tc.local})
			for i, w := range tc.want {
				got := server.ServicePortMappings[i].StandardHttps
				if (got == nil) != (w == nil) || (got != nil && *got != *w) {
					t.Errorf("port %d: got %v, want %v", i, got, w)
				}
			}
		})
	}
}
