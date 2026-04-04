package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// LoadEnvFile reads a .runos.{CID}.env file and returns key-value pairs
func LoadEnvFile(dir, cid string) (map[string]string, error) {
	// Validate cid to prevent path traversal
	if strings.ContainsAny(cid, "/\\..") {
		return nil, fmt.Errorf("invalid cluster ID: %s", cid)
	}

	filename := fmt.Sprintf(".runos.%s.env", cid)
	path := filepath.Join(dir, filename)

	// Ensure resolved path stays within the expected directory
	cleanDir := filepath.Clean(dir)
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, cleanDir+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid cluster ID: path traversal detected")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // File not found is OK, return empty
		}
		return nil, fmt.Errorf("failed to read %s: %w", filename, err)
	}

	envVars := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			// Remove surrounding quotes if present
			value = strings.Trim(value, `"'`)
			envVars[key] = value
		}
	}

	return envVars, nil
}
