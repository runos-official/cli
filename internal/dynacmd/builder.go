package dynacmd

import (
	"fmt"
	"strings"

	"cli/internal/manifest"

	"github.com/spf13/cobra"
)

// Builder builds Cobra commands from a manifest
type Builder struct {
	manifest *manifest.Manifest
	executor *Executor
}

// NewBuilder creates a new command builder
func NewBuilder(m *manifest.Manifest, executor *Executor) *Builder {
	return &Builder{
		manifest: m,
		executor: executor,
	}
}

// BuildCommands generates all commands from the manifest
func (b *Builder) BuildCommands() []*cobra.Command {
	// Map to track created parent commands
	parents := make(map[string]*cobra.Command)

	for _, cmdDef := range b.manifest.Commands {
		b.buildCommandTree(cmdDef, parents)
	}

	// Return top-level commands
	var topLevel []*cobra.Command
	for path, cmd := range parents {
		if !strings.Contains(path, "/") {
			topLevel = append(topLevel, cmd)
		}
	}

	return topLevel
}

func (b *Builder) buildCommandTree(cmdDef manifest.Command, parents map[string]*cobra.Command) {
	parts := strings.Split(cmdDef.Command, "/")

	// Build parent chain
	var currentPath string
	var parent *cobra.Command

	for i, part := range parts {
		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		isLeaf := i == len(parts)-1

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

	// Add --wait flag for commands that return jobs
	if cmdDef.ReturnsJob {
		cmd.Flags().Bool("wait", false, "Wait for job to complete")
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
				defaultVal = field.Default.(string)
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
