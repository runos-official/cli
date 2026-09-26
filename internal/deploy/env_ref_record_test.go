package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// FCR 170: deploy must not write a secretEnv reference to a file it never
// creates. The default secret path still resolves (so a later server merge
// can land there), but runos.yaml records it only once the file exists.
func TestResolveEnvFiles_SecretRefRecordedOnlyWhenFileExists(t *testing.T) {
	t.Run("absent default secret file: path resolved, no reference recorded", func(t *testing.T) {
		dir := t.TempDir()
		config := &DeployConfig{App: "myapp", Port: 8080, ID: "app123"}

		paths, _ := ResolveEnvFiles(dir, config, "cid1")
		if config.SecretEnv != "" {
			t.Errorf("config.SecretEnv = %q, want empty (file absent)", config.SecretEnv)
		}
		if paths.Secret != filepath.Join(dir, DefaultSecretEnvFilename("cid1", "app123")) {
			t.Errorf("paths.Secret = %q, want the default path", paths.Secret)
		}
		if paths.SecretExplicit {
			t.Error("SecretExplicit = true, want false for the auto-derived default")
		}
	})

	t.Run("present default secret file: reference recorded", func(t *testing.T) {
		dir := t.TempDir()
		def := DefaultSecretEnvFilename("cid1", "app123")
		if err := os.WriteFile(filepath.Join(dir, def), []byte("K=v\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		config := &DeployConfig{App: "myapp", Port: 8080, ID: "app123"}

		_, changed := ResolveEnvFiles(dir, config, "cid1")
		if !changed || config.SecretEnv != def {
			t.Errorf("got (changed=%v, SecretEnv=%q), want (true, %q)", changed, config.SecretEnv, def)
		}
	})
}
