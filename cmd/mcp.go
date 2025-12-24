package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"cli/internal/config"
	"cli/internal/manifest"
	"cli/internal/mcp"

	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run MCP server for AI assistant integration",
	Long:  `Run the Model Context Protocol (MCP) server on stdio for integration with AI assistants like Claude Code.`,
	RunE:  runMCP,
}

func runMCP(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(home, ".runos")

	loader := manifest.NewLoader(cfg.GetConductorURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	executor := mcp.NewCommandExecutor(m, cfg.GetConductorURL())
	server := mcp.NewServer(m, executor, Version)

	return server.Run()
}
