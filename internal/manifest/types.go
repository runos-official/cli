package manifest

import "encoding/json"

// Manifest is the root structure for the CLI manifest
type Manifest struct {
	Version  string    `yaml:"version" json:"version"`
	Commands []Command `yaml:"commands" json:"commands"`
}

// Command defines a single CLI command
type Command struct {
	Command     string `yaml:"command" json:"command"` // e.g., "services/add/valkey"
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Endpoint    string `yaml:"endpoint" json:"endpoint"` // e.g., "/api/v1/services/valkey"
	Method      string `yaml:"method" json:"method"`     // GET, POST, DELETE, etc.
	// Sensitivity is carried by MCP, not by a separate flag: a command
	// that returns a credential declares `sensitive_read` and one that
	// takes a credential declares `sensitive_write`. A `Sensitive bool`
	// field used to sit here, parsed on every load and read by nothing,
	// and conductor never emitted the key (B15).
	MCP        []string `yaml:"mcp,omitempty" json:"mcp,omitempty"` // MCP servers: read, sensitive_read, write, sensitive_write
	Input      *Input   `yaml:"input,omitempty" json:"input,omitempty"`
	Output     *Output  `yaml:"output,omitempty" json:"output,omitempty"`
	ReturnsJob bool     `yaml:"returnsJob,omitempty" json:"returnsJob,omitempty"` // Supports --wait flag
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
	// AllowEmpty opts a required string field out of the CLI's
	// empty-required gate (validateInputValues). Used when empty-string
	// carries semantic meaning (e.g. `nodes/rename --name ""` clears
	// the display name back to the bootstrap default). Conductor's
	// manifest sets `allowEmpty: true` on the relevant fields; the CLI
	// honours it by skipping the empty-string refusal but still leaves
	// the rest of the required-input plumbing (positional/flag/-f file
	// missing-arg gate) intact. Regression target: I13-K.
	AllowEmpty bool `yaml:"allowEmpty,omitempty" json:"allowEmpty,omitempty"`
	// ItemType + ItemFields describe array element shape. When Type=="array"
	// and ItemType is set, the MCP tool-schema projection emits a richer
	// `items` definition than the default `{type: "string"}`. ItemFields
	// declares object-element key shape (used when ItemType == "object").
	// Manifest fields are optional; when absent the projection falls back
	// to the legacy `items: {type: "string"}` shape.
	ItemType   string  `yaml:"itemType,omitempty" json:"itemType,omitempty"`
	ItemFields []Field `yaml:"itemFields,omitempty" json:"itemFields,omitempty"`
	// ValueType + ValueFields describe MAP-VALUE shape for object-typed
	// fields whose semantics are "map[<key>] → <value-shape>" rather
	// than a fixed-property record. Canonical case: `requires` on
	// apps/add / apps/update / deploy, which is `map[alias] →
	// {id, type, config, env}`. When ValueType is set, the MCP tool-
	// schema projection emits a richer `additionalProperties`
	// definition than the default `{type: "string"}`. ValueFields
	// declares object-value key shape (used when ValueType == "object").
	// Manifest fields are optional; when absent the projection falls
	// back to the legacy `additionalProperties: {type: "string"}`
	// shape (with the providerOptions and requires carve-outs that
	// already exist server-CLI-side). Regression target: I26-N.
	ValueType   string  `yaml:"valueType,omitempty" json:"valueType,omitempty"`
	ValueFields []Field `yaml:"valueFields,omitempty" json:"valueFields,omitempty"`
}

// Flag defines a boolean flag
type Flag struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Default     bool   `yaml:"default" json:"default"`
}

// Output defines the output schema for a command.
//
// Fields may be a mix of bare strings (legacy, name-only) and rich
// objects (name + type + description + enum). The CLI normalises both
// into OutputField; callers that only need the field names should use
// FieldNames() rather than ranging over Fields directly.
type Output struct {
	Type   string        `yaml:"type,omitempty" json:"type,omitempty"`
	Fields []OutputField `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// FieldNames returns the names of every entry in Fields, in declaration
// order. Convenience helper for callers (output formatter, dynacmd
// hasJobIdOutput) that don't care about per-field type/enum metadata.
func (o *Output) FieldNames() []string {
	if o == nil {
		return nil
	}
	out := make([]string, 0, len(o.Fields))
	for _, f := range o.Fields {
		out = append(out, f.Name)
	}
	return out
}

// OutputField describes one column / key in a command's output. The
// JSON unmarshaller accepts either a bare string (legacy shape: name
// only) or a full object with name + type + description + enum.
type OutputField struct {
	Name        string   `yaml:"name" json:"name"`
	Type        string   `yaml:"type,omitempty" json:"type,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
}

// UnmarshalJSON accepts either a bare string (legacy: just the field
// name) or a full object. Strings become OutputField{Name: s}; objects
// are unmarshalled normally.
func (f *OutputField) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return err
		}
		f.Name = name
		return nil
	}
	type rawOutputField OutputField
	var raw rawOutputField
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*f = OutputField(raw)
	return nil
}

// MarshalJSON writes a bare string when only Name is populated (legacy
// round-trip), or a full object when richer metadata is present. Keeps
// `runos manifest update` writes byte-stable for legacy entries.
func (f OutputField) MarshalJSON() ([]byte, error) {
	if f.Type == "" && f.Description == "" && len(f.Enum) == 0 {
		return json.Marshal(f.Name)
	}
	type rawOutputField OutputField
	return json.Marshal(rawOutputField(f))
}
