package dynacmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// Regression: pre-fix, `runos apps delete <id>` (and every services
// delete sibling) reached the wire on the first keystroke with no
// confirmation prompt and no --yes flag, mirroring `runos deploy`
// gives users a recoverable typo path. IsDestructiveCommand is the
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
		// Goal 23 review. evict-node runs `linstor node lost`, which drops every replica the node
		// held. It must demand --yes exactly like wipe-device.
		{"POST evict-node is destructive (evict- prefix)", manifest.Command{Command: "storage-groups/evict-node", Method: "POST"}, true},
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
		// Review 2 item 6. The matcher only tried the whole segment and its
		// leading token, so a destructive verb sitting in the MIDDLE or at
		// the END of a compound segment escaped both --yes and the MCP
		// confirm. Measured against the live 41.0.0 manifest: these two
		// commands were the only ones misclassified, and both destroy state.
		{"PATCH etcd-remove-member is destructive (infix verb)", manifest.Command{Command: "clusters/etcd-remove-member", Method: "PATCH"}, true},
		{"POST custom-rules-delete is destructive (trailing verb)", manifest.Command{Command: "services/prometheus/{id}/custom-rules-delete", Method: "POST"}, true},
		// Token equality, not substring: a verb is a whole hyphen-delimited
		// word. `list-deleted` reads records and must stay ungated.
		{"POST list-deleted is not destructive (token equality)", manifest.Command{Command: "services/minio/{id}/list-deleted", Method: "POST"}, false},
		{"POST undelete is not destructive (token equality)", manifest.Command{Command: "apps/{id}/undelete", Method: "POST"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDestructiveCommand(tc.cmd); got != tc.want {
				t.Errorf("IsDestructiveCommand(%+v) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// destructiveSummary lifts the resolved primary positional id into
// the confirmation prompt so a typo on the id is visible before the
// user answers y/N. Without a positional it lists the changed flags,
// and without those it says "no target given"; it never prints the
// manifest description.
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
	wipeDevice := manifest.Command{
		Command:     "storage-groups/wipe-device",
		Description: "Wipe a device. This is a very long description that must never be shown as the target.",
		Method:      "POST",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "nid", Type: "string", Required: true},
			{Name: "devicePath", Type: "string", Required: true},
		}},
	}
	bindGPU := manifest.Command{
		Command:     "maintenance-scripts/bind-gpu-vfio/run",
		Description: "Bind a GPU to vfio-pci. Long description.",
		Method:      "POST",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "NODE_NID", Type: "string", Required: true},
			{Name: "MODE", Type: "string"},
		}},
	}
	setData := manifest.Command{
		Command: "services/valkey/{id}/set-data",
		Method:  "PATCH",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true, Required: true},
			{Name: "key", Type: "string", Required: true},
			{Name: "value", Type: "string", Required: true},
		}},
	}
	execSQL := manifest.Command{
		Command: "services/postgresql/{id}/exec-sql",
		Method:  "PATCH",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true, Required: true},
			{Name: "query", Type: "string", Required: true},
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
		{name: "positional absent says no target", cmd: cmdWithID, args: []string{}, want: "apps delete (no target given)"},
		{name: "no positional fields and no flags says no target", cmd: noPositional, args: []string{}, want: "tools cleanup (no target given)"},
		{name: "first positional wins on multi", cmd: multiPositional, args: []string{"postgresql", "lw0vp"}, want: "type=postgresql"},
		{name: "flag-only cid surfaces in target (#131)", cmd: clusterReset, args: []string{}, flags: map[string]string{"cid": "mycluster3"}, want: "cid=mycluster3"},
		{name: "flag empty says no target", cmd: clusterReset, args: []string{}, flags: map[string]string{"cid": ""}, want: "clusters reset (no target given)"},
		// Goal 23 review. wipe-device has no positional field, so the prompt printed the 250-word
		// manifest description as the "target". The changed non-positional flags are the target.
		{name: "wipe-device lists changed flags in field order", cmd: wipeDevice, args: []string{}, flags: map[string]string{"device-path": "/dev/sdb", "nid": "n1abc"}, want: "nid=n1abc device-path=/dev/sdb"},
		{name: "wipe-device with only nid lists nid", cmd: wipeDevice, args: []string{}, flags: map[string]string{"nid": "n1abc", "device-path": ""}, want: "nid=n1abc"},
		{name: "maintenance script names the node via --nid", cmd: bindGPU, args: []string{}, flags: map[string]string{"nid": "n1abc", "mode": ""}, want: "nid=n1abc"},
		{name: "maintenance script with two flags", cmd: bindGPU, args: []string{}, flags: map[string]string{"nid": "n1abc", "mode": "bind"}, want: "nid=n1abc mode=bind"},
		// Adversarial review, finding 4: the flag list printed every value, so `set-data --value
		// SECRET` and `exec-sql --query "... password ..."` echoed the secret to the terminal.
		// Secret-shaped field names are redacted; ids, paths and hostnames stay readable.
		{name: "set-data redacts value and keeps the key", cmd: setData, args: []string{}, flags: map[string]string{"key": "db-pass", "value": "hunter2"}, want: "key=db-pass value=<redacted>"},
		{name: "exec-sql redacts the query", cmd: execSQL, args: []string{}, flags: map[string]string{"query": "UPDATE users SET password='x'"}, want: "query=<redacted>"},
		{name: "wipe-device shows nid and device path in full", cmd: wipeDevice, args: []string{}, flags: map[string]string{"nid": "n1abc", "device-path": "/dev/sdb"}, want: "nid=n1abc device-path=/dev/sdb"},
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

// Adversarial review, finding 5: the positional-by-flag lookup used GetString, which errors on
// an Int flag, so `account notify-keys delete --id 42` read as "no target given". The summary
// reads the flag's rendered value whatever its type.
func TestDestructiveSummary_IntPositionalFlag(t *testing.T) {
	notifyKeyDelete := manifest.Command{
		Command: "account/notify-keys/{id}/delete",
		Method:  "DELETE",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "integer", Positional: true, Required: true},
		}},
	}
	c := &cobra.Command{Use: "x"}
	c.Flags().Int("id", 0, "")
	if err := c.Flags().Set("id", "42"); err != nil {
		t.Fatalf("seed flag: %v", err)
	}
	if got, want := destructiveSummary(c, notifyKeyDelete, nil), "id=42"; got != want {
		t.Errorf("destructiveSummary int --id = %q, want %q", got, want)
	}
	// An unset int flag renders "0", which is not a target.
	c2 := &cobra.Command{Use: "x"}
	c2.Flags().Int("id", 0, "")
	if got, want := destructiveSummary(c2, notifyKeyDelete, nil), "account notify-keys delete (no target given)"; got != want {
		t.Errorf("destructiveSummary unset int --id = %q, want %q", got, want)
	}
}

// TestRedactedFlagName pins the secret-shaped field names the summary redacts (finding 4).
func TestRedactedFlagName(t *testing.T) {
	for _, name := range []string{"value", "query", "password", "secret", "token", "api-key", "apiKey", "credential", "credentials", "clientSecret", "sshKey", "read-write-password"} {
		if !redactedFlagName(name) {
			t.Errorf("redactedFlagName(%q) = false, want true", name)
		}
	}
	// A bare `key` is a name (Valkey key, build-arg key, tag key), never a secret.
	for _, name := range []string{"nid", "id", "device-path", "hostname", "cid", "keyspace", "mode", "name", "key"} {
		if redactedFlagName(name) {
			t.Errorf("redactedFlagName(%q) = true, want false", name)
		}
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
			if got := IsDestructiveCommand(tc.cmd); got != tc.want {
				t.Errorf("IsDestructiveCommand(%+v) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// countingNodeRead answers every node read with a name and counts the
// requests, so a test can prove that a command made NO lookup. The counter
// is atomic because an abandoned worker can still be in flight.
func countingNodeRead(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"name":"node-alpha"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// destructiveSummary sits on the prompt path for EVERY destructive
// command, so the commands that display some other field first must print
// exactly the line they printed before, and must make no lookup at all.
//
// Measured against the live manifest, three gated commands stand for that
// group: storage-groups remove-node displays the storage group id first,
// storage-groups evict-node carries no positional field, and clusters
// networks leave displays the cluster id first.
func TestDestructiveSummary_NonNodeTargetsUnchanged(t *testing.T) {
	removeNode := manifest.Command{
		Command: "storage-groups/{gid}/remove-node",
		Method:  "POST",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "gid", Type: "string", Positional: true, Required: true},
			{Name: "nid", Type: "string", Required: true},
		}},
	}
	evictNode := manifest.Command{
		Command: "storage-groups/evict-node",
		Method:  "POST",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "gid", Type: "string", Required: true},
			{Name: "nid", Type: "string", Required: true},
		}},
	}
	networksLeave := manifest.Command{
		Command: "clusters/{cid}/networks/{networkId}/leave",
		Method:  "POST",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "cid", Type: "string", Positional: true, Required: true},
			{Name: "networkId", Type: "string", Positional: true, Required: true},
		}},
	}
	setData := manifest.Command{
		Command: "services/valkey/{id}/set-data",
		Method:  "PATCH",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "key", Type: "string", Required: true},
			{Name: "value", Type: "string", Required: true},
		}},
	}

	srv, requests := countingNodeRead(t)
	useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

	cases := []struct {
		name  string
		cmd   manifest.Command
		args  []string
		flags map[string]string
		want  string
	}{
		{name: "remove-node shows the storage group id", cmd: removeNode, args: []string{"sg-1"}, flags: map[string]string{"nid": "node-1"}, want: "gid=sg-1"},
		{name: "evict-node lists its changed flags", cmd: evictNode, flags: map[string]string{"gid": "sg-1", "nid": "node-1"}, want: "gid=sg-1 nid=node-1"},
		{name: "networks leave shows the cluster id", cmd: networksLeave, args: []string{"cluster1", "net-1"}, want: "cid=cluster1"},
		{name: "a secret shaped flag still prints the redacted marker", cmd: setData, flags: map[string]string{"key": "db-pass", "value": "hunter2"}, want: "key=db-pass value=<redacted>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c *cobra.Command
			if tc.flags != nil {
				c = &cobra.Command{Use: "x"}
				for name, value := range tc.flags {
					c.Flags().String(name, "", "")
					if err := c.Flags().Set(name, value); err != nil {
						t.Fatalf("seed the flag %s: %v", name, err)
					}
				}
			}
			if got := destructiveSummary(c, tc.cmd, tc.args); got != tc.want {
				t.Errorf("target = %q, want %q", got, tc.want)
			}
		})
	}
	if count := requests.Load(); count != 0 {
		t.Errorf("the non node commands made %d node reads, want 0", count)
	}
}

// The prompt and the non interactive refusal carry the SAME target text,
// because confirmDestructive builds that text once. The refusal path
// therefore names the node too.
//
// The skip flag returns before that text is built, so the skip path makes
// no lookup at all. Test binaries run without a terminal on stdin, so the
// refusal branch is the branch this test reaches.
func TestConfirmDestructive_NodeNameInTheRefusal(t *testing.T) {
	nodesDeleteWithYes := func() *cobra.Command {
		c := &cobra.Command{Use: "delete"}
		c.Flags().BoolP("yes", "y", false, "skip the destructive-action confirmation prompt")
		return c
	}

	t.Run("the refusal carries the node id and the node name", func(t *testing.T) {
		srv, requests := countingNodeRead(t)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

		err := confirmDestructive(nodesDeleteWithYes(), nodesDelete, []string{testNodeID})
		if err == nil {
			t.Fatal("a run with no terminal and no --yes must refuse")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("refusal %q should name the --yes flag", err)
		}
		target := destructiveSummary(nil, nodesDelete, []string{testNodeID})
		if target != "nid=node-1 name=node-alpha" {
			t.Fatalf("the prompt target = %q, want the node id and the node name", target)
		}
		if !strings.Contains(err.Error(), "(target: "+target+")") {
			t.Errorf("refusal %q should carry the prompt target %q", err, target)
		}
		if count := requests.Load(); count != 2 {
			t.Errorf("made %d node reads, want 2 (one for the refusal, one for the comparison)", count)
		}
	})

	t.Run("the skip flag makes no lookup", func(t *testing.T) {
		srv, requests := countingNodeRead(t)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

		c := nodesDeleteWithYes()
		if err := c.ParseFlags([]string{"--yes"}); err != nil {
			t.Fatalf("parse the flags: %v", err)
		}
		if err := confirmDestructive(c, nodesDelete, []string{testNodeID}); err != nil {
			t.Fatalf("--yes must proceed, got %v", err)
		}
		if count := requests.Load(); count != 0 {
			t.Errorf("the skip path made %d node reads, want 0", count)
		}
	})
}
