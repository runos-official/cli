package manifest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cli/internal/auth"
	"cli/internal/cache"
	"cli/internal/config"

	"gopkg.in/yaml.v3"
)

const (
	manifestFileName       = "manifest.yaml"
	versionEndpoint        = "/cli/manifest-version"
	manifestEndpoint       = "/cli/manifest"
	versionCheckCacheKey   = "manifest_version_check"
	versionCheckTTL        = 1 * time.Hour
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
		return nil, fmt.Errorf("no manifest available: %w", localErr)
	}

	// Update cache timestamp for version check
	_ = cacheManager.Set(versionCheckCacheKey, remoteVersion, versionCheckTTL)

	// Check if we need to update
	if localErr == nil && localManifest.Version == remoteVersion {
		return localManifest, nil
	}

	// Fetch new manifest
	newManifest, err := l.fetchManifest()
	if err != nil {
		if localErr == nil {
			return localManifest, nil
		}
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}

	// Save locally
	if err := l.saveLocal(newManifest); err != nil {
		// Log warning but continue with fetched manifest
		fmt.Fprintf(os.Stderr, "Warning: failed to cache manifest: %v\n", err)
	}

	return newManifest, nil
}

// LoadLocal loads only the local manifest without checking for updates
func (l *Loader) LoadLocal() (*Manifest, error) {
	return l.loadLocal()
}

func (l *Loader) loadLocal() (*Manifest, error) {
	path := filepath.Join(l.configDir, manifestFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return &m, nil
}

func (l *Loader) saveLocal(m *Manifest) error {
	if err := os.MkdirAll(l.configDir, 0700); err != nil {
		return err
	}

	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}

	path := filepath.Join(l.configDir, manifestFileName)
	return os.WriteFile(path, data, 0600)
}

type versionResponse struct {
	Version string `json:"version"`
}

func (l *Loader) getAuthToken() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}

	if cfg.RefreshToken == "" || cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}

	refreshResp, err := auth.RefreshIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
	if err != nil {
		return "", err
	}

	return refreshResp.IDToken, nil
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
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}

	return v.Version, nil
}

func (l *Loader) fetchManifest() (*Manifest, error) {
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

	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}

	return &m, nil
}
