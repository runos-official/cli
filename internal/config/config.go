// Package config manages CLI configuration including environment presets and credentials.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	configDirName  = ".runos"
	configFileName = "config.json"

	// remoteConfigURL is the URL of the remote RunOS configuration file.
	remoteConfigURL = "https://runoscdn.com/configs/current.json"
)

// FirebaseConfig holds Firebase project credentials for authentication.
type FirebaseConfig struct {
	APIKey     string `json:"api_key,omitempty"`
	AuthDomain string `json:"auth_domain,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}

// RemoteDomains holds the domain URLs for a RunOS environment.
type RemoteDomains struct {
	Console   string `json:"console"`
	Conductor string `json:"conductor"`
}

// RemoteEnvironment represents a single environment entry in the remote config.
type RemoteEnvironment struct {
	Domains RemoteDomains `json:"domains"`
}

// RemoteConfig represents the remote configuration file fetched from the CDN.
type RemoteConfig struct {
	Default      string                       `json:"default"`
	Environments map[string]RemoteEnvironment `json:"environments"`
}

// Config represents the persisted CLI configuration stored in ~/.runos/config.json.
type Config struct {
	Env              string          `json:"env,omitempty"`
	ConsoleURL       string          `json:"console_url,omitempty"`
	ConductorURL     string          `json:"conductor_url,omitempty"`
	AccountID        string          `json:"account_id,omitempty"`
	DefaultClusterID string          `json:"default_cluster_id,omitempty"`
	RefreshToken     string          `json:"refresh_token,omitempty"`
	Firebase         *FirebaseConfig `json:"firebase,omitempty"`
	SignedInAt       string          `json:"signed_in_at,omitempty"` // RFC3339 timestamp of login
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName), nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// ErrConfigNotFound is returned when the config file doesn't exist AND
// no env-var fallback is in play. CI runners using RUNOS_API_KEY +
// RUNOS_ACCOUNT_ID + RUNOS_API_URL won't trip this.
var ErrConfigNotFound = fmt.Errorf("config not found - run 'runos config env <environment>' to set up")

// Load reads and parses the config file from disk. When the file is
// missing AND CI-style env vars (RUNOS_API_KEY) are set, returns an
// empty config the caller can populate from env via the Get* helpers
// — useful for headless CI where no on-disk config exists.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// CI fallback: a PAT-based caller doesn't need a local
		// config file or interactive `runos login`. Pull the
		// default environment's URLs from the CDN so the user only
		// needs to set RUNOS_API_KEY (and RUNOS_ACCOUNT_ID).
		//
		// We inline the CDN fetch rather than calling InitFromRemote
		// because that helper writes to disk via applyRemoteEnv,
		// which itself calls Load() to preserve existing auth state.
		// That would recurse straight back into this branch.
		// Returning an in-memory Config keeps the CI runner's
		// filesystem clean and avoids the recursion.
		//
		// CDN failure is non-fatal: return an empty Config and let
		// RUNOS_API_URL env var override (or surface the
		// "URL is empty" error downstream). Don't block the run on
		// a transient CDN hiccup.
		if os.Getenv("RUNOS_API_KEY") != "" {
			cfg := &Config{}
			if rc, rerr := FetchRemoteConfig(); rerr == nil {
				if env, ok := rc.Environments[rc.Default]; ok {
					cfg.Env = rc.Default
					cfg.ConsoleURL = env.Domains.Console
					cfg.ConductorURL = env.Domains.Conductor
				}
			}
			return cfg, nil
		}
		return nil, ErrConfigNotFound
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// GetAccountID returns the account ID, preferring the RUNOS_ACCOUNT_ID
// env var. Used by every API-touching subcommand to find which account
// to address. CI runners using a PAT set this env var so the CLI
// doesn't need a local config with the field stamped in by `runos login`.
func (c *Config) GetAccountID() string {
	if envAID := os.Getenv("RUNOS_ACCOUNT_ID"); envAID != "" {
		return envAID
	}
	if c == nil {
		return ""
	}
	return c.AccountID
}

// Save writes the config to disk, creating the config directory if needed.
func (c *Config) Save() error {
	dir, err := configDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	path, err := configPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// GetConsoleURL returns the console URL, preferring the CONSOLE_URL environment variable.
func (c *Config) GetConsoleURL() string {
	if envURL := os.Getenv("CONSOLE_URL"); envURL != "" {
		return envURL
	}
	return c.ConsoleURL
}

// GetAPIURL returns the RunOS API base URL, preferring the
// RUNOS_API_URL environment variable. The internal ConductorURL
// struct field name and conductor_url JSON key are kept as-is to
// preserve the on-disk config schema; user-facing surfaces use
// "API" / "RUNOS_API_URL" exclusively.
func (c *Config) GetAPIURL() string {
	u := c.ConductorURL
	if envURL := os.Getenv("RUNOS_API_URL"); envURL != "" {
		u = envURL
	}
	if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") {
		fmt.Fprintf(os.Stderr, "Warning: API URL uses non-HTTPS scheme: %s\n", u)
	}
	return u
}

// GetDefaultClusterID returns the default cluster ID, preferring the RUNOS_CLUSTER_ID environment variable.
func (c *Config) GetDefaultClusterID() string {
	if envCID := os.Getenv("RUNOS_CLUSTER_ID"); envCID != "" {
		return envCID
	}
	return c.DefaultClusterID
}

// Exists checks if the config file exists.
func Exists() bool {
	path, err := configPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// FetchRemoteConfig fetches the environment configuration from the CDN.
func FetchRemoteConfig() (*RemoteConfig, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(remoteConfigURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch remote config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch remote config (status %d)", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read remote config: %w", err)
	}

	var rc RemoteConfig
	if err := json.Unmarshal(body, &rc); err != nil {
		return nil, fmt.Errorf("failed to parse remote config: %w", err)
	}

	return &rc, nil
}

// InitFromRemote fetches the remote config and applies the default environment.
func InitFromRemote() (*Config, error) {
	rc, err := FetchRemoteConfig()
	if err != nil {
		return nil, err
	}

	return applyRemoteEnv(rc, rc.Default)
}

// InitFromRemoteEnv fetches the remote config and applies the named environment.
func InitFromRemoteEnv(envName string) (*Config, error) {
	rc, err := FetchRemoteConfig()
	if err != nil {
		return nil, err
	}

	return applyRemoteEnv(rc, envName)
}

// applyRemoteEnv creates or updates the local config from a remote environment entry.
func applyRemoteEnv(rc *RemoteConfig, envName string) (*Config, error) {
	env, ok := rc.Environments[envName]
	if !ok {
		// List available environments in error message
		var available []string
		for k := range rc.Environments {
			available = append(available, k)
		}
		sort.Strings(available)
		return nil, fmt.Errorf("unknown environment: %s (available: %v)", envName, available)
	}

	// Load existing config to preserve auth state when switching environments
	cfg, _ := Load()
	if cfg == nil {
		cfg = &Config{}
	}

	cfg.Env = envName
	cfg.ConsoleURL = env.Domains.Console
	cfg.ConductorURL = env.Domains.Conductor

	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	return cfg, nil
}
