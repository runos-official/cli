package manifest

// Manifest is the root structure for the CLI manifest
type Manifest struct {
	Version  string    `yaml:"version" json:"version"`
	Commands []Command `yaml:"commands" json:"commands"`
}

// Command defines a single CLI command
type Command struct {
	Command     string   `yaml:"command" json:"command"` // e.g., "services/add/valkey"
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Endpoint    string   `yaml:"endpoint" json:"endpoint"`                       // e.g., "/api/v1/services/valkey"
	Method      string   `yaml:"method" json:"method"`                           // GET, POST, DELETE, etc.
	Sensitive   bool     `yaml:"sensitive,omitempty" json:"sensitive,omitempty"` // Contains sensitive data (credentials, secrets)
	MCP         []string `yaml:"mcp,omitempty" json:"mcp,omitempty"`             // MCP servers: read, sensitive_read, write, sensitive_write
	Input       *Input   `yaml:"input,omitempty" json:"input,omitempty"`
	Output      *Output  `yaml:"output,omitempty" json:"output,omitempty"`
	ReturnsJob  bool     `yaml:"returnsJob,omitempty" json:"returnsJob,omitempty"` // Supports --wait flag
}

// Input defines the input schema for a command
type Input struct {
	Fields []Field `yaml:"fields,omitempty" json:"fields,omitempty"`
	Flags  []Flag  `yaml:"flags,omitempty" json:"flags,omitempty"` // Boolean-only flags
}

// Field defines a single input field
type Field struct {
	Name        string   `yaml:"name" json:"name"`
	Type        string   `yaml:"type" json:"type"` // string, integer, array, etc.
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Default     any      `yaml:"default,omitempty" json:"default,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	Format      string   `yaml:"format,omitempty" json:"format,omitempty"`         // e.g., "key_value" for tags
	Positional  bool     `yaml:"positional,omitempty" json:"positional,omitempty"` // true = positional arg, not flag
}

// Flag defines a boolean flag
type Flag struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Default     bool   `yaml:"default" json:"default"`
}

// Output defines the output schema for a command
type Output struct {
	Type   string   `yaml:"type,omitempty" json:"type,omitempty"`     // "object" or "array"
	Fields []string `yaml:"fields,omitempty" json:"fields,omitempty"` // Fields to display in table output
}
