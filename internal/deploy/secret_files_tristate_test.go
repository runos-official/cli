package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FCR 792: an explicit `secretFiles: []` means "remove every declared secret file", and an
// omitted key means "keep what the app has". The CLI used to drop the empty list on both the
// wire body and the yaml rewrite (omitempty on a slice), so the two meant the same thing and
// a deliberate removal was silently ignored.
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSecretFilesExplicitEmptyReachesTheWire(t *testing.T) {
	cfg, err := LoadConfig(writeYAML(t, "app: a\nservicePortMappings:\n  - port: 8080\nsecretFiles: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"secretFiles":[]`) {
		t.Fatalf("explicit empty secretFiles must be sent as [], got %s", body)
	}
}

func TestSecretFilesOmittedStaysOmitted(t *testing.T) {
	cfg, err := LoadConfig(writeYAML(t, "app: a\nservicePortMappings:\n  - port: 8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(cfg)
	if strings.Contains(string(body), "secretFiles") {
		t.Fatalf("an omitted secretFiles must stay omitted (keep server state), got %s", body)
	}
}

func TestSecretFilesExplicitEmptySurvivesTheYAMLRewrite(t *testing.T) {
	p := writeYAML(t, "app: a\nservicePortMappings:\n  - port: 8080\nsecretFiles: []\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "secretFiles: []") {
		t.Fatalf("the rewrite must keep the explicit empty list, got:\n%s", data)
	}
	cfg2, _ := LoadConfig(writeYAML(t, "app: a\nservicePortMappings:\n  - port: 8080\n"))
	p2 := filepath.Join(t.TempDir(), "runos.yaml")
	if err := SaveConfig(p2, cfg2); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(p2)
	if strings.Contains(string(data2), "secretFiles") {
		t.Fatalf("the rewrite must not add secretFiles when it was omitted, got:\n%s", data2)
	}
}
