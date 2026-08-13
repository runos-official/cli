package dynacmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// Regression: pre-fix, `runos apps delete <id>` (and every services
// delete sibling) reached the wire on the first keystroke with no
// confirmation prompt and no --yes flag, mirroring `runos deploy`
// gives users a recoverable typo path. isDestructiveCommand is the
// single classifier the builder uses to gate both the flag
// registration and the prompt; pin its behaviour by method.
func TestIsDestructiveCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  manifest.Command
		want bool
	}{
		{"DELETE method is destructive", manifest.Command{Method: "DELETE"}, true},
		{"lowercase delete still matches", manifest.Command{Method: "delete"}, true},
		{"GET is safe", manifest.Command{Method: "GET"}, false},
		{"POST is non-destructive (add/create)", manifest.Command{Method: "POST"}, false},
		{"PUT is non-destructive (update)", manifest.Command{Method: "PUT"}, false},
		{"PATCH is non-destructive", manifest.Command{Method: "PATCH"}, false},
		{"empty method is not destructive", manifest.Command{Method: ""}, false},
		// #27: POST/PATCH endpoints with destructive verbs in the path must
		// also trigger the guard. Pre-fix the guard fired only on DELETE
		// and `valkey clear-cache` / `nodes drain` / `postgresql exec-sql`
		// / `clusters reset` / `minio delete-bucket` / `valkey set-data`
		// / `postgresql revoke-database` all reached the wire unprotected.
		{"POST clear-cache is destructive", manifest.Command{Command: "services/valkey/{id}/clear-cache", Method: "POST"}, true},
		{"POST nodes drain is destructive", manifest.Command{Command: "nodes/{nid}/drain", Method: "POST"}, true},
		{"POST clusters reset is destructive", manifest.Command{Command: "clusters/{cid}/reset", Method: "POST"}, true},
		{"PATCH exec-sql is destructive", manifest.Command{Command: "services/postgresql/{id}/exec-sql", Method: "PATCH"}, true},
		{"PATCH set-data is destructive", manifest.Command{Command: "services/valkey/{id}/set-data", Method: "PATCH"}, true},
		{"PATCH delete-bucket is destructive (prefix match)", manifest.Command{Command: "services/minio/{id}/delete-bucket", Method: "PATCH"}, true},
		{"PATCH delete-object is destructive (prefix match)", manifest.Command{Command: "services/minio/{id}/delete-object", Method: "PATCH"}, true},
		{"PATCH revoke-database is destructive", manifest.Command{Command: "services/postgresql/{id}/revoke-database", Method: "PATCH"}, true},
		{"PATCH remove-peer is destructive", manifest.Command{Command: "services/wireguard/{id}/remove-peer", Method: "PATCH"}, true},
		// obj-58 / Story 103: postgres teardown verbs. drop-user / drop-
		// database are sync PATCH (method-agnostic prefix match on `drop-`)
		// and must inherit the --yes guard + confirmation prompt. The
		// prefix also future-proofs any further drop-* verb.
		{"PATCH drop-user is destructive (drop- prefix)", manifest.Command{Command: "services/postgresql/{id}/drop-user", Method: "PATCH"}, true},
		{"PATCH drop-database is destructive (drop- prefix)", manifest.Command{Command: "services/postgresql/{id}/drop-database", Method: "PATCH"}, true},
		// Goal 23 F26. `storage-groups/wipe-device` destroys every byte on a
		// disk irreversibly and was the ONLY destructive storage verb that ran
		// on the first ask: remove-device and remove-node, which merely edit
		// records, both demanded --yes. The suffix list already carried "wipe",
		// but the final segment is "wipe-device", so neither list matched.
		// Measured on live hardware: it wiped a disk holding a running VM's
		// DRBD replica and reported success. The prefix now covers the whole
		// family so a later wipe-* / flush-* / purge-* inherits the guard.
		{"POST wipe-device is destructive (wipe- prefix)", manifest.Command{Command: "storage-groups/wipe-device", Method: "POST"}, true},
		{"POST flush-cache is destructive (flush- prefix)", manifest.Command{Command: "services/valkey/{id}/flush-cache", Method: "POST"}, true},
		{"POST purge-queue is destructive (purge- prefix)", manifest.Command{Command: "services/kafka/{id}/purge-queue", Method: "POST"}, true},
		// Non-destructive POST/PATCH should NOT trip the guard. The
		// safety net cuts the wrong way only on these few verbs.
		{"POST add is not destructive", manifest.Command{Command: "services/postgresql/add", Method: "POST"}, false},
		{"PATCH update is not destructive", manifest.Command{Command: "services/postgresql/{id}/update", Method: "PATCH"}, false},
		{"POST scale is not destructive", manifest.Command{Command: "services/postgresql/{id}/scale", Method: "POST"}, false},
		// restart is borderline; not in the destructive list because it's
		// reversible and CI workflows expect to restart without --yes
		// gating every invocation.
		{"PATCH restart is not destructive (reversible)", manifest.Command{Command: "services/kafka/{id}/restart", Method: "PATCH"}, false},
		// foreman #37 / Story 50: every maintenance-scripts `run` trigger
		// is destructive by category (cordon/drain/reboot/etc). Matched
		// on path containment + final segment so any future script the
		// registry surfaces inherits the prompt; an unrelated command
		// that happens to end in `run` is not falsely flagged.
		{"POST maintenance-scripts run is destructive", manifest.Command{Command: "maintenance-scripts/node-apt-upgrade-reboot/run", Method: "POST"}, true},
		{"POST hypothetical other maintenance-scripts run is destructive (future scripts inherit)", manifest.Command{Command: "maintenance-scripts/some-future-script/run", Method: "POST"}, true},
		{"POST unrelated path ending in run is NOT destructive", manifest.Command{Command: "apps/{id}/run", Method: "POST"}, false},
		// Goal 23 F11 bonus. `nodes/delete-preflight` is a GET, documented "Advisory only, never
		// blocks the delete", and confirmed read-only on live hardware: run with --yes it
		// returned etcd parity advice and both nodes were still present afterwards. The
		// `delete-` prefix match gated it as a write. A read behind a destructive prompt trains
		// operators to type --yes without reading, which is the opposite of what the prompt is for.
		{"GET delete-preflight is a read, never gated", manifest.Command{Command: "nodes/{nid}/delete-preflight", Method: "GET"}, false},
		{"GET with any destructive-looking verb stays a read", manifest.Command{Command: "services/minio/{id}/delete-bucket-preview", Method: "GET"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDestructiveCommand(tc.cmd); got != tc.want {
				t.Errorf("isDestructiveCommand(%+v) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// destructiveSummary lifts the resolved primary positional id into
// the confirmation prompt so a typo on the id is visible before the
// user answers y/N. Falls back to the manifest description when no
// positional is available.
func TestDestructiveSummary(t *testing.T) {
	cmdWithID := manifest.Command{
		Command:     "apps/{id}/delete",
		Description: "Delete an application",
		Method:      "DELETE",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true, Required: true},
		}},
	}
	noPositional := manifest.Command{
		Command:     "tools/cleanup",
		Description: "Cleanup orphaned resources",
		Method:      "DELETE",
		Input:       &manifest.Input{Fields: []manifest.Field{}},
	}
	multiPositional := manifest.Command{
		Command:     "services/{type}/{id}/delete",
		Description: "Delete a service",
		Method:      "DELETE",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "type", Type: "string", Positional: true, Required: true},
			{Name: "id", Type: "string", Positional: true, Required: true},
		}},
	}
	// Regression target: #131. `clusters reset --cid mycluster3` accepts cid
	// only via --cid (the positional slot is empty), so the summary
	// has to consult the flag form before falling back to the
	// manifest description.
	clusterReset := manifest.Command{
		Command:     "clusters/{cid}/reset",
		Description: "Reset a cluster to unconfigured state",
		Method:      "POST",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "cid", Type: "string", Positional: true, Required: true},
		}},
	}
	cases := []struct {
		name string
		cmd  manifest.Command
		args []string
		// flags optionally seeds a cobra.Command with --<key> set to
		// <value> before destructiveSummary is called. nil reproduces
		// the legacy "args-only" code path.
		flags map[string]string
		want  string
	}{
		{name: "single positional present", cmd: cmdWithID, args: []string{"d2eow"}, want: "id=d2eow"},
		{name: "positional absent falls back to description", cmd: cmdWithID, args: []string{}, want: "Delete an application"},
		{name: "no positional fields uses description", cmd: noPositional, args: []string{}, want: "Cleanup orphaned resources"},
		{name: "first positional wins on multi", cmd: multiPositional, args: []string{"postgresql", "lw0vp"}, want: "type=postgresql"},
		{name: "flag-only cid surfaces in target (#131)", cmd: clusterReset, args: []string{}, flags: map[string]string{"cid": "mycluster3"}, want: "cid=mycluster3"},
		{name: "flag empty falls back to description", cmd: clusterReset, args: []string{}, flags: map[string]string{"cid": ""}, want: "Reset a cluster to unconfigured state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c *cobra.Command
			if tc.flags != nil {
				c = &cobra.Command{Use: "x"}
				for k, v := range tc.flags {
					c.Flags().String(k, "", "")
					if v != "" {
						if err := c.Flags().Set(k, v); err != nil {
							t.Fatalf("seed flag %s: %v", k, err)
						}
					}
				}
			}
			if got := destructiveSummary(c, tc.cmd, tc.args); got != tc.want {
				t.Errorf("destructiveSummary(%s, %v) = %q, want %q", tc.cmd.Command, tc.args, got, tc.want)
			}
		})
	}
}

// Regression for foreman #140: exec-sql is the only destructive verb
// whose actual destructiveness depends on a runtime flag (--read-write).
// destructivePromptApplies must skip the prompt when --read-write is
// false (the safe default) so read-only SELECTs don't train operators
// to type --yes reflexively. Every other destructive verb stays gated
// unconditionally.
func TestDestructivePromptApplies(t *testing.T) {
	execSQL := manifest.Command{Command: "services/postgresql/{id}/exec-sql", Method: "PATCH"}
	appsDelete := manifest.Command{Command: "apps/{id}/delete", Method: "DELETE"}
	clustersReset := manifest.Command{Command: "clusters/{cid}/reset", Method: "POST"}
	appsUpdate := manifest.Command{Command: "apps/{id}/update", Method: "PATCH"}

	makeExecSQLCmd := func(rwSet, rw bool) *cobra.Command {
		c := &cobra.Command{Use: "exec-sql"}
		c.Flags().Bool("read-write", false, "")
		if rwSet {
			v := "false"
			if rw {
				v = "true"
			}
			if err := c.Flags().Set("read-write", v); err != nil {
				t.Fatalf("Set: %v", err)
			}
		}
		return c
	}

	cases := []struct {
		name string
		cmd  manifest.Command
		c    *cobra.Command
		want bool
	}{
		{"exec-sql with --read-write=true prompts", execSQL, makeExecSQLCmd(true, true), true},
		{"exec-sql default (read-write unset) skips prompt", execSQL, makeExecSQLCmd(false, false), false},
		{"exec-sql explicit --read-write=false skips prompt", execSQL, makeExecSQLCmd(true, false), false},
		{"exec-sql without --read-write declared falls back to prompting (safe default)", execSQL, &cobra.Command{Use: "exec-sql"}, true},
		{"DELETE apps delete prompts regardless of flags", appsDelete, &cobra.Command{Use: "delete"}, true},
		{"POST clusters reset prompts regardless of flags", clustersReset, &cobra.Command{Use: "reset"}, true},
		{"non-destructive PATCH apps update skips", appsUpdate, &cobra.Command{Use: "update"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := destructivePromptApplies(tc.c, tc.cmd); got != tc.want {
				t.Errorf("destructivePromptApplies(%s) = %v, want %v", tc.cmd.Command, got, tc.want)
			}
		})
	}
}

// Regression (#23 round 2): the first cut auto-proceeded when stdin
// was not a TTY OR --json was set, exactly the LLM/MCP/CI invocation
// the original bug called out. confirmDestructive now refuses to
// proceed without --yes whenever it can't show a prompt the user can
// answer. This test covers the pure non-TTY branch via a fake cobra
// command; the TTY-prompt branch shares the same well-traveled
// `bufio.NewReader(os.Stdin)` path as deploy's confirmDeploy and is
// exercised live.
func TestConfirmDestructive_NonTTYRefusesWithoutYes(t *testing.T) {
	cmdDef := manifest.Command{
		Command:     "apps/{id}/delete",
		Description: "Delete an application",
		Method:      "DELETE",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true, Required: true},
		}},
	}

	// Without --yes: refuse with a typed error naming the flag.
	c := &cobra.Command{Use: "delete"}
	c.Flags().BoolP("yes", "y", false, "skip the destructive-action confirmation prompt")
	c.Flags().BoolP("json", "j", false, "JSON output")
	err := confirmDestructive(c, cmdDef, []string{"d2eow"})
	if err == nil {
		t.Fatalf("non-TTY without --yes should refuse, got nil error")
	}
	if !strings.Contains(err.Error(), "requires confirmation") {
		t.Errorf("error %q should mention 'requires confirmation'", err.Error())
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error %q should name the --yes flag", err.Error())
	}
	if !strings.Contains(err.Error(), "d2eow") {
		t.Errorf("error %q should name the target id", err.Error())
	}

	// With --yes: skip prompt, proceed (nil error).
	c2 := &cobra.Command{Use: "delete"}
	c2.Flags().BoolP("yes", "y", false, "skip")
	c2.Flags().BoolP("json", "j", false, "JSON output")
	if err := c2.ParseFlags([]string{"--yes"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if err := confirmDestructive(c2, cmdDef, []string{"d2eow"}); err != nil {
		t.Errorf("--yes should auto-proceed, got error: %v", err)
	}

	// --json alone does NOT bypass the guard: the LLM/MCP caller still
	// has to opt in explicitly with --yes. This is the key contract
	// the original test report (#23 regressed) flagged as broken.
	c3 := &cobra.Command{Use: "delete"}
	c3.Flags().BoolP("yes", "y", false, "skip")
	c3.Flags().BoolP("json", "j", false, "JSON output")
	if err := c3.ParseFlags([]string{"--json"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err = confirmDestructive(c3, cmdDef, []string{"d2eow"})
	if err == nil {
		t.Fatalf("--json alone (no --yes) should still refuse in non-TTY mode")
	}
}

// Goal 23 F8 item 3: the CLI and the console disagreed about which verbs are dangerous.
// `vms stop` takes a customer's machine down and reached the wire on the first keystroke, while
// `nodes delete` and `clusters reset` were gated. The console had already gained confirmations
// for stop and restart.
func TestVmPowerVerbsAreGated(t *testing.T) {
	cases := []struct {
		name string
		cmd  manifest.Command
		want bool
	}{
		{"vms stop is gated", manifest.Command{Command: "vms/{vmid}/stop", Method: "PATCH"}, true},
		{"vms restart is gated", manifest.Command{Command: "vms/{vmid}/restart", Method: "PATCH"}, true},
		{"vms pause is gated", manifest.Command{Command: "vms/{vmid}/pause", Method: "PATCH"}, true},
		// Bringing a machine up harms nothing.
		{"vms start is not gated", manifest.Command{Command: "vms/{vmid}/start", Method: "PATCH"}, false},
		{"vms resume is not gated", manifest.Command{Command: "vms/{vmid}/resume", Method: "PATCH"}, false},
		// Scoped to vms/ deliberately: a service restart is a process back in seconds, and CI
		// workflows expect to run it unattended.
		{"service restart stays ungated", manifest.Command{Command: "services/kafka/{id}/restart", Method: "PATCH"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDestructiveCommand(tc.cmd); got != tc.want {
				t.Errorf("isDestructiveCommand(%+v) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
