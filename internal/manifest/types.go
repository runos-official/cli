package manifest

// Manifest is the root structure for the CLI manifest
type Manifest struct {
	Version  string    `yaml:"version"`
	Commands []Command `yaml:"commands"`
}

// Command defines a single CLI command
type Command struct {
	Command     string  `yaml:"command"`               // e.g., "services/add/valkey"
	Description string  `yaml:"description,omitempty"`
	Endpoint    string  `yaml:"endpoint"`              // e.g., "/api/v1/services/valkey"
	Method      string  `yaml:"method"`                // GET, POST, DELETE, etc.
	Input       *Input  `yaml:"input,omitempty"`
	Output      *Output `yaml:"output,omitempty"`
	ReturnsJob  bool    `yaml:"returns_job,omitempty"` // Supports --wait flag
}

// Input defines the input schema for a command
type Input struct {
	Fields []Field `yaml:"fields,omitempty"`
	Flags  []Flag  `yaml:"flags,omitempty"` // Boolean-only flags
}

// Field defines a single input field
type Field struct {
	Name        string      `yaml:"name"`
	Type        string      `yaml:"type"`                  // string, integer, array, etc.
	Description string      `yaml:"description,omitempty"`
	Required    bool        `yaml:"required,omitempty"`
	Default     interface{} `yaml:"default,omitempty"`
	Enum        []string    `yaml:"enum,omitempty"`
	Format      string      `yaml:"format,omitempty"`     // e.g., "key_value" for tags
	Positional  bool        `yaml:"positional,omitempty"` // true = positional arg, not flag
}

// Flag defines a boolean flag
type Flag struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Default     bool   `yaml:"default"`
}

// Output defines the output schema for a command
type Output struct {
	Type   string   `yaml:"type,omitempty"`   // "object" or "array"
	Fields []string `yaml:"fields,omitempty"` // Fields to display in table output
}
