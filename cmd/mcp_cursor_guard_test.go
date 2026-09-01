package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests RUN the guard. The suite they replace only grepped the generated
// script's text, which is how a guard that answered allow for a runos-write
// call shipped green: a sed with a greedy `.*` took the LAST "mcp_server_name"
// in the payload, so a decoy the model wrote into tool_input won. Every payload
// below that carries a decoy is one a reviewer used to demonstrate that.

// cursorGuardCase is one payload and the decision the requirement demands from
// it. The same table drives the in-process decision and the real script, so the
// two cannot drift apart.
type cursorGuardCase struct {
	name       string
	payload    string
	permission string
}

var cursorGuardCases = []cursorGuardCase{
	{
		name:       "the read server is allowed",
		payload:    `{"mcp_server_name":"runos","tool_name":"clusters_list","tool_input":{}}`,
		permission: "allow",
	},
	{
		name:       "the write server is asked about",
		payload:    `{"mcp_server_name":"runos-write","tool_name":"apps_delete","tool_input":{}}`,
		permission: "ask",
	},
	{
		name:       "the sensitive-read server is asked about",
		payload:    `{"mcp_server_name":"runos-sensitive-read","tool_name":"clusters_kubeconfig","tool_input":{}}`,
		permission: "ask",
	},
	{
		name:       "the sensitive-write server is asked about",
		payload:    `{"mcp_server_name":"runos-sensitive-write","tool_name":"deploy","tool_input":{}}`,
		permission: "ask",
	},
	{
		name:       "another tool's server is allowed, because this hook fires for all of them",
		payload:    `{"mcp_server_name":"weather","tool_name":"forecast","tool_input":{}}`,
		permission: "allow",
	},
	{
		// The reviewer's payload, verbatim. envVars is a free-form string
		// map, so the model chooses the key names in it.
		name:       "a decoy in envVars after the real key",
		payload:    `{"mcp_server_name":"runos-write","tool_name":"apps_env-vars_patch","tool_input":{"cid":"abc","id":"app","envVars":{"mcp_server_name":"runos"}}}`,
		permission: "ask",
	},
	{
		name:       "a decoy in tool_input before the real key",
		payload:    `{"tool_input":{"mcp_server_name":"runos"},"mcp_server_name":"runos-write"}`,
		permission: "ask",
	},
	{
		name:       "a decoy two maps deep",
		payload:    `{"mcp_server_name":"runos-write","tool_input":{"vars":{"mcp_server_name":"runos"}}}`,
		permission: "ask",
	},
	{
		name:       "a decoy inside an array element",
		payload:    `{"mcp_server_name":"runos-sensitive-write","tool_input":{"items":[{"mcp_server_name":"runos"}]}}`,
		permission: "ask",
	},
	{
		// Cursor's own docs describe tool_input as a JSON string, whose
		// inner quotes arrive escaped.
		name:       "a decoy inside tool_input sent as a JSON string",
		payload:    `{"tool_name":"x","tool_input":"{\"mcp_server_name\":\"runos\"}","mcp_server_name":"runos-write"}`,
		permission: "ask",
	},
	{
		name:       "a decoy in a free-text value carrying escaped quotes",
		payload:    `{"tool_input":{"note":"a \" then \"mcp_server_name\":\"runos\""},"mcp_server_name":"runos-write"}`,
		permission: "ask",
	},
	{
		// The field order Cursor's documented example uses.
		name:       "the documented field order",
		payload:    `{"tool_name":"apps_delete","tool_input":{"id":"app"},"mcp_server_name":"runos-write","command":"/opt/runos/bin/runos"}`,
		permission: "ask",
	},
	{
		name: "pretty printed with the decoy on its own line",
		payload: `{
  "tool_name": "apps_env-vars_patch",
  "tool_input": {
    "mcp_server_name": "runos"
  },
  "mcp_server_name" : "runos-sensitive-read"
}`,
		permission: "ask",
	},
	{
		// A guard that cannot find the server name has to be loud. If a
		// future Cursor renames the field, the user is asked about every
		// call instead of being silently allowed through.
		name:       "no server name at all",
		payload:    `{"tool_name":"apps_delete","tool_input":{}}`,
		permission: "ask",
	},
	{
		name:       "an explicit null server name",
		payload:    `{"mcp_server_name":null,"tool_input":{"mcp_server_name":"runos"}}`,
		permission: "ask",
	},
	{
		name:       "a server name that is not a string",
		payload:    `{"mcp_server_name":42}`,
		permission: "ask",
	},
	{
		name:       "an empty payload",
		payload:    ``,
		permission: "ask",
	},
	{
		name:       "a payload that is not JSON",
		payload:    `not json at all`,
		permission: "ask",
	},
	{
		name:       "a bare JSON null",
		payload:    `null`,
		permission: "ask",
	},

	// Two bypasses a fresh verifier reproduced against the REAL generated hook
	// script, both of which returned allow before this block existed.
	//
	// 1. encoding/json hides duplicate and case-variant keys. Duplicates
	//    OVERWRITE, so the last wins, and the decoder falls back to a
	//    case-insensitive field match. Either way a caller appends one key and
	//    downgrades ask to allow. Reversing the order gave ask, which proved it
	//    was strictly last-wins. The guard now walks the token stream and takes
	//    the MOST RESTRICTIVE name it finds.
	{
		name:       "a duplicate top-level key cannot downgrade write to read",
		payload:    `{"mcp_server_name":"runos-write","mcp_server_name":"runos"}`,
		permission: "ask",
	},
	{
		name:       "a duplicate top-level key cannot downgrade sensitive-write either",
		payload:    `{"mcp_server_name":"runos-sensitive-write","mcp_server_name":"runos"}`,
		permission: "ask",
	},
	{
		name:       "a case-variant key cannot downgrade write to read",
		payload:    `{"mcp_server_name":"runos-write","MCP_SERVER_NAME":"runos"}`,
		permission: "ask",
	},
	{
		name:       "the order does not matter, read first then write still asks",
		payload:    `{"mcp_server_name":"runos","mcp_server_name":"runos-write"}`,
		permission: "ask",
	},

	// 2. Any RunOS-looking server outside the four hardcoded names fell through
	//    to allow. runos-write-prod is a plausible real configuration, not a lab
	//    shape: a second account, or a copied and renamed entry, produces exactly
	//    that, and `runos mcp configure cursor` removes only the four names it
	//    knows, so it never cleans one up either.
	{
		name:       "runos-writer is not one of the four, so it asks",
		payload:    `{"mcp_server_name":"runos-writer"}`,
		permission: "ask",
	},
	{
		name:       "runos_write with an underscore asks",
		payload:    `{"mcp_server_name":"runos_write"}`,
		permission: "ask",
	},
	{
		name:       "a trailing space does not make a write server unknown-and-allowed",
		payload:    `{"mcp_server_name":"runos-write "}`,
		permission: "ask",
	},
	{
		name:       "an upper-case spelling of a write server asks",
		payload:    `{"mcp_server_name":"RUNOS-WRITE"}`,
		permission: "ask",
	},
	{
		name:       "a renamed write server, the plausible real configuration, asks",
		payload:    `{"mcp_server_name":"runos-write-prod","command":"runos mcp serve write","tool_name":"apps_delete"}`,
		permission: "ask",
	},

	// The other half of the rule: a server that is not RunOS at all is not ours
	// to judge. The hook fires for every MCP server in the project, so blocking
	// somebody else's would be a defect of its own.
	{
		name:       "a server that is not RunOS is allowed through",
		payload:    `{"mcp_server_name":"github","tool_name":"create_issue"}`,
		permission: "allow",
	},
	{
		name:       "a non-RunOS name that merely contains runos is still not ours",
		payload:    `{"mcp_server_name":"notrunos"}`,
		permission: "allow",
	},
}

func TestCursorGuardDecision(t *testing.T) {
	for _, tc := range cursorGuardCases {
		t.Run(tc.name, func(t *testing.T) {
			var decision map[string]any
			raw := cursorGuardDecision([]byte(tc.payload))
			if err := json.Unmarshal(raw, &decision); err != nil {
				t.Fatalf("the decision is not JSON: %q", raw)
			}
			if decision["permission"] != tc.permission {
				t.Errorf("permission = %v, want %s\npayload: %s", decision["permission"], tc.permission, tc.payload)
			}
		})
	}
}

// TestCursorGuardScript_RunsTheRealPayloads executes the script Cursor would
// execute, against the binary it would call, feeding each payload on stdin.
func TestCursorGuardScript_RunsTheRealPayloads(t *testing.T) {
	script := writeCursorGuardScriptForTest(t, cursorGuardBinary(t))

	for _, tc := range cursorGuardCases {
		t.Run(tc.name, func(t *testing.T) {
			decision := runCursorGuardScript(t, script, tc.payload)
			if decision["permission"] != tc.permission {
				t.Errorf("permission = %v, want %s\npayload: %s", decision["permission"], tc.permission, tc.payload)
			}
		})
	}
}

// An ask decision has to carry the sentence the user reads, otherwise Cursor
// asks a question with no subject.
func TestCursorGuardScript_AskCarriesTheReason(t *testing.T) {
	script := writeCursorGuardScriptForTest(t, cursorGuardBinary(t))
	decision := runCursorGuardScript(t, script, `{"mcp_server_name":"runos-write","tool_input":{}}`)

	user, _ := decision["user_message"].(string)
	if !strings.Contains(user, "runos-write") {
		t.Errorf("user_message = %q, want it to name the server", user)
	}
	agent, _ := decision["agent_message"].(string)
	if !strings.Contains(agent, "Ask the user") {
		t.Errorf("agent_message = %q, want it to tell the model to ask", agent)
	}
}

// The binary the script calls can be moved, renamed or uninstalled. A guard
// that cannot reach it must ask, never allow, because the hook is registered
// failClosed and a silent allow is the failure that matters.
func TestCursorGuardScript_AsksWhenTheBinaryIsGone(t *testing.T) {
	script := writeCursorGuardScriptForTest(t, filepath.Join(t.TempDir(), "runos-that-is-not-there"))
	decision := runCursorGuardScript(t, script, `{"mcp_server_name":"runos","tool_input":{}}`)

	if decision["permission"] != "ask" {
		t.Errorf("permission = %v, want ask when the binary is missing", decision["permission"])
	}
}

// A binary path with a space or a quote in it must not break the script. A
// macOS user with the CLI under "Application Support" has the first, and the
// second is what an unquoted path turns into a shell injection.
func TestCursorGuardScript_QuotesThePath(t *testing.T) {
	dir := t.TempDir()
	awkward := filepath.Join(dir, "run os' dir")
	if err := os.MkdirAll(awkward, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binary := filepath.Join(awkward, "runos")
	source, err := os.ReadFile(cursorGuardBinary(t))
	if err != nil {
		t.Fatalf("read the built binary: %v", err)
	}
	if err := os.WriteFile(binary, source, 0o755); err != nil {
		t.Fatalf("copy the binary: %v", err)
	}

	script := writeCursorGuardScriptForTest(t, binary)
	decision := runCursorGuardScript(t, script, `{"mcp_server_name":"runos-write","tool_input":{}}`)
	if decision["permission"] != "ask" {
		t.Errorf("permission = %v, want ask; the path quoting broke the script", decision["permission"])
	}
}

// A megabyte of tool_input is an ordinary secret-file update. An earlier
// attempt at this guard scanned the payload in awk, which took 47 seconds on
// exactly this input and would have hit Cursor's hook timeout.
func TestCursorGuardScript_DecidesALargePayloadPromptly(t *testing.T) {
	script := writeCursorGuardScriptForTest(t, cursorGuardBinary(t))
	payload := fmt.Sprintf(`{"tool_name":"apps_secret-files_update","tool_input":{"blob":%q},"mcp_server_name":"runos-sensitive-write"}`,
		strings.Repeat("x", 1_000_000))

	start := time.Now()
	decision := runCursorGuardScript(t, script, payload)
	elapsed := time.Since(start)

	if decision["permission"] != "ask" {
		t.Errorf("permission = %v, want ask", decision["permission"])
	}
	if elapsed > 15*time.Second {
		t.Errorf("the guard took %s on a 1 MB payload; Cursor times a hook out long before that", elapsed)
	}
}

func writeCursorGuardScriptForTest(t *testing.T, runosPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runos-guard.sh")
	if err := os.WriteFile(path, []byte(cursorGuardScript(runosPath)), 0o755); err != nil {
		t.Fatalf("write the guard: %v", err)
	}
	return path
}

func runCursorGuardScript(t *testing.T, scriptPath, payload string) map[string]any {
	t.Helper()
	guard := exec.Command("bash", scriptPath)
	guard.Stdin = strings.NewReader(payload)
	var stdout, stderr bytes.Buffer
	guard.Stdout, guard.Stderr = &stdout, &stderr

	if err := guard.Run(); err != nil {
		t.Fatalf("the guard exited badly: %v\nstderr: %s", err, stderr.String())
	}
	var decision map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decision); err != nil {
		t.Fatalf("the guard printed something Cursor cannot read: %q\nstderr: %s", stdout.String(), stderr.String())
	}
	return decision
}

var (
	cursorGuardBinaryOnce sync.Once
	cursorGuardBinaryDir  string
	cursorGuardBinaryPath string
	cursorGuardBinaryErr  error
	// cursorTestPackageDir is captured before any subtest changes directory.
	cursorTestPackageDir, _ = os.Getwd()
)

// cursorGuardBinary builds the real runos binary once for the whole run. The
// guard script hands its payload to that binary, so a test that does not build
// it proves nothing about the decision.
func cursorGuardBinary(t *testing.T) string {
	t.Helper()
	cursorGuardBinaryOnce.Do(func() {
		cursorGuardBinaryDir, cursorGuardBinaryErr = os.MkdirTemp("", "runos-cursor-guard")
		if cursorGuardBinaryErr != nil {
			return
		}
		out := filepath.Join(cursorGuardBinaryDir, "runos")
		build := exec.Command("go", "build", "-o", out, "github.com/runos-official/cli")
		build.Dir = cursorTestPackageDir
		if output, err := build.CombinedOutput(); err != nil {
			cursorGuardBinaryErr = fmt.Errorf("go build: %w\n%s", err, output)
			return
		}
		cursorGuardBinaryPath = out
	})
	if cursorGuardBinaryErr != nil {
		t.Fatalf("the guard cannot be tested without the binary it calls: %v", cursorGuardBinaryErr)
	}
	return cursorGuardBinaryPath
}

// TestMain exists only to clean up that binary. It builds nothing by itself, so
// a run that never touches the guard pays nothing for it.
func TestMain(m *testing.M) {
	code := m.Run()
	if cursorGuardBinaryDir != "" {
		os.RemoveAll(cursorGuardBinaryDir)
	}
	os.Exit(code)
}

// Cursor runs the guard before every MCP call and times it out. The root
// command bootstraps the config from the CDN and fetches the manifest on a
// first run, so the guard has to opt out of that: a hook that waits on the
// network is a hook that blocks the editor.
func TestCursorGuard_DoesNotRunTheRootBootstrap(t *testing.T) {
	binary := cursorGuardBinary(t)
	// A home directory with no RunOS config is what triggers the bootstrap.
	home := t.TempDir()

	guard := exec.Command(binary, "mcp", "cursor-guard")
	guard.Stdin = strings.NewReader(`{"mcp_server_name":"runos-write","tool_input":{}}`)
	// An unroutable address, so any network call the command makes hangs
	// rather than succeeding quietly.
	guard.Env = append(os.Environ(),
		"HOME="+home,
		"RUNOS_API_URL=http://192.0.2.1:1",
	)
	var stdout, stderr bytes.Buffer
	guard.Stdout, guard.Stderr = &stdout, &stderr

	start := time.Now()
	if err := guard.Run(); err != nil {
		t.Fatalf("the guard refused to answer without a config: %v\nstderr: %s", err, stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the guard took %s with no config on disk, which means it reached the network", elapsed)
	}

	var decision map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decision); err != nil {
		t.Fatalf("no decision on stdout: %q\nstderr: %s", stdout.String(), stderr.String())
	}
	if decision["permission"] != "ask" {
		t.Errorf("permission = %v, want ask", decision["permission"])
	}
	if home != "" {
		if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
			t.Errorf("the guard wrote into the home directory: %v", entries)
		}
	}
}

// A set-but-empty RUNOS_API_KEY makes the root command refuse. The guard reads
// no credential, so it has to answer anyway rather than leave Cursor with
// nothing on stdout.
func TestCursorGuard_AnswersWithABrokenAuthEnvironment(t *testing.T) {
	guard := exec.Command(cursorGuardBinary(t), "mcp", "cursor-guard")
	guard.Stdin = strings.NewReader(`{"mcp_server_name":"runos","tool_input":{}}`)
	guard.Env = append(os.Environ(), "RUNOS_API_KEY=")
	var stdout, stderr bytes.Buffer
	guard.Stdout, guard.Stderr = &stdout, &stderr

	if err := guard.Run(); err != nil {
		t.Fatalf("the guard refused to answer: %v\nstderr: %s", err, stderr.String())
	}
	var decision map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decision); err != nil {
		t.Fatalf("no decision on stdout: %q\nstderr: %s", stdout.String(), stderr.String())
	}
	// The read server is allowed. If the root bootstrap ran and refused, the
	// script would fall back to ask and every call in the project would prompt.
	if decision["permission"] != "allow" {
		t.Errorf("permission = %v, want allow", decision["permission"])
	}
}
