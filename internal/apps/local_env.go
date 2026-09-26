package apps

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/deploy"
)

// LoadLocalEnv reads the env file referenced by the app yaml's `env:` (or
// `secretEnv:`) field. The path is resolved relative to yamlDir when not
// absolute.
//
// When envRef is empty, falls back to defaultRef (the documented per-app
// default like `runos.<cid>.<id>.config.env`). The caller computes that
// default via EnvFilename / SecretEnvFilename and passes it in. The fallback
// fixes V3 (apps_sync silently skipping a file at the default path because
// the yaml didn't carry an explicit ref), and matches what apps_pull writes
// when it materialises env values from server.
//
// Returns (empty map, false, nil) when neither envRef nor defaultRef is set,
// or when an auto-derived defaultRef file doesn't exist on disk. The caller
// treats exists==false as "no local content for this side" — sync skips the
// push.
//
// When envRef is set explicitly (the yaml named the file) but it's missing,
// returns deploy.ErrMissingExplicitEnvFile rather than empty: apps sync is
// replace-all per source, so silently treating a typo'd `env:` path as empty
// would DELETE every server-side env var for that source. An explicit
// reference must point at a real file — same fail-loud contract as
// LoadLocalSecretFiles / LoadLocalOverrides.
//
// An envRef equal to defaultRef is NOT explicit (FCR 170). runos deploy
// writes the canonical default back into the yaml, and the default secret
// file is legitimately absent when the app has no secret vars. This is the
// same rule deploy.ResolveEnvFiles applies, so deploy and sync agree.
func LoadLocalEnv(yamlDir, envRef, defaultRef string) (map[string]string, bool, error) {
	ref := envRef
	explicit := envRef != "" && filepath.Clean(envRef) != filepath.Clean(defaultRef)
	if ref == "" {
		ref = defaultRef
	}
	if ref == "" {
		return map[string]string{}, false, nil
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(yamlDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if explicit {
				return nil, false, fmt.Errorf("env file %q (referenced in runos.yaml): %w", path, deploy.ErrMissingExplicitEnvFile)
			}
			return map[string]string{}, false, nil
		}
		return nil, false, err
	}
	return parseEnvBytes(data), true, nil
}
