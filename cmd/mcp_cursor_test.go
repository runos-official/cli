package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cursor target writes into a project a user already owns, so every case
// below starts from a different state of that project. Two failures matter.
// Merging wrongly silently deletes another tool's MCP server or another tool's
// hook. Converging wrongly leaves the project with four servers loading and no
// guard, which is what the version before this one did whenever .cursor/mcp.json
// already named a runos server.

const cursorTestRunosPath = "/opt/runos/bin/runos"

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return data
}

// cursorTestOptions runs the command with the confirmation already answered, so
// a case that is not about the confirmation does not have to feed it.
func cursorTestOptions() cursorOptions {
	return cursorOptions{yes: true, in: strings.NewReader(""), out: io.Discard}
}

func serverNames(t *testing.T, data map[string]any) []string {
	t.Helper()
	servers, ok := data["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing, got %v", data)
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	return names
}

func hasServer(t *testing.T, data map[string]any, name string) bool {
	t.Helper()
	for _, got := range serverNames(t, data) {
		if got == name {
			return true
		}
	}
	return false
}

func TestBuildCursorMCPConfig(t *testing.T) {
	unrelated := map[string]any{
		"mcpServers": map[string]any{
			"weather": map[string]any{"type": "stdio", "command": "npx"},
		},
	}

	cases := []struct {
		name     string
		existing map[string]any
		servers  []cursorMCPServer
		check    func(t *testing.T, data map[string]any)
	}{
		{
			name:     "a fresh project declares all four servers",
			existing: map[string]any{},
			servers:  cursorMCPServers,
			check: func(t *testing.T, data map[string]any) {
				servers, _ := data["mcpServers"].(map[string]any)
				if len(servers) != 4 {
					t.Errorf("declared %d servers, want 4: %v", len(servers), servers)
				}
				for _, want := range cursorMCPServers {
					server, ok := servers[want.name].(map[string]any)
					if !ok {
						t.Errorf("server %q missing", want.name)
						continue
					}
					// Cursor requires `type` on a stdio entry.
					if server["type"] != "stdio" {
						t.Errorf("%s type = %v, want stdio", want.name, server["type"])
					}
					if server["command"] != cursorTestRunosPath {
						t.Errorf("%s command = %v, want %s", want.name, server["command"], cursorTestRunosPath)
					}
					args, _ := server["args"].([]string)
					if len(args) != 3 || args[2] != want.serveArg {
						t.Errorf("%s args = %v, want [mcp serve %s]", want.name, args, want.serveArg)
					}
				}
			},
		},
		{
			name:     "a server another tool put there survives",
			existing: unrelated,
			servers:  cursorMCPServers,
			check: func(t *testing.T, data map[string]any) {
				servers, _ := data["mcpServers"].(map[string]any)
				weather, ok := servers["weather"].(map[string]any)
				if !ok {
					t.Fatalf("the project's own weather server was deleted: %v", servers)
				}
				if weather["command"] != "npx" {
					t.Errorf("weather command = %v, want npx", weather["command"])
				}
				if len(servers) != 5 {
					t.Errorf("declared %d servers, want 5 (weather plus four RunOS)", len(servers))
				}
			},
		},
		{
			name:     "read-only declares the read server alone",
			existing: map[string]any{},
			servers:  cursorMCPServers[:1],
			check: func(t *testing.T, data map[string]any) {
				servers, _ := data["mcpServers"].(map[string]any)
				if len(servers) != 1 {
					t.Errorf("declared %d servers, want 1: %v", len(servers), servers)
				}
				if _, ok := servers["runos"]; !ok {
					t.Errorf("the read server is missing: %v", servers)
				}
			},
		},
		{
			// --read-only is worth nothing if servers an earlier run
			// declared stay declared. Cursor loads whatever is in the file.
			name: "read-only removes the risky servers an earlier run declared",
			existing: map[string]any{
				"mcpServers": map[string]any{
					"weather":               map[string]any{"command": "npx"},
					"runos":                 map[string]any{"command": "old"},
					"runos-write":           map[string]any{"command": "old"},
					"runos-sensitive-read":  map[string]any{"command": "old"},
					"runos-sensitive-write": map[string]any{"command": "old"},
				},
			},
			servers: cursorMCPServers[:1],
			check: func(t *testing.T, data map[string]any) {
				for _, gone := range []string{"runos-write", "runos-sensitive-read", "runos-sensitive-write"} {
					if hasServer(t, data, gone) {
						t.Errorf("%s is still declared, so Cursor still loads it", gone)
					}
				}
				if !hasServer(t, data, "weather") {
					t.Error("the project's own weather server was deleted")
				}
				if !hasServer(t, data, "runos") {
					t.Error("the read server is missing")
				}
			},
		},
		{
			// A committed .cursor/mcp.json carries the path from whoever
			// ran the command first, which is not this machine's path.
			name: "a stale binary path is refreshed",
			existing: map[string]any{
				"mcpServers": map[string]any{
					"runos": map[string]any{"type": "stdio", "command": "/somebody/elses/runos"},
				},
			},
			servers: cursorMCPServers,
			check: func(t *testing.T, data map[string]any) {
				servers, _ := data["mcpServers"].(map[string]any)
				runos, _ := servers["runos"].(map[string]any)
				if runos["command"] != cursorTestRunosPath {
					t.Errorf("runos command = %v, want %s", runos["command"], cursorTestRunosPath)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, buildCursorMCPConfig(tc.existing, cursorTestRunosPath, tc.servers))
		})
	}
}

func TestBuildCursorHooksConfig(t *testing.T) {
	guardEntry := func(t *testing.T, data map[string]any) map[string]any {
		t.Helper()
		hooks, ok := data["hooks"].(map[string]any)
		if !ok {
			t.Fatalf("hooks missing, got %v", data)
		}
		entries, ok := hooks["beforeMCPExecution"].([]any)
		if !ok {
			t.Fatalf("beforeMCPExecution missing, got %v", hooks)
		}
		for _, entry := range entries {
			hook, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if hook["command"] == cursorGuardHookCommand {
				return hook
			}
		}
		t.Fatalf("the guard is not registered: %v", entries)
		return nil
	}

	t.Run("a fresh project registers the guard at version 1", func(t *testing.T) {
		data := buildCursorHooksConfig(map[string]any{})
		if data["version"] != 1 {
			t.Errorf("version = %v, want 1", data["version"])
		}
		guardEntry(t, data)
	})

	t.Run("the guard is registered failClosed", func(t *testing.T) {
		// Cursor lets a hook that crashes, times out or prints invalid JSON
		// allow the call through. This hook is the only brake on the write
		// servers, so it has to block instead.
		if guardEntry(t, buildCursorHooksConfig(map[string]any{}))["failClosed"] != true {
			t.Error("the guard is registered fail-open, so a broken guard allows every write call")
		}
	})

	t.Run("an entry an older version wrote gains failClosed", func(t *testing.T) {
		existing := map[string]any{
			"version": float64(1),
			"hooks": map[string]any{
				"beforeMCPExecution": []any{
					map[string]any{"command": cursorGuardHookCommand},
				},
			},
		}
		if guardEntry(t, buildCursorHooksConfig(existing))["failClosed"] != true {
			t.Error("an existing guard entry was left fail-open")
		}
	})

	t.Run("hooks another tool put there survive", func(t *testing.T) {
		existing := map[string]any{
			"version": float64(1),
			"hooks": map[string]any{
				"beforeShellExecution": []any{map[string]any{"command": ".cursor/hooks/audit.sh"}},
				"beforeMCPExecution":   []any{map[string]any{"command": ".cursor/hooks/other-guard.sh"}},
			},
		}
		data := buildCursorHooksConfig(existing)
		hooks, _ := data["hooks"].(map[string]any)
		if shell, _ := hooks["beforeShellExecution"].([]any); len(shell) != 1 {
			t.Errorf("the project's own beforeShellExecution hook was deleted: %v", hooks)
		}
		entries, _ := hooks["beforeMCPExecution"].([]any)
		if len(entries) != 2 {
			t.Fatalf("beforeMCPExecution = %v, want the project's own hook plus ours", entries)
		}
		first, _ := entries[0].(map[string]any)
		if first["command"] != ".cursor/hooks/other-guard.sh" {
			t.Errorf("the project's own MCP hook was replaced: %v", entries)
		}
		guardEntry(t, data)
	})

	t.Run("the guard is registered once, not twice", func(t *testing.T) {
		data := buildCursorHooksConfig(map[string]any{})
		data = buildCursorHooksConfig(data)
		hooks, _ := data["hooks"].(map[string]any)
		entries, _ := hooks["beforeMCPExecution"].([]any)
		if len(entries) != 1 {
			t.Errorf("beforeMCPExecution = %v, want our guard exactly once", entries)
		}
	})
}

// The reviewer's reproduction: configure a project, delete the guard, run
// again. The version before this printed "already configured, skipping", exited
// 0, and left the project with four servers loading and no guard at all.
func TestConfigureCursor_RepairsADeletedGuard(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := os.Remove(cursorHooksPath()); err != nil {
		t.Fatalf("delete hooks.json: %v", err)
	}
	if err := os.Remove(cursorGuardPath()); err != nil {
		t.Fatalf("delete the guard: %v", err)
	}

	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("the repair run: %v", err)
	}

	if _, err := os.Stat(cursorGuardPath()); err != nil {
		t.Errorf("the guard script was not restored: %v", err)
	}
	hooks := readJSONFile(t, cursorHooksPath())
	registered, _ := hooks["hooks"].(map[string]any)
	if entries, _ := registered["beforeMCPExecution"].([]any); len(entries) != 1 {
		t.Errorf("beforeMCPExecution = %v, want the guard back", entries)
	}
}

// The reviewer's second reproduction, in two halves. A malformed hooks.json
// used to be found only AFTER mcp.json had declared four servers.
func TestConfigureCursor_AMalformedHooksFileWritesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cursorHooksPath(), []byte("NOT JSON"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := configureCursor(cursorTestOptions())
	if err == nil {
		t.Fatal("a malformed .cursor/hooks.json must be reported, not worked around")
	}
	if !strings.Contains(err.Error(), "hooks.json") {
		t.Errorf("error %q does not name the file the user has to fix", err)
	}
	if _, statErr := os.Stat(cursorConfigPath()); statErr == nil {
		t.Error("mcp.json was written anyway, so the project now loads servers with no guard")
	}
	if _, statErr := os.Stat(cursorGuardPath()); statErr == nil {
		t.Error("the guard script was written before the failure")
	}
	after, readErr := os.ReadFile(cursorHooksPath())
	if readErr != nil || string(after) != "NOT JSON" {
		t.Errorf("the malformed file was rewritten: %q %v", after, readErr)
	}
}

func TestConfigureCursor_FixingTheHooksFileThenRerunningRepairs(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cursorHooksPath(), []byte("NOT JSON"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := configureCursor(cursorTestOptions()); err == nil {
		t.Fatal("the malformed file should have stopped the run")
	}

	// The user fixes the file exactly as the error asked.
	if err := os.WriteFile(cursorHooksPath(), []byte("{}"), 0o644); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("the repair run: %v", err)
	}

	hooks := readJSONFile(t, cursorHooksPath())
	registered, _ := hooks["hooks"].(map[string]any)
	if entries, _ := registered["beforeMCPExecution"].([]any); len(entries) != 1 {
		t.Errorf("beforeMCPExecution = %v, want the guard registered", entries)
	}
}

// A project that already names a runos server, from a hand-written config or a
// teammate's commit, still gets the guard.
func TestConfigureCursor_AHandWrittenRunosEntryStillGetsTheGuard(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"mcpServers":{"runos":{"type":"stdio","command":"/somebody/elses/runos","args":["mcp","serve","read"]}}}`
	if err := os.WriteFile(cursorConfigPath(), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(cursorGuardPath()); err != nil {
		t.Errorf("no guard was written for a project that already named runos: %v", err)
	}
	config := readJSONFile(t, cursorConfigPath())
	servers, _ := config["mcpServers"].(map[string]any)
	runos, _ := servers["runos"].(map[string]any)
	if runos["command"] == "/somebody/elses/runos" {
		t.Error("the stale binary path from another machine was left in place")
	}
}

func TestConfigureCursor_SecondRunChangesNothing(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before := map[string][]byte{}
	for _, path := range []string{cursorConfigPath(), cursorHooksPath(), cursorGuardPath()} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		before[path] = content
	}

	var report strings.Builder
	opts := cursorTestOptions()
	opts.out = &report
	if err := configureCursor(opts); err != nil {
		t.Fatalf("second run: %v", err)
	}

	for path, content := range before {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(after) != string(content) {
			t.Errorf("the second run changed %s:\nbefore:\n%s\nafter:\n%s", path, content, after)
		}
	}
	// The user re-runs this command to find out whether it took, so a run
	// that changed nothing has to say so.
	if !strings.Contains(report.String(), "Already up to date") {
		t.Errorf("the second run did not report that nothing changed:\n%s", report.String())
	}
}

// `null` is valid JSON, so it parses, and it unmarshals into a NIL map. Writing
// into that map used to panic with a Go stack trace on the user's terminal.
func TestLoadCursorJSON_NonObjectJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "the JSON literal null reads as an empty object", content: "null"},
		{name: "an array is reported", content: "[]", wantErr: true},
		{name: "a string is reported", content: `"x"`, wantErr: true},
		{name: "a number is reported", content: "123", wantErr: true},
		{name: "an empty file is reported", content: "", wantErr: true},
		{name: "a malformed file is reported", content: "{ this is not json", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.MkdirAll(".cursor", 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(cursorConfigPath(), []byte(tc.content), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}

			data, err := loadCursorJSON(cursorConfigPath())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%q was accepted", tc.content)
				}
				if !strings.Contains(err.Error(), "mcp.json") {
					t.Errorf("error %q does not name the file the user has to fix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q was rejected: %v", tc.content, err)
			}
			if data == nil {
				t.Fatal("a nil map was returned, and writing into one panics")
			}
		})
	}
}

func TestConfigureCursor_ANullConfigDoesNotPanic(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cursorConfigPath(), []byte("null"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(cursorHooksPath(), []byte("null"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(serverNames(t, readJSONFile(t, cursorConfigPath()))) != 4 {
		t.Error("the four servers were not declared")
	}
}

// The guard is a bash script. Windows cannot run it, and Cursor lets a hook
// that fails allow the call through, so the risky servers must not be declared
// there at all.
func TestConfigureCursor_WindowsRefusesTheRiskyServers(t *testing.T) {
	t.Chdir(t.TempDir())
	original := cursorGOOS
	cursorGOOS = "windows"
	t.Cleanup(func() { cursorGOOS = original })

	err := configureCursor(cursorTestOptions())
	if err == nil {
		t.Fatal("the four-server config was written on a platform that cannot run the guard")
	}
	if !strings.Contains(err.Error(), "--read-only") {
		t.Errorf("error %q does not name the one command that works here", err)
	}
	if _, statErr := os.Stat(cursorConfigPath()); statErr == nil {
		t.Error("mcp.json was written anyway")
	}
}

func TestConfigureCursor_WindowsAllowsReadOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	original := cursorGOOS
	cursorGOOS = "windows"
	t.Cleanup(func() { cursorGOOS = original })

	opts := cursorTestOptions()
	opts.readOnly = true
	if err := configureCursor(opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	names := serverNames(t, readJSONFile(t, cursorConfigPath()))
	if len(names) != 1 || names[0] != "runos" {
		t.Errorf("declared %v, want the read server alone", names)
	}
	if _, err := os.Stat(cursorHooksPath()); err == nil {
		t.Error("a hooks.json was written for a guard that cannot run")
	}
}

// The confirmation is the one place the user is told that three servers they
// did not name are about to start loading.
func TestConfigureCursor_TheConfirmationGatesTheWrite(t *testing.T) {
	cases := []struct {
		name    string
		typed   string
		written bool
	}{
		{name: "yes proceeds", typed: "yes\n", written: true},
		{name: "yes with no trailing newline proceeds", typed: "yes", written: true},
		{name: "no aborts", typed: "no\n"},
		{name: "an empty answer aborts", typed: "\n"},
		{name: "a closed stdin aborts", typed: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var report strings.Builder
			opts := cursorOptions{in: strings.NewReader(tc.typed), out: &report}

			if err := configureCursor(opts); err != nil {
				t.Fatalf("run: %v", err)
			}
			_, err := os.Stat(cursorConfigPath())
			if tc.written && err != nil {
				t.Errorf("nothing was written after the user typed yes: %v", err)
			}
			if !tc.written && err == nil {
				t.Error("the config was written without a confirmation")
			}
			if !tc.written && !strings.Contains(report.String(), "Aborted") {
				t.Errorf("the abort was not reported:\n%s", report.String())
			}
		})
	}
}

func TestConfigureCursor_TheWarningNamesEveryRiskyServer(t *testing.T) {
	t.Chdir(t.TempDir())
	var report strings.Builder
	opts := cursorOptions{in: strings.NewReader("no\n"), out: &report}
	if err := configureCursor(opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, server := range cursorMCPServers {
		if !strings.Contains(report.String(), server.name) {
			t.Errorf("the warning does not name %s", server.name)
		}
		if server.risk != "" && !strings.Contains(report.String(), server.risk) {
			t.Errorf("the warning does not say what %s does", server.name)
		}
	}
	if !strings.Contains(report.String(), "failClosed") {
		t.Error("the warning does not say a broken guard blocks every MCP call in the project")
	}
}

// Something can strip the executable bit: a checkout on a filesystem that drops
// it, or an over-eager umask. Cursor then cannot run the guard.
func TestConfigureCursor_RestoresTheGuardExecutableBit(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := os.Chmod(cursorGuardPath(), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("the repair run: %v", err)
	}

	info, err := os.Stat(cursorGuardPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("guard mode %v is not executable", info.Mode().Perm())
	}
}

// Cursor runs the registered command from the project root, so the path
// registered in hooks.json has to be the path the file is written to.
func TestConfigureCursor_TheRegisteredPathIsTheWrittenPath(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := configureCursor(cursorTestOptions()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(filepath.FromSlash(cursorGuardHookCommand)); err != nil {
		t.Errorf("the registered guard is not at the path Cursor will run: %v", err)
	}
}
