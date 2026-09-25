package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FCR 732: the server holds standardHttps: false and the yaml omits it.
// Conductor reads the omission as the default (true), so a deploy would
// turn standard HTTPS back on. The gate must refuse, as it does for other
// omit-resets fields.
func TestDeployGate_OmittedStandardHttpsOverServerFalseRefuses(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	local := "app: web\ndeployType: cli\nid: ab12c\ncid: k1\naid: acc-1\nresourceRequirementClassId: app.sl1.small\nservicePortMappings:\n    - port: 3000\n"
	if err := os.WriteFile(yamlPath, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{
		"id": "ab12c", "name": "web", "deployType": "cli", "replicas": float64(1),
		"resourceRequirementClassId": "app.sl1.small",
		"servicePortMappings":        []any{map[string]any{"port": float64(3000), "standardHttps": false}},
	}
	srv := fakeConductorForDiff(t, raw, nil, nil, nil)
	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	localApp, err := LoadLocalApp(yamlPath)
	if err != nil {
		t.Fatalf("LoadLocalApp: %v", err)
	}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if !report.NeedsForceToDeploy() {
		t.Fatalf("deploy gate must refuse; serverOnly=%v", report.YAML.ServerOnlyFields)
	}
	clear, _ := PartitionServerOnlyByClearSemantics(report.YAML.ServerOnlyFields)
	found := false
	for _, f := range clear {
		if strings.HasPrefix(f, "servicePortMappings[0].standardHttps") {
			found = true
		}
	}
	if !found {
		t.Errorf("standardHttps must be listed as reset by the deploy; clearOnOmit=%v", clear)
	}
}

func TestStandardHttpsResetHint(t *testing.T) {
	if got := StandardHttpsResetHint([]string{"servicePortMappings[1].standardHttps (false)"}); !strings.Contains(got, "standardHttps: false") {
		t.Errorf("expected the set-it-explicitly remedy, got %q", got)
	}
	if got := StandardHttpsResetHint([]string{"healthCheck (http)", "servicePortMappings[0].domains (1 entry)"}); got != "" {
		t.Errorf("expected no hint, got %q", got)
	}
	if isStandardHttpsPath("standardHttps (false)") {
		t.Error("top-level legacy shorthand is not a port path")
	}
}
