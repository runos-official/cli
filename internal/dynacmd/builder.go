// Package dynacmd builds and executes CLI commands dynamically from the API manifest.
package dynacmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
)

// placeholderRegex matches {name} patterns in command paths
var placeholderRegex = regexp.MustCompile(`^\{(\w+)\}$`)

// Builder builds Cobra commands from a manifest
type Builder struct {
	manifest     *manifest.Manifest
	executor     *Executor
	existingCmds map[string]*cobra.Command
}

// NewBuilder creates a new command builder
func NewBuilder(m *manifest.Manifest, executor *Executor) *Builder {
	return &Builder{
		manifest:     m,
		executor:     executor,
		existingCmds: make(map[string]*cobra.Command),
	}
}

// WithExistingCommands registers static commands that dynamic commands should merge with.
// When a manifest command has the same top-level name as an existing command, the dynamic
// subcommands will be added to the existing command rather than creating a new one.
// This allows static subcommands (like "clusters default") to coexist with dynamic ones
// (like "clusters list", "clusters show").
func (b *Builder) WithExistingCommands(cmds ...*cobra.Command) *Builder {
	for _, cmd := range cmds {
		b.existingCmds[cmd.Name()] = cmd
	}
	return b
}

// BuildCommands generates all commands from the manifest, merging with existing commands
func (b *Builder) BuildCommands() []*cobra.Command {
	// Map to track created parent commands, pre-populated with existing commands
	parents := make(map[string]*cobra.Command)
	for name, cmd := range b.existingCmds {
		parents[name] = cmd
	}

	for _, cmdDef := range b.manifest.Commands {
		b.buildCommandTree(cmdDef, parents)
	}

	// Return top-level commands (excluding ones that were passed in as existing)
	var topLevel []*cobra.Command
	for path, cmd := range parents {
		if !strings.Contains(path, "/") {
			if _, wasExisting := b.existingCmds[path]; !wasExisting {
				topLevel = append(topLevel, cmd)
			}
		}
	}

	return topLevel
}

func (b *Builder) buildCommandTree(cmdDef manifest.Command, parents map[string]*cobra.Command) {
	parts := strings.Split(cmdDef.Command, "/")

	// Filter out placeholder segments like {id} - they're just metadata, not actual commands
	// The placeholder value comes from input fields as flags (e.g., --id)
	var filteredParts []string
	for _, part := range parts {
		if !placeholderRegex.MatchString(part) {
			filteredParts = append(filteredParts, part)
		}
	}

	// Build parent chain
	var currentPath string
	var parent *cobra.Command

	for i, part := range filteredParts {
		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		isLeaf := i == len(filteredParts)-1

		if existing, ok := parents[currentPath]; ok {
			parent = existing
			continue
		}

		var cmd *cobra.Command
		if isLeaf {
			// Leaf command - has the actual execution logic
			cmd = b.buildLeafCommand(part, cmdDef)
		} else {
			// Intermediate command - just a container
			cmd = &cobra.Command{
				Use:   part,
				Short: "Manage " + part,
			}
		}

		parents[currentPath] = cmd

		if parent != nil {
			parent.AddCommand(cmd)
		}

		parent = cmd
	}
}

func (b *Builder) buildLeafCommand(name string, cmdDef manifest.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   b.buildUseLine(name, cmdDef),
		Short: cmdDef.Description,
		RunE: func(c *cobra.Command, args []string) error {
			// Check if required positional args are missing
			if cmdDef.Input != nil {
				argIndex := 0
				for _, field := range cmdDef.Input.Fields {
					if field.Positional {
						if argIndex >= len(args) && field.Required {
							// Missing required positional arg - show available options if enum exists
							if len(field.Enum) > 0 {
								return showEnumOptions(c, field)
							}
							return fmt.Errorf("missing required argument: %s", field.Name)
						}
						argIndex++
					}
				}
			}
			return b.executor.Execute(c, args, cmdDef)
		},
	}

	// Add flags from input schema
	if cmdDef.Input != nil {
		addFieldFlags(cmd, cmdDef.Input.Fields)
		addBoolFlags(cmd, cmdDef.Input.Flags)
	}

	// Add -f flag for file input (for commands with input fields)
	if cmdDef.Input != nil && len(cmdDef.Input.Fields) > 0 {
		cmd.Flags().StringP("file", "f", "", "YAML file with input values")
	}

	// Add --cid flag for cluster ID (if endpoint uses :cid)
	if strings.Contains(cmdDef.Endpoint, ":cid") {
		cmd.Flags().String("cid", "", "Cluster ID (uses default from config if not specified)")
	}

	// Add --json flag for JSON output
	cmd.Flags().Bool("json", false, "Output as JSON")

	// Add --follow flag for commands that return jobs (detected by jobId in output)
	if hasJobIdOutput(cmdDef) {
		cmd.Flags().Bool("follow", false, "Follow job progress until completion")
	}

	return cmd
}

func (b *Builder) buildUseLine(name string, cmdDef manifest.Command) string {
	useLine := name

	if cmdDef.Input == nil {
		return useLine
	}

	// Add positional args to use line
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			if field.Required {
				useLine += " <" + field.Name + ">"
			} else {
				useLine += " [" + field.Name + "]"
			}
		}
	}

	return useLine
}

func addFieldFlags(cmd *cobra.Command, fields []manifest.Field) {
	for _, field := range fields {
		if field.Positional {
			continue // Positional args are handled separately
		}

		switch field.Type {
		case "string":
			defaultVal := ""
			if field.Default != nil {
				if v, ok := field.Default.(string); ok {
					defaultVal = v
				}
			}
			cmd.Flags().String(field.Name, defaultVal, field.Description)

		case "integer":
			defaultVal := 0
			if field.Default != nil {
				switch v := field.Default.(type) {
				case int:
					defaultVal = v
				case float64:
					defaultVal = int(v)
				}
			}
			cmd.Flags().Int(field.Name, defaultVal, field.Description)

		case "array":
			cmd.Flags().StringSlice(field.Name, nil, field.Description)

		case "boolean":
			defaultVal := false
			if field.Default != nil {
				if v, ok := field.Default.(bool); ok {
					defaultVal = v
				}
			}
			cmd.Flags().Bool(field.Name, defaultVal, field.Description)
		}

		if field.Required {
			cmd.MarkFlagRequired(field.Name)
		}
	}
}

func addBoolFlags(cmd *cobra.Command, flags []manifest.Flag) {
	for _, flag := range flags {
		cmd.Flags().Bool(flag.Name, flag.Default, flag.Description)
	}
}

func showEnumOptions(cmd *cobra.Command, field manifest.Field) error {
	fmt.Printf("Available options for <%s>:\n\n", field.Name)
	for _, option := range field.Enum {
		fmt.Printf("  %s\n", option)
	}
	fmt.Printf("\nUsage: %s <%s>\n", cmd.CommandPath(), field.Name)
	return nil
}

func hasJobIdOutput(cmdDef manifest.Command) bool {
	if cmdDef.Output == nil {
		return false
	}
	for _, field := range cmdDef.Output.Fields {
		if field.Name == "jobId" {
			return true
		}
	}
	return false
}
