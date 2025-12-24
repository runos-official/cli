package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultConsoleURL    = "https://console.beta.runos.com"
	DefaultConductorURL  = "http://localhost:3025"
	configDirName        = ".runos"
	configFileName       = "config.json"
)

type FirebaseConfig struct {
	APIKey     string `json:"api_key,omitempty"`
	AuthDomain string `json:"auth_domain,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}

type Config struct {
	ConsoleURL       string          `json:"console_url,omitempty"`
	ConductorURL     string          `json:"conductor_url,omitempty"`
	AccountID        string          `json:"account_id,omitempty"`
	DefaultClusterID string          `json:"default_cluster_id,omitempty"`
	RefreshToken     string          `json:"refresh_token,omitempty"`
	Firebase         *FirebaseConfig `json:"firebase,omitempty"`
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

func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := DefaultConfig()
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to write default config: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.applyDefaults() {
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to update config with defaults: %w", err)
		}
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() bool {
	updated := false
	defaults := DefaultConfig()

	if c.ConsoleURL == "" {
		c.ConsoleURL = defaults.ConsoleURL
		updated = true
	}

	if c.ConductorURL == "" {
		c.ConductorURL = defaults.ConductorURL
		updated = true
	}

	return updated
}

func DefaultConfig() *Config {
	return &Config{
		ConsoleURL:   DefaultConsoleURL,
		ConductorURL: DefaultConductorURL,
	}
}

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

func (c *Config) GetConsoleURL() string {
	if envURL := os.Getenv("CONSOLE_URL"); envURL != "" {
		return envURL
	}
	if c.ConsoleURL != "" {
		return c.ConsoleURL
	}
	return DefaultConsoleURL
}

func (c *Config) GetConductorURL() string {
	if envURL := os.Getenv("CONDUCTOR_API_URL"); envURL != "" {
		return envURL
	}
	if c.ConductorURL != "" {
		return c.ConductorURL
	}
	return DefaultConductorURL
}

func (c *Config) GetDefaultClusterID() string {
	if envCID := os.Getenv("RUNOS_CLUSTER_ID"); envCID != "" {
		return envCID
	}
	return c.DefaultClusterID
}
