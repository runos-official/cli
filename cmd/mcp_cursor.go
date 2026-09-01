package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

// cursorMCPServer describes one RunOS MCP server as Cursor sees it.
type cursorMCPServer struct {
	name     string
	serveArg string
	// risk is empty for the read server. A non-empty risk puts the server
	// behind the guard hook, and is the sentence the user and the agent read
	// when Cursor asks.
	risk string
}

// cursorMCPServers is the one list every part of this target works from.
// `.cursor/mcp.json` declares each entry, the guard asks about each entry that
// carries a risk, and the warning names each entry. One list means the guard
// can never disagree with the config about what a server is.
//
// The read server carries no risk because it performs no mutation. It is not
// free of secrets: on a CLI older than manifest 45.0.0 the grafana, litellm,
// langfuse, vector and clickhouse credentials commands still sit on the read
// tier, so that server can return a password. Manifest 45.0.0 moves those five
// to sensitive_read.
var cursorMCPServers = []cursorMCPServer{
	{name: "runos", serveArg: "read"},
	{
		name:     "runos-sensitive-read",
		serveArg: "sensitive-read",
		risk:     "returns credentials and connection strings, which become visible to the model",
	},
	{
		name:     "runos-write",
		serveArg: "write",
		risk:     "creates, updates and deletes live infrastructure",
	},
	{
		name:     "runos-sensitive-write",
		serveArg: "sensitive-write",
		risk:     "rotates credentials and secrets on live infrastructure",
	},
}

// cursorUnknownRunOSServer stands for a RunOS-looking server whose exact name is
// not one of the four above. It is NOT declared in mcp.json and is never written
// by `runos mcp configure cursor`.
//
// It exists because falling through to allow was a real bypass, reproduced against
// the generated hook script: `runos-writer`, `runos_write`, `runos-write ` with a
// trailing space, `RUNOS-WRITE` and `runos-write-prod` all returned allow. The last
// is a plausible real configuration, not a lab shape, because a second account or a
// copied and renamed entry produces exactly that. `runos mcp configure cursor`
// removes only the four names it knows, so it never cleans such an entry up either.
//
// Treated as the highest risk on purpose. The guard cannot know what a server it
// does not recognise will do, and asking about an unfamiliar RunOS server is a small
// cost against silently allowing a live write one.
var cursorUnknownRunOSServer = cursorMCPServer{
	name: "unrecognised RunOS server",
	risk: "is a RunOS server this guard does not recognise, so its risk is unknown and it may write to live infrastructure or return credentials",
}

// cursorGuardHookCommand is the command Cursor runs the guard by. Cursor runs a
// project hook from the project root, so the path is relative to that root and
// keeps forward slashes, which is what Cursor's own docs show.
const cursorGuardHookCommand = ".cursor/hooks/runos-guard.sh"

// cursorGOOS is runtime.GOOS, replaced by the test that pins the Windows
// behaviour. The guard is a bash script, so the platform decides whether the
// risky servers may be declared at all.
var cursorGOOS = runtime.GOOS

var mcpConfigureCursorCmd = &cobra.Command{
	Use:   "cursor",
	Short: "Configure the RunOS MCP servers for Cursor (project-level)",
	Long: `Add the RunOS MCP servers to the current project's .cursor/mcp.json configuration.

This creates or updates .cursor/mcp.json in the current directory, scoping the RunOS
tools to this project only. It also writes .cursor/hooks.json and a guard script, so
Cursor asks before every call to the write and sensitive servers.

Cursor has no per-server switch in mcp.json, so all four servers load. Use --read-only
to declare the read server alone.`,
	RunE: runMCPConfigureCursor,
}

// The cursor target registers itself here because cmd/mcp.go is already over
// the repo's file-size budget and must not grow.
func init() {
	mcpConfigureCmd.AddCommand(mcpConfigureCursorCmd)
	mcpConfigureCursorCmd.Flags().BoolP("yes", "y", false, "Skip the warning and confirmation prompt")
	mcpConfigureCursorCmd.Flags().Bool("read-only", false, "Declare only the read server, so the write and sensitive servers never load")
	mcpCmd.AddCommand(mcpCursorGuardCmd)
}

// cursorOptions is what the command decided, separated from cobra so the tests
// drive the whole flow rather than the two writers.
type cursorOptions struct {
	// readOnly declares the read server alone and removes the other three if
	// an earlier run declared them. Cursor has no per-server switch in
	// mcp.json, so this is the only way to stop the risky servers loading.
	readOnly bool
	// yes skips the confirmation. The confirmation is what tells the user
	// that three servers they did not name are about to start loading.
	yes bool
	in  io.Reader
	out io.Writer
}

func runMCPConfigureCursor(cmd *cobra.Command, args []string) error {
	readOnly, _ := cmd.Flags().GetBool("read-only")
	yes, _ := cmd.Flags().GetBool("yes")
	return configureCursor(cursorOptions{
		readOnly: readOnly,
		yes:      yes,
		in:       cmd.InOrStdin(),
		out:      cmd.OutOrStdout(),
	})
}

// configureCursor converges the project on the whole desired state every run:
// the server declarations, the hook registration and the guard script. It never
// skips on the strength of one of the three, because a project whose mcp.json
// names a runos server and whose guard has been deleted is exactly the project
// that needs repairing, and the version that skipped it reported success.
func configureCursor(opts cursorOptions) error {
	if opts.in == nil {
		opts.in = os.Stdin
	}
	if opts.out == nil {
		opts.out = os.Stdout
	}

	// The guard is a bash script. Windows cannot run it, and Cursor lets a
	// hook that fails allow the call through, so declaring the risky servers
	// there would leave them with no brake at all.
	guardRuns := cursorGOOS != "windows"
	if !guardRuns && !opts.readOnly {
		return fmt.Errorf("the guard hook is a bash script and %s cannot run it, so the write and sensitive servers must not be declared here. "+
			"Run `runos mcp configure cursor --read-only` to declare the read server alone", cursorGOOS)
	}

	servers := cursorMCPServers
	if opts.readOnly {
		servers = servers[:1]
	}

	runosPath, err := cursorRunosPath()
	if err != nil {
		return err
	}

	// Both files are parsed BEFORE either is written. The version before this
	// wrote mcp.json first and found a malformed hooks.json second, which left
	// the project declaring four servers with no guard registered, and the
	// error named neither fact.
	mcpConfig, err := loadCursorJSON(cursorConfigPath())
	if err != nil {
		return err
	}
	hooksConfig := map[string]any{}
	if guardRuns {
		if hooksConfig, err = loadCursorJSON(cursorHooksPath()); err != nil {
			return err
		}
	}

	// Read-only declares nothing that can change anything, so it needs no
	// confirmation.
	if !opts.yes && !opts.readOnly {
		confirmed, err := confirmCursorWrite(opts)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(opts.out, "Aborted. Nothing was written.")
			return nil
		}
	}

	fmt.Fprintln(opts.out, "Configuring the RunOS MCP servers for Cursor (project-level)...")

	changed := 0
	declarations, err := cursorJSONBytes(buildCursorMCPConfig(mcpConfig, runosPath, servers))
	if err != nil {
		return err
	}
	written, err := writeCursorFile(opts.out, cursorConfigPath(), declarations, 0o644)
	if err != nil {
		return err
	}
	changed += written

	if guardRuns {
		written, err = writeCursorFile(opts.out, cursorGuardPath(), []byte(cursorGuardScript(runosPath)), 0o755)
		if err != nil {
			return err
		}
		changed += written

		registration, err := cursorJSONBytes(buildCursorHooksConfig(hooksConfig))
		if err != nil {
			return err
		}
		written, err = writeCursorFile(opts.out, cursorHooksPath(), registration, 0o644)
		if err != nil {
			return err
		}
		changed += written
	}

	reportCursorResult(opts, changed, guardRuns)
	return nil
}

func cursorConfigPath() string { return filepath.Join(".cursor", "mcp.json") }
func cursorHooksPath() string  { return filepath.Join(".cursor", "hooks.json") }
func cursorGuardPath() string  { return filepath.Join(".cursor", "hooks", "runos-guard.sh") }

func cursorRunosPath() (string, error) {
	runosPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}
	runosPath, err = filepath.EvalSymlinks(runosPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve runos path: %w", err)
	}
	return runosPath, nil
}

// loadCursorJSON reads one of the project's JSON files. A file that is not
// there reads as an empty object, so a fresh project needs no special case.
//
// `null` is valid JSON and unmarshals into a NIL map, and writing into a nil
// map panics. That is how this used to end, with a Go stack trace on the user's
// terminal and a half-configured project.
func loadCursorJSON(path string) (map[string]any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w. Fix that file or move it aside, then run this again. Nothing was written", path, err)
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

// buildCursorMCPConfig returns the .cursor/mcp.json the project should have.
// Servers another tool put there are kept. RunOS servers this run does not
// declare are removed, because --read-only asking for the read server alone
// means nothing if the other three stay declared from an earlier run.
func buildCursorMCPConfig(existing map[string]any, runosPath string, servers []cursorMCPServer) map[string]any {
	// A missing mcpServers, or one holding something that is not an object,
	// starts over. Cursor cannot read either shape, so there is nothing to
	// preserve.
	mcpServers, ok := existing["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = map[string]any{}
	}

	declared := make(map[string]bool, len(servers))
	for _, server := range servers {
		mcpServers[server.name] = map[string]any{
			"type":    "stdio",
			"command": runosPath,
			"args":    []string{"mcp", "serve", server.serveArg},
			"env":     map[string]any{},
		}
		declared[server.name] = true
	}
	for _, server := range cursorMCPServers {
		if !declared[server.name] {
			delete(mcpServers, server.name)
		}
	}

	existing["mcpServers"] = mcpServers
	return existing
}

// buildCursorHooksConfig returns the .cursor/hooks.json the project should
// have. Hooks another tool put there are kept, and the guard is registered
// once however often this runs.
func buildCursorHooksConfig(existing map[string]any) map[string]any {
	// Version 1 is the only version Cursor reads. A file that already carries
	// one keeps it, because the rest of that file belongs to somebody else.
	if _, ok := existing["version"]; !ok {
		existing["version"] = 1
	}

	hooks, ok := existing["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}

	entries, _ := hooks["beforeMCPExecution"].([]any)
	registered := false
	for _, entry := range entries {
		hook, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if command, _ := hook["command"].(string); command == cursorGuardHookCommand {
			// Repair an entry an older version of this command wrote
			// without failClosed.
			hook["failClosed"] = true
			registered = true
			break
		}
	}
	if !registered {
		entries = append(entries, map[string]any{
			// Cursor lets a hook that crashes, times out or prints invalid
			// JSON allow the action through. This hook is the only brake on
			// the write servers, so it blocks instead.
			"command":    cursorGuardHookCommand,
			"failClosed": true,
		})
	}

	hooks["beforeMCPExecution"] = entries
	existing["hooks"] = hooks
	return existing
}

// cursorJSONBytes renders one config file. Every value in these maps came out
// of encoding/json or was put there by this file, so a failure here is not
// expected. It is still returned rather than panicked: a stack trace on the
// user's terminal is never the right way to report a config problem.
func cursorJSONBytes(data map[string]any) ([]byte, error) {
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to render the config: %w", err)
	}
	return append(content, '\n'), nil
}

// writeCursorFile writes content only when the file differs, and reports
// whether it wrote. A run that changes nothing has to say so, because the user
// re-runs this command to find out whether it took.
func writeCursorFile(out io.Writer, path string, content []byte, mode os.FileMode) (int, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm() == mode {
			fmt.Fprintf(out, "  unchanged  %s\n", cursorDisplayPath(path))
			return 0, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return 0, fmt.Errorf("failed to write %s: %w", path, err)
	}
	// WriteFile leaves an existing file's mode alone, so the guard script
	// stays non-executable if something stripped the bit.
	if err := os.Chmod(path, mode); err != nil {
		return 0, fmt.Errorf("failed to set the mode on %s: %w", path, err)
	}
	fmt.Fprintf(out, "  wrote      %s\n", cursorDisplayPath(path))
	return 1, nil
}

func cursorDisplayPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	return filepath.Join(cwd, path)
}
