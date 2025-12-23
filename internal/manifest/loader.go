package manifest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	manifestFileName = "manifest.yaml"
	versionEndpoint  = "/api/public/manifest-version"
	manifestEndpoint = "/api/public/manifest"
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

// Load loads the manifest, checking for updates if possible
func (l *Loader) Load() (*Manifest, error) {
	localManifest, localErr := l.loadLocal()

	// Try to check for updates
	remoteVersion, err := l.fetchVersion()
	if err != nil {
		// Network error - use local if available
		if localErr == nil {
			return localManifest, nil
		}
		return nil, fmt.Errorf("no manifest available: %w", localErr)
	}

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

func (l *Loader) fetchVersion() (string, error) {
	url := l.baseURL + versionEndpoint

	resp, err := l.httpClient.Get(url)
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
	url := l.baseURL + manifestEndpoint

	resp, err := l.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var m Manifest
	if err := yaml.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}

	return &m, nil
}
