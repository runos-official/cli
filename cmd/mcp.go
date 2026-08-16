package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/mcp"
	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
)

// placeholderRegex matches {name} patterns in command paths
var placeholderRegex = regexp.MustCompile(`/?\{[^}]+\}`)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server commands for AI assistant integration",
	Long:  `Commands for the Model Context Protocol (MCP) server integration with AI assistants like Claude Code.`,
}

var mcpServeCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Run an MCP server on stdio",
	Long:   `Run the Model Context Protocol (MCP) server on stdio for integration with AI assistants.`,
	Hidden: true, // Internal command, users shouldn't call this directly
}

var mcpServeReadCmd = &cobra.Command{
	Use:    "read",
	Short:  "Run the read-only MCP server",
	Hidden: true,
	RunE:   func(cmd *cobra.Command, args []string) error { return runMCPServe("read") },
}

var mcpServeSensitiveReadCmd = &cobra.Command{
	Use:    "sensitive-read",
	Short:  "Run the sensitive read MCP server",
	Hidden: true,
	RunE:   func(cmd *cobra.Command, args []string) error { return runMCPServe("sensitive_read") },
}

var mcpServeWriteCmd = &cobra.Command{
	Use:    "write",
	Short:  "Run the write MCP server",
	Hidden: true,
	RunE:   func(cmd *cobra.Command, args []string) error { return runMCPServe("write") },
}

var mcpServeSensitiveWriteCmd = &cobra.Command{
	Use:    "sensitive-write",
	Short:  "Run the sensitive write MCP server",
	Hidden: true,
	RunE:   func(cmd *cobra.Command, args []string) error { return runMCPServe("sensitive_write") },
}

var mcpConfigureCmd = &cobra.Command{
	Use:   "configure <target>",
	Short: "Configure MCP server for an AI assistant",
	Long:  `Configure the RunOS MCP server for integration with AI assistants.`,
	Run:   runMCPConfigure,
}

var mcpConfigureClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Configure the RunOS MCP server for Claude Code (project-level)",
	Long: `Add the RunOS MCP server to the current project's .mcp.json configuration.

This creates or updates .mcp.json in the current directory, scoping the RunOS
tools to this project only. Claude Code will have access to RunOS tools for
managing clusters, services, and applications when working in this project.`,
	RunE: runMCPConfigureClaude,
}

var mcpConfigureOpencodeCmd = &cobra.Command{
	Use:   "opencode",
	Short: "Configure the RunOS MCP server for OpenCode (project-level)",
	Long: `Add the RunOS MCP server to the current project's opencode.json configuration.

This creates or updates opencode.json in the current directory, scoping the RunOS
tools to this project only. OpenCode will have access to RunOS tools for
managing clusters, services, and applications when working in this project.`,
	RunE: runMCPConfigureOpencode,
}

var mcpConfigureGeminiCmd = &cobra.Command{
	Use:   "gemini",
	Short: "Configure the RunOS MCP server for Gemini CLI (project-level)",
	Long: `Add the RunOS MCP server to the current project's .gemini/settings.json configuration.

This creates or updates .gemini/settings.json in the current directory, scoping the RunOS
tools to this project only. Gemini CLI will have access to RunOS tools for
managing clusters, services, and applications when working in this project.`,
	RunE: runMCPConfigureGemini,
}

var mcpConfigureCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Configure the RunOS MCP server for OpenAI Codex (project-level)",
	Long: `Add the RunOS MCP server to the current project's .codex/config.toml configuration.

This creates or updates .codex/config.toml in the current directory, scoping the RunOS
tools to this project only. Also creates rules to auto-allow read-only operations.`,
	RunE: runMCPConfigureCodex,
}

func init() {
	mcpCmd.AddCommand(mcpServeCmd)
	mcpServeCmd.AddCommand(mcpServeReadCmd)
	mcpServeCmd.AddCommand(mcpServeSensitiveReadCmd)
	mcpServeCmd.AddCommand(mcpServeWriteCmd)
	mcpServeCmd.AddCommand(mcpServeSensitiveWriteCmd)
	mcpCmd.AddCommand(mcpConfigureCmd)
	mcpConfigureCmd.AddCommand(mcpConfigureClaudeCmd)
	mcpConfigureCmd.AddCommand(mcpConfigureOpencodeCmd)
	mcpConfigureCmd.AddCommand(mcpConfigureGeminiCmd)
	mcpConfigureCmd.AddCommand(mcpConfigureCodexCmd)
	mcpConfigureCodexCmd.Flags().BoolP("yes", "y", false, "Skip warning and confirmation prompt")
}

func runMCPConfigure(cmd *cobra.Command, args []string) {
	fmt.Println("Available targets:")
	fmt.Println("  claude    Configure for Claude Code CLI (project-level)")
	fmt.Println("  opencode  Configure for OpenCode (project-level)")
	fmt.Println("  gemini    Configure for Gemini CLI (project-level)")
	fmt.Println("  codex     Configure for OpenAI Codex (project-level)")
	fmt.Println()
	fmt.Println("Usage: runos mcp configure <target>")
}

func runMCPServe(category string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(home, ".runos")

	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	executor := mcp.NewCommandExecutor(m, cfg.GetAPIURL())
	server := mcp.NewServer(m, executor, version.Version, category)
	// Without a default cluster the tool schema has to mark `cid`
	// required, since there is nothing to fall back on (B13).
	server.SetDefaultClusterID(cfg.GetDefaultClusterID())

	return server.Run()
}

func runMCPConfigureClaude(cmd *cobra.Command, args []string) error {
	// Skip if already configured
	if content, err := os.ReadFile(".mcp.json"); err == nil {
		var data map[string]any
		if json.Unmarshal(content, &data) == nil {
			if servers, ok := data["mcpServers"].(map[string]any); ok {
				if _, ok := servers["runos"]; ok {
					fmt.Println("RunOS MCP already configured in .mcp.json, skipping.")
					return nil
				}
			}
		}
	}

	// Find the runos binary path
	runosPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find runos executable: %w", err)
	}

	// Resolve any symlinks to get the actual path
	runosPath, err = filepath.EvalSymlinks(runosPath)
	if err != nil {
		return fmt.Errorf("failed to resolve runos path: %w", err)
	}

	fmt.Println("Configuring RunOS MCP server for Claude Code (project-level)...")

	// Add MCP servers to project's .mcp.json
	if err := addMCPServersToProjectConfig(runosPath); err != nil {
		return fmt.Errorf("failed to configure MCP server: %w", err)
	}

	// Add allowed tools to project's .claude/settings.json
	if err := addAllowedToolsToClaudeSettings(); err != nil {
		return fmt.Errorf("failed to configure Claude settings: %w", err)
	}

	fmt.Println("\nRunOS MCP servers configured successfully!")
	fmt.Println("Claude Code now has access to RunOS tools in this project.")

	return nil
}

func getReadToolNames(m *manifest.Manifest) []string {
	var toolNames []string

	for _, cmd := range m.Commands {
		// Check if command belongs to "read" MCP category
		hasRead := false
		for _, cat := range cmd.MCP {
			if cat == "read" {
				hasRead = true
				break
			}
		}
		if !hasRead {
			continue
		}

		// Tool name = command path with {id} placeholders stripped, then / replaced by _
		cmdPath := placeholderRegex.ReplaceAllString(cmd.Command, "")
		toolName := strings.ReplaceAll(cmdPath, "/", "_")

		toolNames = append(toolNames, toolName)
	}

	return toolNames
}

func addMCPServersToProjectConfig(runosPath string) error {
	mcpJSON := ".mcp.json"

	// Read existing config or create empty one
	var configData map[string]any
	content, err := os.ReadFile(mcpJSON)
	if err != nil {
		if os.IsNotExist(err) {
			configData = make(map[string]any)
		} else {
			return err
		}
	} else {
		if err := json.Unmarshal(content, &configData); err != nil {
			return fmt.Errorf("failed to parse .mcp.json: %w", err)
		}
	}

	// Get or create mcpServers section
	mcpServers, ok := configData["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
	}

	// Add all 4 RunOS MCP servers
	mcpServers["runos"] = map[string]any{
		"type":    "stdio",
		"command": runosPath,
		"args":    []string{"mcp", "serve", "read"},
		"env":     map[string]any{},
	}
	mcpServers["runos-sensitive-read"] = map[string]any{
		"type":    "stdio",
		"command": runosPath,
		"args":    []string{"mcp", "serve", "sensitive-read"},
		"env":     map[string]any{},
	}
	mcpServers["runos-write"] = map[string]any{
		"type":    "stdio",
		"command": runosPath,
		"args":    []string{"mcp", "serve", "write"},
		"env":     map[string]any{},
	}
	mcpServers["runos-sensitive-write"] = map[string]any{
		"type":    "stdio",
		"command": runosPath,
		"args":    []string{"mcp", "serve", "sensitive-write"},
		"env":     map[string]any{},
	}

	configData["mcpServers"] = mcpServers

	// Write back
	newContent, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(mcpJSON, newContent, 0644); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Updated %s\n", filepath.Join(cwd, mcpJSON))
	return nil
}

func addAllowedToolsToClaudeSettings() error {
	// Create .claude/hooks directory if it doesn't exist
	hooksDir := filepath.Join(".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return err
	}

	// Create the guard hook script for sensitive operations
	if err := createGuardHook(hooksDir); err != nil {
		return err
	}

	settingsFile := filepath.Join(".claude", "settings.json")

	// Read existing settings or create empty one
	var settingsData map[string]any
	content, err := os.ReadFile(settingsFile)
	if err != nil {
		if os.IsNotExist(err) {
			settingsData = make(map[string]any)
		} else {
			return err
		}
	} else {
		if err := json.Unmarshal(content, &settingsData); err != nil {
			return fmt.Errorf("failed to parse .claude/settings.json: %w", err)
		}
	}

	// Get or create permissions section
	permissions, ok := settingsData["permissions"].(map[string]any)
	if !ok {
		permissions = make(map[string]any)
	}

	// Get existing allow list
	var allowList []string
	if existing, ok := permissions["allow"].([]any); ok {
		for _, t := range existing {
			if s, ok := t.(string); ok {
				allowList = append(allowList, s)
			}
		}
	}

	// Add wildcard for read-only RunOS tools (mcp__runos__* only, not the other servers)
	runosReadWildcard := "mcp__runos__*"
	found := false
	for _, t := range allowList {
		if t == runosReadWildcard {
			found = true
			break
		}
	}
	if !found {
		allowList = append(allowList, runosReadWildcard)
	}

	permissions["allow"] = allowList
	settingsData["permissions"] = permissions

	// Add hooks configuration for sensitive operations
	hooks, ok := settingsData["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
	}

	// Set up PreToolUse hook for sensitive RunOS operations
	hooks["PreToolUse"] = []any{
		map[string]any{
			"matcher": "mcp__runos-sensitive.*",
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": ".claude/hooks/runos-guard.sh",
				},
			},
		},
	}

	settingsData["hooks"] = hooks

	// Write back
	newContent, err := json.MarshalIndent(settingsData, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(settingsFile, newContent, 0644); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Updated %s\n", filepath.Join(cwd, settingsFile))
	return nil
}

func createGuardHook(hooksDir string) error {
	hookScript := `#!/bin/bash
set -euo pipefail

# Warn about sensitive data exposure to the LLM
echo '{"message": "This operation involves sensitive data that will be visible to the LLM"}'
exit 0
`

	hookPath := filepath.Join(hooksDir, "runos-guard.sh")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Created %s\n", filepath.Join(cwd, hookPath))
	return nil
}

func runMCPConfigureOpencode(cmd *cobra.Command, args []string) error {
	// Skip if already configured
	if content, err := os.ReadFile("opencode.json"); err == nil {
		var data map[string]any
		if json.Unmarshal(content, &data) == nil {
			if mcp, ok := data["mcp"].(map[string]any); ok {
				if _, ok := mcp["runos"]; ok {
					fmt.Println("RunOS MCP already configured in opencode.json, skipping.")
					return nil
				}
			}
		}
	}

	// Find the runos binary path
	runosPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find runos executable: %w", err)
	}

	// Resolve any symlinks to get the actual path
	runosPath, err = filepath.EvalSymlinks(runosPath)
	if err != nil {
		return fmt.Errorf("failed to resolve runos path: %w", err)
	}

	fmt.Println("Configuring RunOS MCP server for OpenCode (project-level)...")

	if err := addMCPServersToOpencodeConfig(runosPath); err != nil {
		return fmt.Errorf("failed to configure MCP server: %w", err)
	}

	fmt.Println("\nRunOS MCP servers configured successfully!")
	fmt.Println("OpenCode now has access to RunOS tools in this project.")

	return nil
}

func addMCPServersToOpencodeConfig(runosPath string) error {
	configFile := "opencode.json"

	// Read existing config or create empty one
	var configData map[string]any
	content, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			configData = map[string]any{
				"$schema": "https://opencode.ai/config.json",
			}
		} else {
			return err
		}
	} else {
		if err := json.Unmarshal(content, &configData); err != nil {
			return fmt.Errorf("failed to parse opencode.json: %w", err)
		}
	}

	// Get or create mcp section
	mcpSection, ok := configData["mcp"].(map[string]any)
	if !ok {
		mcpSection = make(map[string]any)
	}

	// Add all 4 RunOS MCP servers in OpenCode format
	mcpSection["runos"] = map[string]any{
		"type":        "local",
		"command":     []string{runosPath, "mcp", "serve", "read"},
		"environment": map[string]any{},
		"enabled":     true,
	}
	mcpSection["runos-sensitive-read"] = map[string]any{
		"type":        "local",
		"command":     []string{runosPath, "mcp", "serve", "sensitive-read"},
		"environment": map[string]any{},
		"enabled":     true,
	}
	mcpSection["runos-write"] = map[string]any{
		"type":        "local",
		"command":     []string{runosPath, "mcp", "serve", "write"},
		"environment": map[string]any{},
		"enabled":     true,
	}
	mcpSection["runos-sensitive-write"] = map[string]any{
		"type":        "local",
		"command":     []string{runosPath, "mcp", "serve", "sensitive-write"},
		"environment": map[string]any{},
		"enabled":     true,
	}

	configData["mcp"] = mcpSection

	// Write back
	newContent, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(configFile, newContent, 0644); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Updated %s\n", filepath.Join(cwd, configFile))
	return nil
}

func runMCPConfigureGemini(cmd *cobra.Command, args []string) error {
	// Skip if already configured
	geminiSettings := filepath.Join(".gemini", "settings.json")
	if content, err := os.ReadFile(geminiSettings); err == nil {
		var data map[string]any
		if json.Unmarshal(content, &data) == nil {
			if servers, ok := data["mcpServers"].(map[string]any); ok {
				if _, ok := servers["runos"]; ok {
					fmt.Println("RunOS MCP already configured in .gemini/settings.json, skipping.")
					return nil
				}
			}
		}
	}

	// Find the runos binary path
	runosPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find runos executable: %w", err)
	}

	// Resolve any symlinks to get the actual path
	runosPath, err = filepath.EvalSymlinks(runosPath)
	if err != nil {
		return fmt.Errorf("failed to resolve runos path: %w", err)
	}

	fmt.Println("Configuring RunOS MCP server for Gemini CLI (project-level)...")

	if err := addMCPServersToGeminiConfig(runosPath); err != nil {
		return fmt.Errorf("failed to configure MCP server: %w", err)
	}

	fmt.Println("\nRunOS MCP servers configured successfully!")
	fmt.Println("Gemini CLI now has access to RunOS tools in this project.")

	return nil
}

func addMCPServersToGeminiConfig(runosPath string) error {
	geminiDir := ".gemini"
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		return fmt.Errorf("failed to create .gemini directory: %w", err)
	}

	settingsFile := filepath.Join(geminiDir, "settings.json")

	// Read existing config or create empty one
	var configData map[string]any
	content, err := os.ReadFile(settingsFile)
	if err != nil {
		if os.IsNotExist(err) {
			configData = make(map[string]any)
		} else {
			return err
		}
	} else {
		if err := json.Unmarshal(content, &configData); err != nil {
			return fmt.Errorf("failed to parse .gemini/settings.json: %w", err)
		}
	}

	// Get or create mcpServers section
	mcpServers, ok := configData["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
	}

	// Add all 4 RunOS MCP servers
	mcpServers["runos"] = map[string]any{
		"command": runosPath,
		"args":    []string{"mcp", "serve", "read"},
		"env":     map[string]any{},
	}
	mcpServers["runos-sensitive-read"] = map[string]any{
		"command": runosPath,
		"args":    []string{"mcp", "serve", "sensitive-read"},
		"env":     map[string]any{},
	}
	mcpServers["runos-write"] = map[string]any{
		"command": runosPath,
		"args":    []string{"mcp", "serve", "write"},
		"env":     map[string]any{},
	}
	mcpServers["runos-sensitive-write"] = map[string]any{
		"command": runosPath,
		"args":    []string{"mcp", "serve", "sensitive-write"},
		"env":     map[string]any{},
	}

	configData["mcpServers"] = mcpServers

	// Write back
	newContent, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(settingsFile, newContent, 0644); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Updated %s\n", filepath.Join(cwd, settingsFile))
	return nil
}

func runMCPConfigureCodex(cmd *cobra.Command, args []string) error {
	// Skip if already configured
	if content, err := os.ReadFile(".codex/config.toml"); err == nil {
		if strings.Contains(string(content), "[mcp_servers.runos]") {
			fmt.Println("RunOS MCP already configured in .codex/config.toml, skipping.")
			return nil
		}
	}

	// Find the runos binary path
	runosPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find runos executable: %w", err)
	}

	// Resolve any symlinks to get the actual path
	runosPath, err = filepath.EvalSymlinks(runosPath)
	if err != nil {
		return fmt.Errorf("failed to resolve runos path: %w", err)
	}

	// Load config and manifest to get read tool names
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	configDir := filepath.Join(home, ".runos")

	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	// Get all read tool names from manifest
	readToolNames := getReadToolNames(m)

	skipConfirm, _ := cmd.Flags().GetBool("yes")

	if !skipConfirm {
		// Get current working directory for the warning
		projectPath, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}

		// Show warning and ask for confirmation
		fmt.Println("╔════════════════════════════════════════════════════════════════════════════╗")
		fmt.Println("║                              ⚠️  WARNING ⚠️                                 ║")
		fmt.Println("╚════════════════════════════════════════════════════════════════════════════╝")
		fmt.Println()
		fmt.Println("You are about to configure RunOS MCP for OpenAI Codex.")
		fmt.Println()
		fmt.Println("IMPORTANT: Codex does NOT ask for permission before executing tools.")
		fmt.Println("This means Codex can perform destructive operations WITHOUT confirmation:")
		fmt.Println()
		fmt.Println("  • Delete services from your cluster")
		fmt.Println("  • Add or remove nodes")
		fmt.Println("  • Modify application configurations")
		fmt.Println("  • Rotate credentials and secrets")
		fmt.Println()
		fmt.Println("This is NOT RECOMMENDED for production clusters.")
		fmt.Println()
		fmt.Println("────────────────────────────────────────────────────────────────────────────────")
		fmt.Println()
		fmt.Println("This will:")
		fmt.Println("  1. Add MCP server config to .codex/config.toml (project-level)")
		fmt.Println("  2. Add auto-allow rules for read-only RunOS tools")
		fmt.Printf("  3. Add this project as 'trusted' in ~/.codex/config.toml\n")
		fmt.Println()
		fmt.Printf("Project path: %s\n", projectPath)
		fmt.Println()
		fmt.Println("NOTE: RunOS MCP cannot work within the Codex sandbox environment.")
		fmt.Println("      Adding the project as 'trusted' disables sandboxing for this project.")
		fmt.Println()
		fmt.Print("Type 'yes' to proceed: ")

		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "yes" {
			fmt.Println("Aborted. You must type 'yes' to proceed.")
			return nil
		}

		fmt.Println()
	}

	// Add MCP servers to .codex/config.toml in current directory
	if err := addMCPServersToCodexConfig(runosPath); err != nil {
		return fmt.Errorf("failed to configure MCP server: %w", err)
	}

	// Add rules to auto-allow read-only tools
	if err := addCodexRules(readToolNames); err != nil {
		return fmt.Errorf("failed to configure Codex rules: %w", err)
	}

	// Add project to trusted projects in global Codex config
	if err := addCodexTrustedProject(); err != nil {
		return fmt.Errorf("failed to configure trusted project: %w", err)
	}

	fmt.Println("\nRunOS MCP servers configured successfully!")
	fmt.Println("Codex now has access to RunOS tools in this project.")

	return nil
}

func addMCPServersToCodexConfig(runosPath string) error {
	codexDir := ".codex"
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		return fmt.Errorf("failed to create .codex directory: %w", err)
	}

	configPath := filepath.Join(codexDir, "config.toml")

	// Read existing config if it exists
	existingContent := ""
	if content, err := os.ReadFile(configPath); err == nil {
		existingContent = string(content)
	}

	// Check if runos servers are already configured
	if strings.Contains(existingContent, "[mcp_servers.runos]") {
		fmt.Printf("RunOS MCP servers already configured in %s\n", configPath)
		return nil
	}

	// Build TOML content for MCP servers
	mcpConfig := fmt.Sprintf(`
[mcp_servers.runos]
command = %q
args = ["mcp", "serve", "read"]

[mcp_servers.runos-sensitive-read]
command = %q
args = ["mcp", "serve", "sensitive-read"]

[mcp_servers.runos-write]
command = %q
args = ["mcp", "serve", "write"]

[mcp_servers.runos-sensitive-write]
command = %q
args = ["mcp", "serve", "sensitive-write"]
`, runosPath, runosPath, runosPath, runosPath)

	// Append to existing config or create new
	var newContent string
	if existingContent != "" {
		newContent = existingContent + "\n" + mcpConfig
	} else {
		newContent = strings.TrimPrefix(mcpConfig, "\n")
	}

	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Updated %s\n", filepath.Join(cwd, configPath))
	return nil
}

func addCodexRules(readToolNames []string) error {
	rulesDir := filepath.Join(".codex", "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return fmt.Errorf("failed to create rules directory: %w", err)
	}

	rulesPath := filepath.Join(rulesDir, "default.rules")

	// Read existing rules if they exist
	existingContent := ""
	if content, err := os.ReadFile(rulesPath); err == nil {
		existingContent = string(content)
	}

	// Check if runos rules are already configured
	if strings.Contains(existingContent, "RunOS read-only") {
		fmt.Printf("RunOS rules already configured in %s\n", rulesPath)
		return nil
	}

	// Build tool names array for the rule
	toolNamesFormatted := make([]string, len(readToolNames))
	for i, name := range readToolNames {
		toolNamesFormatted[i] = fmt.Sprintf("%q", name)
	}

	// Build rules content
	rulesContent := fmt.Sprintf(`
prefix_rule(
  pattern=["mcp__runos__", [%s]],
  decision="allow",
  justification="Allow RunOS read-only operations"
)
`, strings.Join(toolNamesFormatted, ", "))

	// Append to existing rules or create new
	var newContent string
	if existingContent != "" {
		newContent = existingContent + "\n" + rulesContent
	} else {
		newContent = strings.TrimPrefix(rulesContent, "\n")
	}

	if err := os.WriteFile(rulesPath, []byte(newContent), 0644); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Updated %s\n", filepath.Join(cwd, rulesPath))
	return nil
}

func addCodexTrustedProject() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// Get current working directory (the project path)
	projectPath, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Global Codex config at ~/.codex/config.toml
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		return fmt.Errorf("failed to create ~/.codex directory: %w", err)
	}

	configPath := filepath.Join(codexDir, "config.toml")

	// Read existing config if it exists
	existingContent := ""
	if content, err := os.ReadFile(configPath); err == nil {
		existingContent = string(content)
	}

	// Check if this project is already configured
	projectSection := fmt.Sprintf(`[projects.%q]`, projectPath)
	if strings.Contains(existingContent, projectSection) {
		fmt.Printf("Project already configured in %s\n", configPath)
		fmt.Println("Please ensure the following config appears in the file:")
		fmt.Println()
		fmt.Printf("%s\n", projectSection)
		fmt.Println(`trust_level = "trusted"`)
		return nil
	}

	// Build TOML content for trusted project
	trustConfig := fmt.Sprintf(`
[projects.%q]
trust_level = "trusted"
`, projectPath)

	// Append to existing config or create new
	var newContent string
	if existingContent != "" {
		newContent = existingContent + "\n" + trustConfig
	} else {
		newContent = strings.TrimPrefix(trustConfig, "\n")
	}

	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		return err
	}

	fmt.Printf("Updated %s (added project as trusted)\n", configPath)
	return nil
}
