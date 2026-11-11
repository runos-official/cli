package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateEnvironmentCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environments.json")
	if err := os.WriteFile(path, []byte(`{"default":"dev","environments":{"dev":{"domains":{"api":"https://api.dev.example","console":"https://console.dev.example"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNOS_ENVIRONMENTS_FILE", path)
	rc, err := FetchRemoteConfig()
	if err != nil {
		t.Fatal(err)
	}
	if rc.Environments["dev"].Domains.APIURL() != "https://api.dev.example" {
		t.Fatal("private dev environment missing")
	}
}
