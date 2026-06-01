package mcp

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStaticAppsTools_ReadIncludesPullDiffList(t *testing.T) {
	got := staticAppsTools("read")
	gotNames := toolNames(got)
	want := []string{"apps_pull", "apps_diff", "apps_list_previous_uploads"}
	for _, w := range want {
		if !contains(gotNames, w) {
			t.Errorf("read category missing %q; got %v", w, gotNames)
		}
	}
	if contains(gotNames, "apps_sync") {
		t.Error("apps_sync should NOT appear in read category (it modifies cluster state)")
	}
}

func TestStaticAppsTools_WriteIncludesSync(t *testing.T) {
	got := staticAppsTools("write")
	gotNames := toolNames(got)
	if !contains(gotNames, "apps_sync") {
		t.Errorf("write category missing apps_sync; got %v", gotNames)
	}
	for _, readOnly := range []string{"apps_pull", "apps_diff", "apps_list_previous_uploads"} {
		if contains(gotNames, readOnly) {
			t.Errorf("%s should not appear in write category", readOnly)
		}
	}
}

func TestStaticAppsTools_OtherCategoriesEmpty(t *testing.T) {
	for _, cat := range []string{"sensitive_read", "sensitive_write", "unknown"} {
		if got := staticAppsTools(cat); len(got) != 0 {
			t.Errorf("category %q should have no static apps tools, got %d", cat, len(got))
		}
	}
}

func TestIsStaticAppsTool(t *testing.T) {
	yes := []string{"apps_pull", "apps_diff", "apps_sync", "apps_list_previous_uploads"}
	for _, n := range yes {
		if !isStaticAppsTool(n) {
			t.Errorf("isStaticAppsTool(%q) = false, want true", n)
		}
	}
	no := []string{"deploy", "apps_list", "clusters_list", "", "apps_pull_extra"}
	for _, n := range no {
		if isStaticAppsTool(n) {
			t.Errorf("isStaticAppsTool(%q) = true, want false", n)
		}
	}
}

// ---------------------------------------------------------------------------
// argv translation
// ---------------------------------------------------------------------------

func TestBuildAppsPullArgs_BulkDefaults(t *testing.T) {
	got := buildAppsPullArgs(map[string]any{})
	want := []string{"apps", "pull", "--json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestBuildAppsPullArgs_PassesAllFlags(t *testing.T) {
	got := buildAppsPullArgs(map[string]any{
		"yaml_file": "/abs/path/runos.yaml",
		"cid":       "mycluster3 (Hetzner Prod)",
		"app_id":    "appid4",
		"out":       "./synced",
		"force":     true,
	})
	wantHas := []string{
		"/abs/path/runos.yaml",
		"--cid", "mycluster3",
		"--app-id", "appid4",
		"--out", "./synced",
		"--force",
		"--json",
	}
	for _, w := range wantHas {
		if !contains(got, w) {
			t.Errorf("argv missing %q; got %v", w, got)
		}
	}
}

func TestBuildAppsPullArgs_CodeVersionImpliesCode(t *testing.T) {
	// code_version should pass --code-version and NOT also pass --code
	// (the CLI treats --code-version as implying --code).
	got := buildAppsPullArgs(map[string]any{
		"app_id":       "appid4",
		"code":         true, // user passed both; code_version wins
		"code_version": "9e2c1f0b",
	})
	if !contains(got, "--code-version") {
		t.Errorf("expected --code-version in argv; got %v", got)
	}
	if !contains(got, "9e2c1f0b") {
		t.Errorf("expected cliUploadID value in argv; got %v", got)
	}
	if contains(got, "--code") {
		t.Errorf("--code should be omitted when --code-version is set; got %v", got)
	}
}

func TestBuildAppsPullArgs_AllFlag(t *testing.T) {
	got := buildAppsPullArgs(map[string]any{
		"all": true,
		"out": "./synced",
	})
	if !contains(got, "--all") {
		t.Errorf("expected --all in argv; got %v", got)
	}
	if !contains(got, "--out") || !contains(got, "./synced") {
		t.Errorf("expected --out ./synced in argv; got %v", got)
	}
}

func TestBuildAppsPullArgs_CodeAloneAddsFlag(t *testing.T) {
	got := buildAppsPullArgs(map[string]any{
		"app_id": "appid4",
		"code":   true,
	})
	if !contains(got, "--code") {
		t.Errorf("expected --code in argv; got %v", got)
	}
}

func TestBuildAppsDiffArgs_RequiresYamlFile(t *testing.T) {
	if _, err := buildAppsDiffArgs(map[string]any{}); err == nil {
		t.Fatal("expected error when yaml_file missing")
	}

	got, err := buildAppsDiffArgs(map[string]any{
		"yaml_file": "runos.yaml",
		"cid":       "mycluster3 (Hetzner)",
	})
	if err != nil {
		t.Fatalf("buildAppsDiffArgs: %v", err)
	}
	for _, want := range []string{"apps", "diff", "runos.yaml", "--json", "--cid", "mycluster3", "--redact-secrets"} {
		if !contains(got, want) {
			t.Errorf("argv missing %q; got %v", want, got)
		}
	}
	assertYamlIsLastPositional(t, got, "runos.yaml")
}

// TestBuildAppsDiffArgs_FlagsBeforeDoubleDash regression: a previous version
// placed `--` and the yaml positional before the cid append, which made
// `--cid mycluster3` get parsed as two extra positionals (Cobra treats everything
// after `--` as positional). The fix is to append `--`/yaml last.
func TestBuildAppsDiffArgs_FlagsBeforeDoubleDash(t *testing.T) {
	got, err := buildAppsDiffArgs(map[string]any{
		"yaml_file": "runos.yaml",
		"cid":       "mycluster3 (Hetzner)",
	})
	if err != nil {
		t.Fatalf("buildAppsDiffArgs: %v", err)
	}
	dashIdx := indexOf(got, "--")
	cidIdx := indexOf(got, "--cid")
	if cidIdx < 0 {
		t.Fatalf("expected --cid in argv; got %v", got)
	}
	if dashIdx < 0 || cidIdx > dashIdx {
		t.Errorf("--cid must appear before `--`, otherwise Cobra reads it as a positional; got %v", got)
	}
}

// indexOf returns the position of v in s, or -1.
func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// assertYamlIsLastPositional checks that argv ends with `-- <yaml>`. Anything
// appended after `--` is treated as a positional by Cobra, so this layout is
// the only one that lets us pass a yaml-shaped value (potentially starting
// with `-`) without it being misread as a flag, while also keeping subsequent
// flags out of the positional bucket.
func assertYamlIsLastPositional(t *testing.T, argv []string, yaml string) {
	t.Helper()
	if len(argv) < 2 {
		t.Fatalf("argv too short to contain `-- <yaml>`: %v", argv)
	}
	last := argv[len(argv)-1]
	prev := argv[len(argv)-2]
	if prev != "--" || last != yaml {
		t.Errorf("expected argv to end with `-- %s`; got tail %q %q in %v", yaml, prev, last, argv)
	}
}

func TestBuildAppsSyncArgs_AlwaysAddsYes(t *testing.T) {
	got, err := buildAppsSyncArgs(map[string]any{
		"yaml_file": "runos.yaml",
	})
	if err != nil {
		t.Fatalf("buildAppsSyncArgs: %v", err)
	}
	if !contains(got, "--yes") {
		t.Errorf("sync argv must include --yes (no interactive stdin in MCP); got %v", got)
	}
	assertYamlIsLastPositional(t, got, "runos.yaml")
}

func TestBuildAppsSyncArgs_DryRun(t *testing.T) {
	got, err := buildAppsSyncArgs(map[string]any{
		"yaml_file": "runos.yaml",
		"dry_run":   true,
	})
	if err != nil {
		t.Fatalf("buildAppsSyncArgs: %v", err)
	}
	if !contains(got, "--dry-run") {
		t.Errorf("dry_run=true should produce --dry-run; got %v", got)
	}
	assertYamlIsLastPositional(t, got, "runos.yaml")
}

// TestBuildAppsSyncArgs_FlagsBeforeDoubleDash regression: with cid and dry_run
// both set, the previous layout produced `apps sync ... -- runos.yaml --cid mycluster3
// --dry-run`, and Cobra read the trailing flags as positionals (received 4).
// Every flag must appear before `--`.
func TestBuildAppsSyncArgs_FlagsBeforeDoubleDash(t *testing.T) {
	got, err := buildAppsSyncArgs(map[string]any{
		"yaml_file": "runos.yaml",
		"cid":       "mycluster3 (Hetzner)",
		"dry_run":   true,
	})
	if err != nil {
		t.Fatalf("buildAppsSyncArgs: %v", err)
	}
	dashIdx := indexOf(got, "--")
	if dashIdx < 0 {
		t.Fatalf("expected `--` in argv; got %v", got)
	}
	for _, flag := range []string{"--cid", "--dry-run", "--yes", "--redact-secrets"} {
		idx := indexOf(got, flag)
		if idx < 0 {
			t.Errorf("expected %s in argv; got %v", flag, got)
			continue
		}
		if idx > dashIdx {
			t.Errorf("%s must appear before `--`, otherwise Cobra reads it as a positional; got %v", flag, got)
		}
	}
	assertYamlIsLastPositional(t, got, "runos.yaml")
}

func TestBuildAppsListPreviousUploadsArgs_RequiresYamlFile(t *testing.T) {
	if _, err := buildAppsListPreviousUploadsArgs(map[string]any{}); err == nil {
		t.Fatal("expected error when yaml_file missing")
	}
	got, err := buildAppsListPreviousUploadsArgs(map[string]any{
		"yaml_file": "runos.mycluster3.appid4/runos.yaml",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !contains(got, "list-previous-uploads") || !contains(got, "--json") {
		t.Errorf("argv missing pieces: %v", got)
	}
}

// ---------------------------------------------------------------------------
// buildDeployArgs (MCP deploy tool)
// ---------------------------------------------------------------------------

func TestBuildDeployArgs_AlwaysFollows(t *testing.T) {
	got := buildDeployArgs(map[string]any{})
	if !contains(got, "--follow") {
		t.Errorf("argv must include --follow; got %v", got)
	}
}

func TestBuildDeployArgs_ForwardsCidAndStripsName(t *testing.T) {
	got := buildDeployArgs(map[string]any{
		"cid": "mycluster3 (Hetzner Prod)",
	})
	if !contains(got, "--cid") || !contains(got, "mycluster3") {
		t.Errorf("expected --cid mycluster3 (short id only) in argv; got %v", got)
	}
	if contains(got, "mycluster3 (Hetzner Prod)") {
		t.Errorf("argv should not carry the (Name) suffix; got %v", got)
	}
}

func TestBuildDeployArgs_ForceWhenTrue(t *testing.T) {
	got := buildDeployArgs(map[string]any{"force": true})
	if !contains(got, "--force") {
		t.Errorf("expected --force in argv; got %v", got)
	}
}

// TestBuildDeployArgs_WithYamlFile pins the multi-yaml entry point: when
// the project directory holds more than one runos*.yaml the LLM has to
// pass yaml_file, and we forward it as the CLI's --config flag.
func TestBuildDeployArgs_WithYamlFile(t *testing.T) {
	got := buildDeployArgs(map[string]any{
		"yaml_file": "runos.mycluster3.appid4.yaml",
	})
	if !contains(got, "--config") || !contains(got, "runos.mycluster3.appid4.yaml") {
		t.Errorf("expected --config runos.mycluster3.appid4.yaml in argv; got %v", got)
	}
	cfgIdx := indexOf(got, "--config")
	pathIdx := indexOf(got, "runos.mycluster3.appid4.yaml")
	if cfgIdx < 0 || pathIdx != cfgIdx+1 {
		t.Errorf("--config and its value must be adjacent; got %v", got)
	}
}

func TestBuildDeployArgs_WithoutYamlFile(t *testing.T) {
	// Single-yaml back-compat: no yaml_file means the subprocess uses
	// the default runos.yaml in cwd, no --config flag added.
	got := buildDeployArgs(map[string]any{})
	if contains(got, "--config") {
		t.Errorf("--config must be omitted when yaml_file is absent; got %v", got)
	}
}

func TestBuildDeployArgs_YamlFileWithCidAndForce(t *testing.T) {
	got := buildDeployArgs(map[string]any{
		"cid":       "mycluster3 (Hetzner Prod)",
		"yaml_file": "/abs/runos.mycluster3.appid4.yaml",
		"force":     true,
	})
	for _, want := range []string{"--cid", "mycluster3", "--config", "/abs/runos.mycluster3.appid4.yaml", "--force", "--follow"} {
		if !contains(got, want) {
			t.Errorf("argv missing %q; got %v", want, got)
		}
	}
}

// TestBuildDeployArgs_ForwardsBuildArgArray pins the MCP -> CLI
// translation for the new `build_arg` array: one --build-arg flag per
// entry, preserved in invocation order. Non-string entries are skipped
// (MCP schema declares items as string; a non-string in the array is
// malformed input on the caller's side, not silent corruption).
// Objective 40 / story 60.
func TestBuildDeployArgs_ForwardsBuildArgArray(t *testing.T) {
	got := buildDeployArgs(map[string]any{
		"build_arg": []any{
			"NEXT_PUBLIC_API_PORT=443",
			"NODE_ENV=production",
		},
	})
	if !contains(got, "--build-arg") {
		t.Fatalf("expected --build-arg in argv; got %v", got)
	}
	// Count: two flags + two values = four matching positions.
	var flagCount, val1Idx, val2Idx int
	for i, s := range got {
		if s == "--build-arg" {
			flagCount++
		}
		if s == "NEXT_PUBLIC_API_PORT=443" {
			val1Idx = i
		}
		if s == "NODE_ENV=production" {
			val2Idx = i
		}
	}
	if flagCount != 2 {
		t.Errorf("--build-arg occurrences = %d, want 2; got %v", flagCount, got)
	}
	if val1Idx == 0 || val2Idx == 0 || val1Idx > val2Idx {
		t.Errorf("build_arg values out of order or missing; got %v (val1=%d val2=%d)", got, val1Idx, val2Idx)
	}
}

func TestBuildDeployArgs_BuildArgAbsentOrEmpty(t *testing.T) {
	for _, args := range []map[string]any{
		{},
		{"build_arg": nil},
		{"build_arg": []any{}},
		{"build_arg": "not-an-array"}, // wrong type, must not propagate
	} {
		got := buildDeployArgs(args)
		if contains(got, "--build-arg") {
			t.Errorf("--build-arg should be absent for args %v; got %v", args, got)
		}
	}
}

func TestBuildDeployArgs_BuildArgSkipsNonStringEntries(t *testing.T) {
	// MCP schema declares items: string. Non-string entries from a
	// misbehaving caller are silently dropped here; the CLI's
	// internal/buildargs.Parse would in any case reject malformed shape
	// downstream. Pinned so a future "be tolerant" refactor can't
	// turn a 42 into "42" implicitly.
	got := buildDeployArgs(map[string]any{
		"build_arg": []any{
			"FOO=ok",
			42, // dropped
			"",
			"BAR=ok",
		},
	})
	matches := 0
	for _, s := range got {
		if s == "FOO=ok" || s == "BAR=ok" {
			matches++
		}
	}
	if matches != 2 {
		t.Errorf("expected FOO=ok and BAR=ok to survive; got %v", got)
	}
	for _, s := range got {
		if s == "42" || s == "" {
			t.Errorf("non-string entry leaked into argv: %v", got)
		}
	}
}

func TestBuildDeployArgs_NoForceWhenFalseOrAbsent(t *testing.T) {
	cases := []map[string]any{
		{},
		{"force": false},
		{"force": "true"}, // wrong type, must not propagate
	}
	for _, args := range cases {
		got := buildDeployArgs(args)
		if contains(got, "--force") {
			t.Errorf("--force should be absent for args %v; got %v", args, got)
		}
	}
}

func TestBuildAppsCommandArgs_UnknownTool(t *testing.T) {
	_, err := buildAppsCommandArgs("apps_unknown", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention unknown; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// interpretAppsCommandResult
// ---------------------------------------------------------------------------

// Round 1 follow-up (Test 2 UX): apps_diff exits with code 2 to signal
// "drift detected" so CI gates fail and the MCP wrapper can translate it
// into a successful tool result with the structured drift report. The
// translation must be tight — only `apps_diff` and only on the
// dedicated exit code, never on a generic non-zero exit or a different
// tool. Otherwise an MCP caller could see false "success" for real
// failures.
func TestInterpretAppsCommandResult_DriftSignalIsSuccess(t *testing.T) {
	t.Parallel()
	out, err := interpretAppsCommandResult(
		errors.New("exit status 2"),
		2,    // exitCode
		true, // hasExitError
		`{"status":"drift"}`,
		"apps_diff",
	)
	if err != nil {
		t.Errorf("expected drift signal to translate to success, got err: %v", err)
	}
	if out != `{"status":"drift"}` {
		t.Errorf("expected drift output preserved, got %q", out)
	}
}

func TestInterpretAppsCommandResult_OtherToolsKeepExitCode2AsError(t *testing.T) {
	t.Parallel()
	// Translation is apps_diff-specific. apps_sync / apps_pull / etc.
	// returning exit code 2 is treated as a real failure — there's no
	// drift contract for them.
	for _, tool := range []string{"apps_sync", "apps_pull", "apps_show"} {
		t.Run(tool, func(t *testing.T) {
			_, err := interpretAppsCommandResult(
				errors.New("exit status 2"),
				2,
				true,
				"some output",
				tool,
			)
			if err == nil {
				t.Errorf("expected non-apps_diff tool to surface exit code 2 as error, got nil")
			}
		})
	}
}

func TestInterpretAppsCommandResult_RealErrorsStaySurfaced(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		runErr       error
		exitCode     int
		hasExitError bool
		output       string
	}{
		{
			name:         "exit code 1 with output (typical CLI failure)",
			runErr:       errors.New("exit status 1"),
			exitCode:     1,
			hasExitError: true,
			output:       "API error: Bad Request",
		},
		{
			name:         "exit code 1 without output",
			runErr:       errors.New("exit status 1"),
			exitCode:     1,
			hasExitError: true,
			output:       "",
		},
		{
			name:         "non-ExitError (e.g. runtime exec failure)",
			runErr:       errors.New("fork/exec: no such file"),
			exitCode:     0,
			hasExitError: false,
			output:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interpretAppsCommandResult(
				tc.runErr, tc.exitCode, tc.hasExitError, tc.output, "apps_diff",
			)
			if err == nil {
				t.Errorf("expected real error to surface, got nil")
			}
		})
	}
}

func TestInterpretAppsCommandResult_CleanRunIsPassthrough(t *testing.T) {
	t.Parallel()
	out, err := interpretAppsCommandResult(nil, 0, false, "clean output", "apps_pull")
	if err != nil {
		t.Errorf("expected nil error on clean run, got %v", err)
	}
	if out != "clean output" {
		t.Errorf("expected output passthrough, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func toolNames(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}
