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
	manifestFileName     = "manifest.json"
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

// NewLoader creates a new manifest loader
func NewLoader(baseURL, configDir string) *Loader {
	return &Loader{
		baseURL:   baseURL,
		configDir: configDir,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Load loads the manifest, checking for updates if cache has expired
func (l *Loader) Load() (*Manifest, error) {
	localManifest, localErr := l.loadLocal()
	cacheManager := cache.NewManager(l.configDir)

	// Check if we should skip version check (cache still valid)
	if localErr == nil && !cacheManager.IsExpired(versionCheckCacheKey) {
		return localManifest, nil
	}

	// Try to check for updates
	remoteVersion, err := l.fetchVersion()
	if err != nil {
		// Network error - use local if available
		if localErr == nil {
			return localManifest, nil
		}
		// I25-D: fresh install (no local manifest) AND version-check
		// failed. Don't give up here — the version endpoint can fail
		// for reasons unrelated to the manifest endpoint (older
		// conductor without /cli/manifest-version, intermittent gateway
		// errors). Try fetching the full manifest directly before
		// declaring nothing is available.
		rawJSON, fetchErr := l.fetchManifestRaw()
		if fetchErr != nil {
			// Wrap both errors (Go 1.20+ multi-%w) so callers can
			// errors.Is the fetch failure (notably auth.ErrNotAuthenticated
			// on a fresh, pre-login install) and keep first-run output
			// friendly instead of treating it as a hard manifest fault.
			return nil, fmt.Errorf("no manifest available: %w (manifest fetch also failed: %w)", localErr, fetchErr)
		}
		if err := l.saveLocalRaw(rawJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to cache manifest: %v\n", err)
		}
		return parseManifest(rawJSON)
	}

	// Update cache timestamp for version check
	_ = cacheManager.Set(versionCheckCacheKey, remoteVersion, versionCheckTTL)

	// Check if we need to update
	if localErr == nil && localManifest.Version == remoteVersion {
		return localManifest, nil
	}

	// Fetch new manifest (raw JSON bytes)
	rawJSON, err := l.fetchManifestRaw()
	if err != nil {
		if localErr == nil {
			return localManifest, nil
		}
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}

	// Save raw JSON locally
	if err := l.saveLocalRaw(rawJSON); err != nil {
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
	rawJSON, err := l.fetchManifestRaw()
	if err != nil {
		return nil, err
	}
	if err := l.saveLocalRaw(rawJSON); err != nil {
		return nil, err
	}
	return parseManifest(rawJSON)
}

func (l *Loader) loadLocal() (*Manifest, error) {
	path := filepath.Join(l.configDir, manifestFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	return parseManifest(data)
}

func (l *Loader) saveLocalRaw(data []byte) error {
	if err := os.MkdirAll(l.configDir, 0700); err != nil {
		return err
	}

	path := filepath.Join(l.configDir, manifestFileName)
	return os.WriteFile(path, data, 0600)
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

func (l *Loader) getAuthToken() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return auth.ResolveToken(cfg)
}

func (l *Loader) fetchVersion() (string, error) {
	token, err := l.getAuthToken()
	if err != nil {
		return "", err
	}

	url := l.baseURL + versionEndpoint

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := l.httpClient.Do(req)
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

func (l *Loader) fetchManifestRaw() ([]byte, error) {
	token, err := l.getAuthToken()
	if err != nil {
		return nil, err
	}

	url := l.baseURL + manifestEndpoint

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
}
