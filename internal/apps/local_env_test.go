package apps

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// FCR 170: runos deploy records the canonical default secretEnv reference
// in runos.yaml without creating the file when the app has no secret env.
// apps sync must then read the absent default as "no local content", the
// same rule deploy.ResolveEnvFiles applies, not as a missing explicit file.
func TestLoadLocalEnv_CanonicalDefaultRefAbsentIsNotExplicit(t *testing.T) {
	dir := t.TempDir()
	def := SecretEnvFilename("cid1", "app12")

	got, exists, err := LoadLocalEnv(dir, def, def)
	if err != nil {
		t.Fatalf("LoadLocalEnv(default ref, file absent) = %v, want nil", err)
	}
	if exists {
		t.Error("exists = true, want false for an absent default file")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// A genuinely custom reference that names a missing file keeps the
// fail-loud contract: sync is replace-all, so a typo must not read as empty.
func TestLoadLocalEnv_CustomRefAbsentStaysExplicit(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadLocalEnv(dir, "prod-secret.env", SecretEnvFilename("cid1", "app12"))
	if !errors.Is(err, deploy.ErrMissingExplicitEnvFile) {
		t.Fatalf("err = %v, want ErrMissingExplicitEnvFile", err)
	}
}

// The canonical default reference with the file present still loads it.
func TestLoadLocalEnv_CanonicalDefaultRefPresentLoads(t *testing.T) {
	dir := t.TempDir()
	def := SecretEnvFilename("cid1", "app12")
	if err := os.WriteFile(filepath.Join(dir, def), []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, exists, err := LoadLocalEnv(dir, def, def)
	if err != nil || !exists || got["K"] != "v" {
		t.Fatalf("got (%v, %v, %v), want ({K:v}, true, nil)", got, exists, err)
	}
}
