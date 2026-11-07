package dynacmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// probeCmdDef mirrors the conductor manifest entry for the watchdog probe (FCR 1909): a
// maintenance-scripts run that changes nothing unless LOAD_DRIVER is "true".
func probeCmdDef(rule *manifest.ReadOnlyUnless) manifest.Command {
	return manifest.Command{
		Command:        "maintenance-scripts/watchdog-probe/run",
		Method:         "POST",
		MCP:            []string{"read"},
		ReadOnlyUnless: rule,
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "LOAD_DRIVER", Type: "string", Default: "false"},
			{Name: "NODE_NID", Type: "string"},
		}},
	}
}

func loadDriverRule() *manifest.ReadOnlyUnless {
	return &manifest.ReadOnlyUnless{Field: "LOAD_DRIVER", Values: []string{"true"}}
}

// probeCobra builds the leaf flags the builder registers for the probe, then applies args.
func probeCobra(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "run"}
	c.Flags().String("load-driver", "false", "")
	c.Flags().String("nid", "", "")
	c.Flags().StringP("file", "f", "", "")
	c.Flags().BoolP("yes", "y", false, "")
	if err := c.ParseFlags(args); err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	return c
}

func writeBody(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// FCR 1909. The probe reads sysfs and /proc only, so the default run must not prompt. With
// LOAD_DRIVER=true it loads a kernel module and must prompt. --yes stays registered so a
// script that passes it keeps working, and every other command keeps its confirmation.
func TestDestructivePromptApplies_ReadOnlyUnless(t *testing.T) {
	probe := probeCmdDef(loadDriverRule())
	cases := []struct {
		name string
		c    *cobra.Command
		want bool
	}{
		{"no flag: the default false is read-only", probeCobra(t), false},
		{"--load-driver false", probeCobra(t, "--load-driver", "false"), false},
		{"--load-driver yes is not a listed value", probeCobra(t, "--load-driver", "yes"), false},
		{"--load-driver true", probeCobra(t, "--load-driver", "true"), true},
		{"--load-driver TRUE matches case-insensitively", probeCobra(t, "--load-driver", "TRUE"), true},
		{"--load-driver with spaces still matches", probeCobra(t, "--load-driver", " true "), true},
		{"-f body sets LOAD_DRIVER true", probeCobra(t, "-f", writeBody(t, "LOAD_DRIVER: \"true\"\n")), true},
		{"-f body sets LOAD_DRIVER to a YAML bool", probeCobra(t, "-f", writeBody(t, "LOAD_DRIVER: true\n")), true},
		{"-f body sets LOAD_DRIVER false", probeCobra(t, "-f", writeBody(t, "LOAD_DRIVER: \"false\"\n")), false},
		{"-f body without LOAD_DRIVER", probeCobra(t, "-f", writeBody(t, "NODE_NID: n1\n")), false},
		{"-f body with a list value fails closed", probeCobra(t, "-f", writeBody(t, "LOAD_DRIVER: [\"true\"]\n")), true},
		{"-f body that cannot be read fails closed", probeCobra(t, "-f", filepath.Join(t.TempDir(), "missing.yaml")), true},
		{"flag false but -f true: either source loading the driver prompts", probeCobra(t, "--load-driver", "false", "-f", writeBody(t, "LOAD_DRIVER: \"true\"\n")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := destructivePromptApplies(tc.c, probe); got != tc.want {
				t.Errorf("destructivePromptApplies = %v, want %v", got, tc.want)
			}
		})
	}
	if !IsDestructiveCommand(probe) {
		t.Error("IsDestructiveCommand must stay true for the probe so --yes stays registered")
	}
}

// A marker the CLI cannot honour fails closed: the command keeps its confirmation.
func TestDestructivePromptApplies_ReadOnlyUnlessFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		cmd  manifest.Command
	}{
		{"malformed marker", probeCmdDef(&manifest.ReadOnlyUnless{Malformed: true})},
		{"empty field", probeCmdDef(&manifest.ReadOnlyUnless{Values: []string{"true"}})},
		{"no values", probeCmdDef(&manifest.ReadOnlyUnless{Field: "LOAD_DRIVER"})},
		{"an empty value", probeCmdDef(&manifest.ReadOnlyUnless{Field: "LOAD_DRIVER", Values: []string{""}})},
		{"field the command does not declare", probeCmdDef(&manifest.ReadOnlyUnless{Field: "NOPE", Values: []string{"true"}})},
		{"no input at all", manifest.Command{Command: "maintenance-scripts/x/run", Method: "POST", ReadOnlyUnless: loadDriverRule()}},
		{"positional field", manifest.Command{
			Command: "maintenance-scripts/x/run", Method: "POST", ReadOnlyUnless: loadDriverRule(),
			Input: &manifest.Input{Fields: []manifest.Field{{Name: "LOAD_DRIVER", Type: "string", Positional: true}}},
		}},
		{"DELETE is always destructive", manifest.Command{
			Command: "apps/{id}/delete", Method: "DELETE", ReadOnlyUnless: loadDriverRule(),
			Input: &manifest.Input{Fields: []manifest.Field{{Name: "LOAD_DRIVER", Type: "string"}}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !destructivePromptApplies(probeCobra(t), tc.cmd) {
				t.Errorf("%s must keep the confirmation", tc.name)
			}
		})
	}
}

// A default the server would act on counts: a field whose default is a listed value prompts.
func TestDestructivePromptApplies_ReadOnlyUnlessHonoursDefault(t *testing.T) {
	cmd := probeCmdDef(loadDriverRule())
	cmd.Input.Fields[0].Default = "true"
	if !destructivePromptApplies(probeCobra(t), cmd) {
		t.Error("a default of true loads the driver and must prompt")
	}
}

// Unmarked commands are unchanged: the apt upgrade still demands --yes.
func TestDestructivePromptApplies_UnmarkedScriptUnchanged(t *testing.T) {
	apt := manifest.Command{Command: "maintenance-scripts/node-apt-upgrade-reboot/run", Method: "POST"}
	if !destructivePromptApplies(&cobra.Command{Use: "run"}, apt) {
		t.Error("node-apt-upgrade-reboot run must still prompt")
	}
}

// The MCP surface asks the same question with a map of arguments.
func TestIsDestructiveInvocation_Args(t *testing.T) {
	probe := probeCmdDef(loadDriverRule())
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"no args", map[string]any{}, false},
		{"nil args", nil, false},
		{"false", map[string]any{"LOAD_DRIVER": "false"}, false},
		{"null value uses the default", map[string]any{"LOAD_DRIVER": nil}, false},
		{"true", map[string]any{"LOAD_DRIVER": "true"}, true},
		{"True", map[string]any{"LOAD_DRIVER": "True"}, true},
		{"bool true", map[string]any{"LOAD_DRIVER": true}, true},
		{"bool false", map[string]any{"LOAD_DRIVER": false}, false},
		{"list fails closed", map[string]any{"LOAD_DRIVER": []any{"true"}}, true},
		{"object fails closed", map[string]any{"LOAD_DRIVER": map[string]any{"v": "true"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDestructiveInvocation(probe, tc.args); got != tc.want {
				t.Errorf("IsDestructiveInvocation(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
	apt := manifest.Command{Command: "maintenance-scripts/node-apt-upgrade-reboot/run", Method: "POST"}
	if !IsDestructiveInvocation(apt, nil) {
		t.Error("an unmarked destructive command stays destructive")
	}
	list := manifest.Command{Command: "apps/list", Method: "GET", ReadOnlyUnless: loadDriverRule()}
	if IsDestructiveInvocation(list, map[string]any{"LOAD_DRIVER": "true"}) {
		t.Error("the marker only narrows: a non-destructive command never becomes destructive")
	}
}
