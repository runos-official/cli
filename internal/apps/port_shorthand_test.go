package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadYAML(t *testing.T, body string) *PulledApp {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runos.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := LoadLocalApp(p)
	if err != nil {
		t.Fatalf("LoadLocalApp: %v", err)
	}
	return app
}

var serverOnePort = map[string]any{
	"name":     "web",
	"replicas": float64(1),
	"servicePortMappings": []any{
		map[string]any{"port": float64(8080), "standardHttps": true},
	},
}

// FCR 753: runos deploy reads `port:` / `standardHttps:` as one mapping, but
// apps sync ignored the shorthand and planned servicePortMappings: [] against
// an app whose only port is 8080. The shorthand must load as that mapping.
func TestLoadLocalApp_PortShorthandLoadsAsOneMapping(t *testing.T) {
	cases := []struct {
		name, body string
		wantHTTPS  bool
	}{
		{"port and standardHttps", "app: web\nport: 8080\nstandardHttps: true\n", true},
		{"port alone defaults standardHttps to true", "app: web\nport: 8080\n", true},
		{"standardHttps false", "app: web\nport: 8080\nstandardHttps: false\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := loadYAML(t, c.body)
			if len(app.ServicePortMappings) != 1 || app.ServicePortMappings[0].Port != 8080 ||
				app.ServicePortMappings[0].StandardHTTPSValue() != c.wantHTTPS {
				t.Fatalf("ServicePortMappings = %+v, want [{8080 %v}]", app.ServicePortMappings, c.wantHTTPS)
			}
		})
	}
}

// servicePortMappings wins over the shorthand, as on the deploy path.
func TestLoadLocalApp_MappingsWinOverShorthand(t *testing.T) {
	app := loadYAML(t, "app: web\nport: 9090\nservicePortMappings:\n  - port: 8080\n")
	if len(app.ServicePortMappings) != 1 || app.ServicePortMappings[0].Port != 8080 {
		t.Fatalf("ServicePortMappings = %+v, want [{8080}]", app.ServicePortMappings)
	}
}

func TestComputeYAMLPatch_PortShorthandMatchingServerIsNotDrift(t *testing.T) {
	local := loadYAML(t, "app: web\nreplicas: 1\nport: 8080\nstandardHttps: true\n")
	patch, diff, _, _ := computeYAMLPatch(local, serverOnePort, nil)
	if patch != nil {
		t.Errorf("patch = %+v, want nil (shorthand equals the server mapping)", patch)
	}
	if strings.Contains(diff, "servicePortMappings") {
		t.Errorf("diff plans servicePortMappings:\n%s", diff)
	}
}

// A yaml with no port declaration at all means "preserve", the same as the
// wire body (which omits empty mappings) and the deploy path (which falls
// back to the existing mappings). The plan must never show [] for it.
func TestComputeYAMLPatch_NoPortsDeclaredNeverPlansEmptyList(t *testing.T) {
	local := loadYAML(t, "app: web\nreplicas: 1\n")
	patch, diff, _, _ := computeYAMLPatch(local, serverOnePort, nil)
	if _, ok := patch["servicePortMappings"]; ok {
		t.Errorf("patch carries servicePortMappings: %+v", patch)
	}
	if strings.Contains(diff, "servicePortMappings") {
		t.Errorf("diff plans servicePortMappings:\n%s", diff)
	}
}
