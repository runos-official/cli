package mcp

import (
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

var commandPlaceholder = regexp.MustCompile(`/?\{[^}]+\}`)

func manifestToolName(command string) string {
	return strings.ReplaceAll(commandPlaceholder.ReplaceAllString(command, ""), "/", "_")
}

func shadowsReservedTool(command, name string) bool {
	if name == "deploy" {
		return command != name
	}
	gatePath := strings.ReplaceAll(name, "_", "/")
	return isGateTool(gatePath) && command != gatePath
}

func manifestCommandForTool(m *manifest.Manifest, name string) *manifest.Command {
	for i := range m.Commands {
		command := m.Commands[i].Command
		if manifestToolName(command) == name && !shadowsReservedTool(command, name) {
			return &m.Commands[i]
		}
	}
	return nil
}

func commandInTier(cmd manifest.Command, tier string) bool {
	if cmd.Command == "deploy" && tier != "sensitive_write" {
		return false
	}
	return isGateTool(cmd.Command) || contains(cmd.MCP, tier)
}

func staticToolGroup(name string) func(string) []Tool {
	switch {
	case isStaticRunTool(name):
		return staticRunTools
	case isStaticAppsTool(name):
		return staticAppsTools
	case isStaticServicesTool(name):
		return staticServicesTools
	case isStaticJobsTool(name):
		return staticJobsTools
	}
	return nil
}

func (s *Server) toolAllowedInTier(name string) bool {
	if isManifestUpdateTool(name) || name == toolsEnableToolName {
		return true
	}
	if group := staticToolGroup(name); group != nil {
		for _, tool := range group(s.category) {
			if tool.Name == name {
				return true
			}
		}
		return false
	}

	// Account toolsets trim discovery. The tier is the dispatch boundary.
	cmd := manifestCommandForTool(s.manifest, name)
	return cmd != nil && commandInTier(*cmd, s.category)
}
