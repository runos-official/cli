package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringOrSlice handles YAML fields that can be either a single string or an array of strings
type StringOrSlice []string

// UnmarshalYAML implements the yaml.Unmarshaler interface, accepting either a single string or an array of strings.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// Single string value
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		// Array of strings
		var items []string
		if err := value.Decode(&items); err != nil {
			return err
		}
		*s = items
		return nil
	default:
		return fmt.Errorf("domain must be a string or array of strings")
	}
}

// MarshalYAML implements the yaml.Marshaler interface, encoding a single-element slice as a scalar string.
func (s StringOrSlice) MarshalYAML() (any, error) { //nolint:ireturn
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

// MarshalJSON implements the json.Marshaler interface, always encoding as a JSON array of strings.
func (s StringOrSlice) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(s))
}

// DeployConfig represents the runos.yaml configuration file
type DeployConfig struct {
	App             string                        `yaml:"app" json:"app"`
	Port            int                           `yaml:"port" json:"port"`
	ID              string                        `yaml:"id,omitempty" json:"id,omitempty"`
	CID             string                        `yaml:"cid,omitempty" json:"cid,omitempty"`
	AID             string                        `yaml:"aid,omitempty" json:"aid,omitempty"`
	Env             string                        `yaml:"env,omitempty" json:"env,omitempty"`
	Domain          StringOrSlice                 `yaml:"domain,omitempty" json:"domain,omitempty"`
	Dockerfile      string                        `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	CPURequestMc    int                           `yaml:"cpuRequestMc,omitempty" json:"cpuRequestMc,omitempty"`
	CPULimitMc      int                           `yaml:"cpuLimitMc,omitempty" json:"cpuLimitMc,omitempty"`
	MemoryRequestMb int                           `yaml:"memoryRequestMb,omitempty" json:"memoryRequestMb,omitempty"`
	MemoryLimitMb   int                           `yaml:"memoryLimitMb,omitempty" json:"memoryLimitMb,omitempty"`
	StandardHttps   *bool                         `yaml:"standardHttps,omitempty" json:"standardHttps,omitempty"`
	Requires        map[string]ServiceRequirement `yaml:"requires,omitempty" json:"requires,omitempty"`
	CustomEnvVars   map[string]string             `yaml:"-" json:"customEnvVars,omitempty"`
}

// ServiceRequirement defines a dependent service (e.g., PostgreSQL, Valkey)
type ServiceRequirement struct {
	ID     string            `yaml:"id,omitempty" json:"id,omitempty"`
	Type   string            `yaml:"type" json:"type"`
	Class  string            `yaml:"class,omitempty" json:"class,omitempty"`
	Config map[string]any    `yaml:"config,omitempty" json:"config,omitempty"`
	Env    map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// LoadConfig reads and parses a runos.yaml config file
func LoadConfig(path string) (*DeployConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("runos.yaml not found at %s", path)
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config DeployConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

// Validate checks that required fields are present
func (c *DeployConfig) Validate() error {
	if c.App == "" {
		return fmt.Errorf("app name is required in runos.yaml")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("valid port (1-65535) is required in runos.yaml")
	}
	return nil
}

// SaveConfig writes the config back to the file
func SaveConfig(path string, config *DeployConfig) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// ValidateAID checks that the config AID matches the session AID
func ValidateAID(configAID, sessionAID string) error {
	if configAID == "" {
		return nil // No AID in config, allow
	}
	if configAID == sessionAID {
		return nil // AIDs match, allow
	}
	return fmt.Errorf("config file AID (%s) does not match session AID (%s). Ensure you're logged into the correct account", configAID, sessionAID)
}

// ResolveEnvPath determines the env file path based on config state.
// Priority: 1) explicit env field in config, 2) legacy .runos.{CID}.env file,
// 3) default .runos.{CID}.{ID}.env if app ID is known.
// Returns the resolved absolute path and whether the config was modified (needs saving).
func ResolveEnvPath(configDir string, config *DeployConfig, cid string) (string, bool) {
	// Explicit env path in config — use as-is
	if config.Env != "" {
		return filepath.Join(configDir, config.Env), false
	}

	// Backwards compat: check for legacy .runos.{CID}.env
	legacyFilename := fmt.Sprintf(".runos.%s.env", cid)
	legacyPath := filepath.Join(configDir, legacyFilename)
	if _, err := os.Stat(legacyPath); err == nil {
		config.Env = legacyFilename
		return legacyPath, true
	}

	// Default: .runos.{CID}.{ID}.env (only if ID is known)
	if config.ID != "" {
		filename := fmt.Sprintf(".runos.%s.%s.env", cid, config.ID)
		config.Env = filename
		return filepath.Join(configDir, filename), true
	}

	return "", false
}

// DefaultEnvFilename returns the default env filename for a given cluster and app ID.
func DefaultEnvFilename(cid, appID string) string {
	return fmt.Sprintf(".runos.%s.%s.env", cid, appID)
}

// LoadEnvFile reads an env file at the given path and returns key-value pairs.
// Returns nil, nil if the path is empty or the file does not exist.
func LoadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", filepath.Base(path), err)
	}

	envVars := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			value = strings.Trim(value, `"'`)
			envVars[key] = value
		}
	}

	return envVars, nil
}

// SaveEnvFile writes env vars to the given path.
func SaveEnvFile(path string, envVars map[string]string) error {
	if path == "" {
		return fmt.Errorf("env file path is required")
	}

	var lines []string
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, envVars[k]))
	}

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}

	return nil
}
