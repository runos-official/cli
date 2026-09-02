// Package manifest handles loading, caching, and parsing of the CLI command manifest.
package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/cache"
	"github.com/runos-official/cli/internal/config"
)

const (
	manifestFileName = "manifest.json"
	// accountFileName holds the account id the cached manifest was served
	// for. A sidecar rather than a field in manifest.json, because that
	// file is read raw elsewhere (cmd/root.go stats it, cmd/apps_pull.go
	// reads it), and a cache.json entry is wrong because cache entries
	// expire and this fact must not.
	accountFileName      = "manifest.account"
	versionEndpoint      = "/cli/manifest-version"
	manifestEndpoint     = "/cli/manifest"
	versionCheckCacheKey = "manifest_version_check"
	versionCheckTTL      = 1 * time.Hour
)

// Loader handles loading and caching of the manifest
type Loader struct {
	baseURL    string
	configDir  string
	httpClient *http.Client
}

// DefaultTimeout is the deadline for a manifest fetch or version probe.
const DefaultTimeout = 10 * time.Second

// AdvisoryTimeout is the deadline for a probe made only to EXPLAIN a
// failure the user already has, not to do the work they asked for.
//
// The drift check after a 4xx costs one extra round trip on a command
// that has already failed, and at DefaultTimeout an unreachable API added
// ten seconds to every failure, including in a loop. A warm conductor
// answers /cli/manifest-version in well under a second; when it does not,
// the guidance is dropped and the original error stands alone.
const AdvisoryTimeout = 3 * time.Second

// NewLoader creates a new manifest loader
func NewLoader(baseURL, configDir string) *Loader {
	return NewLoaderWithTimeout(baseURL, configDir, DefaultTimeout)
}

// NewLoaderWithTimeout creates a manifest loader whose HTTP calls carry
// the given deadline. Used for advisory probes, see AdvisoryTimeout.
func NewLoaderWithTimeout(baseURL, configDir string, timeout time.Duration) *Loader {
	return &Loader{
		baseURL:   baseURL,
		configDir: configDir,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// THE MANIFEST IS SERVED PER ACCOUNT (FPL31 D11, D12).
//
// Conductor serves `/:aid/cli/manifest` and `/:aid/cli/manifest-version`,
// which carry only the commands of the modules that account has switched
// on. The bare `/cli/manifest` routes stay unfiltered and are what an
// older conductor answers, so each scoped route falls back to its bare
// twin on a 404 and on a 404 ONLY. Any other status is an error, exactly
// as before, because a 500 on the scoped route says nothing about whether
// the bare route exists.
//
// Two accounts therefore have two different command surfaces, so the
// account id is cached beside the manifest. A cached manifest served for
// another account is never handed back without a fetch attempt first, or
// one account would see another account's commands.

// Load loads the manifest, checking for updates if cache has expired
func (l *Loader) Load() (*Manifest, error) {
	localManifest, localErr := l.loadLocal()
	cacheManager := cache.NewManager(l.configDir)

	// An account switch invalidates the cached manifest whatever the TTL
	// says, so both shortcuts below are skipped while the two disagree.
	// A MISSING sidecar counts as a mismatch, so the first run after this
	// upgrade refetches once and writes it.
	mismatch := localErr == nil && !l.accountMatchesConfig()

	// Check if we should skip version check (cache still valid)
	if localErr == nil && !mismatch && !cacheManager.IsExpired(versionCheckCacheKey) {
		return localManifest, nil
	}

	// Try to check for updates
	remoteVersion, err := l.fetchVersion()
	if err != nil {
		// Network error - use local if available.
		//
		// This holds for an account mismatch too: the CLI fails OPEN and
		// conductor refuses what it must (FPL31 D18). Handing back no
		// commands at all because a fetch failed would be worse than a
		// stale list the API will refuse route by route.
		if localErr == nil {
			return localManifest, nil
		}
		// I25-D: fresh install (no local manifest) AND version-check
		// failed. Don't give up here — the version endpoint can fail
		// for reasons unrelated to the manifest endpoint (older
		// conductor without /cli/manifest-version, intermittent gateway
		// errors). Try fetching the full manifest directly before
		// declaring nothing is available.
		rawJSON, accountID, fetchErr := l.fetchManifestRaw()
		if fetchErr != nil {
			// Wrap both errors (Go 1.20+ multi-%w) so callers can
			// errors.Is the fetch failure (notably auth.ErrNotAuthenticated
			// on a fresh, pre-login install) and keep first-run output
			// friendly instead of treating it as a hard manifest fault.
			return nil, fmt.Errorf("no manifest available: %w (manifest fetch also failed: %w)", localErr, fetchErr)
		}
		if err := l.saveLocalRaw(rawJSON, accountID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to cache manifest: %v\n", err)
		}
		return parseManifest(rawJSON)
	}

	// Update cache timestamp for version check
	_ = cacheManager.Set(versionCheckCacheKey, remoteVersion, versionCheckTTL)

	// Check if we need to update. The served version carries the enabled
	// module set as semver build metadata (45.3.0+virt), so a module
	// toggle changes this string and refetches, with nothing here having
	// to parse it.
	if localErr == nil && !mismatch && localManifest.Version == remoteVersion {
		return localManifest, nil
	}

	// Fetch new manifest (raw JSON bytes)
	rawJSON, accountID, err := l.fetchManifestRaw()
	if err != nil {
		if localErr == nil {
			return localManifest, nil
		}
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}

	// Save raw JSON locally
	if err := l.saveLocalRaw(rawJSON, accountID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to cache manifest: %v\n", err)
	}

	// Parse and return
	return parseManifest(rawJSON)
}

// LoadLocal loads only the local manifest without checking for updates
func (l *Loader) LoadLocal() (*Manifest, error) {
	return l.loadLocal()
}

// ServerVersion reports the manifest version the API is currently serving, without
// downloading or caching the manifest itself.
//
// Exists so an "unknown command" can be told apart from "your cached command list is
// stale" (goal 21, O10). That distinction had been guessed wrong eight separate times,
// because the two failures look identical from the outside.
func (l *Loader) ServerVersion() (string, error) {
	return l.fetchVersion()
}

// ForceUpdate fetches and saves the latest manifest, bypassing all caches
func (l *Loader) ForceUpdate() (*Manifest, error) {
	rawJSON, accountID, err := l.fetchManifestRaw()
	if err != nil {
		return nil, err
	}
	if err := l.saveLocalRaw(rawJSON, accountID); err != nil {
		return nil, err
	}
	return parseManifest(rawJSON)
}

// BareManifest fetches the UNFILTERED command list, the one that names
// every command conductor serves rather than the ones this account may
// call. It never writes the cache or the sidecar.
//
// Used to tell "this command does not exist" apart from "this command
// belongs to a module this account has switched off": the first is a
// typo, the second is one `runos account modules enable` away, and the
// two are indistinguishable from the scoped list alone.
func (l *Loader) BareManifest() (*Manifest, error) {
	token, _, err := l.session()
	if err != nil {
		return nil, err
	}
	resp, err := l.do(token, "", manifestEndpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	return parseManifest(data)
}

func (l *Loader) loadLocal() (*Manifest, error) {
	path := filepath.Join(l.configDir, manifestFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	return parseManifest(data)
}

// cachedAccountID reports the account id the cached manifest was served
// for. The bool is false when the sidecar is missing or unreadable, which
// is treated as a mismatch rather than as "no account".
func (l *Loader) cachedAccountID() (string, bool) {
	data, err := os.ReadFile(filepath.Join(l.configDir, accountFileName))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// accountMatchesConfig reports whether the cached manifest was served for
// the account the CLI is pointed at now. A missing sidecar is a mismatch.
// A config that cannot be read is NOT: with no account id to compare
// against, refetching on every call would be a guess.
func (l *Loader) accountMatchesConfig() bool {
	cached, ok := l.cachedAccountID()
	if !ok {
		return false
	}
	_, accountID, err := l.session()
	if err != nil {
		return true
	}
	return cached == accountID
}

func (l *Loader) saveLocalRaw(data []byte, accountID string) error {
	if err := os.MkdirAll(l.configDir, 0700); err != nil {
		return err
	}

	path := filepath.Join(l.configDir, manifestFileName)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	// Written second and unconditionally: an empty account id is a real
	// answer (the bare route served this manifest) and must overwrite a
	// previous account's, not be left behind as a stale claim.
	return os.WriteFile(filepath.Join(l.configDir, accountFileName), []byte(accountID), 0600)
}

func parseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	return &m, nil
}

type versionResponse struct {
	Version string `json:"version"`
}

// session reads the credential and the account id together, so one fetch
// costs one config load and both readers apply the same rule for which
// account id wins (GetAccountID prefers RUNOS_ACCOUNT_ID).
func (l *Loader) session() (token, accountID string, err error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", err
	}
	token, err = auth.ResolveToken(cfg)
	if err != nil {
		return "", "", err
	}
	return token, cfg.GetAccountID(), nil
}

// do issues one GET against baseURL+endpoint with the bearer token.
func (l *Loader) do(token, accountID, endpoint string) (*http.Response, error) {
	url := l.baseURL + endpoint
	if accountID != "" {
		url = l.baseURL + "/" + accountID + endpoint
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return l.httpClient.Do(req)
}

// get requests the account-scoped route and retries the bare route on a
// 404, so an older conductor that never learned the scoped route still
// serves this CLI. With no account id it requests the bare route only and
// never probes the scoped one. Each caller falls back on its own, because
// the two routes ship independently.
func (l *Loader) get(token, accountID, endpoint string) (*http.Response, error) {
	resp, err := l.do(token, accountID, endpoint)
	if err != nil || accountID == "" {
		return resp, err
	}
	if resp.StatusCode != http.StatusNotFound {
		return resp, nil
	}
	resp.Body.Close()
	return l.do(token, "", endpoint)
}

func (l *Loader) fetchVersion() (string, error) {
	token, accountID, err := l.session()
	if err != nil {
		return "", err
	}

	resp, err := l.get(token, accountID, versionEndpoint)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var v versionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&v); err != nil {
		return "", err
	}

	return v.Version, nil
}

// fetchManifestRaw returns the manifest bytes and the account id they
// were served for, so the caller writes a sidecar that matches the file
// it just wrote rather than re-reading the config and racing it.
func (l *Loader) fetchManifestRaw() ([]byte, string, error) {
	token, accountID, err := l.session()
	if err != nil {
		return nil, "", err
	}

	resp, err := l.get(token, accountID, manifestEndpoint)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, "", err
	}
	return data, accountID, nil
}
