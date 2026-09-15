package dynacmd

import (
	"encoding/json"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// What the reinstall verb actually puts on the wire.
//
// MEASURED DEFECT THIS EXISTS FOR: a reinstall run with `--ssh-key-ids <id>`
// produced a machine carrying the account's DEFAULT key set instead of the named
// key. Conductor reads `sshKeyIds` only when it arrives as a JSON array, and it
// falls back to the default set otherwise, so a body that carries the ids in any
// other shape is accepted, provisions a machine, and writes the wrong keys. The
// failure is silent: nothing errors and the machine boots.
//
// The field is declared `type: array, format: string_array` with NO itemType,
// which is the shape the coercion path treats least like every other array, so
// it is pinned here rather than assumed from the general array tests.
func reinstallCommandDef() manifest.Command {
	return manifest.Command{
		Command:  "tenant/servers/reinstall",
		Endpoint: "/:aid/tenant/servers/{sid}/reinstall",
		Method:   "POST",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "sid", Type: "string", Required: true},
				{Name: "joinCid", Type: "string"},
				{Name: "joinAs", Type: "string", Enum: []string{"auto", "control-plane", "worker"}},
				{Name: "sshKeyIds", Type: "array", Format: "string_array"},
			},
			Flags: []manifest.Flag{{Name: "acknowledgeDataLoss"}},
		},
	}
}

func runReinstall(t *testing.T, set func(leaf *leafFlags)) map[string]any {
	t.Helper()
	def := reinstallCommandDef()
	m := &manifest.Manifest{Version: "test", Commands: []manifest.Command{def}}

	var sent []byte
	srv := bodyRecordingStub(t, &sent)
	warnEnv(t, localhostURL(srv.URL))

	top := NewBuilder(m, NewExecutor(localhostURL(srv.URL))).BuildCommands()
	leaf := findLeafCommand(t, top, def.Command)
	set(&leafFlags{t: t, leaf: leaf})

	exec := NewExecutor(localhostURL(srv.URL))
	if err := exec.Execute(leaf, nil, def); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("request body is not JSON: %v (%q)", err, sent)
	}
	return body
}

func TestReinstallSendsSshKeyIdsAsAnArray(t *testing.T) {
	body := runReinstall(t, func(f *leafFlags) {
		f.set("sid", "aaa11")
		f.set("acknowledge-data-loss", "true")
		f.set("ssh-key-ids", "bbb22")
	})

	raw, present := body["sshKeyIds"]
	if !present {
		t.Fatalf("sshKeyIds is missing from the body; conductor then writes the account default set. body=%v", body)
	}
	ids, ok := raw.([]any)
	if !ok {
		t.Fatalf("sshKeyIds went out as %T (%v); conductor requires a JSON array and silently falls back to the default set for anything else", raw, raw)
	}
	if len(ids) != 1 || ids[0] != "bbb22" {
		t.Fatalf("sshKeyIds = %v, want [bbb22]", ids)
	}
}

// Two keys, given as two flags, must arrive as two elements. A body that joins
// them into one string reaches conductor as a single unknown key id, which it
// refuses, so this one fails loudly rather than silently. Pinned anyway: the
// repeatable form is the documented one.
func TestReinstallSendsEveryRepeatedSshKeyId(t *testing.T) {
	body := runReinstall(t, func(f *leafFlags) {
		f.set("sid", "aaa11")
		f.set("acknowledge-data-loss", "true")
		f.set("ssh-key-ids", "bbb22")
		f.set("ssh-key-ids", "ccc33")
	})
	ids, ok := body["sshKeyIds"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "bbb22" || ids[1] != "ccc33" {
		t.Fatalf("sshKeyIds = %v, want [bbb22 ccc33]", body["sshKeyIds"])
	}
}

// Naming no keys must leave the field OUT, not send an empty array. The two are
// different requests at conductor: absent means "use the account default set",
// and an empty list would mean "write no account key at all".
func TestReinstallOmitsSshKeyIdsWhenTheFlagIsUnused(t *testing.T) {
	body := runReinstall(t, func(f *leafFlags) {
		f.set("sid", "aaa11")
		f.set("acknowledge-data-loss", "true")
	})
	if raw, present := body["sshKeyIds"]; present {
		t.Fatalf("sshKeyIds = %v was sent with the flag unused; that is a different request from omitting it", raw)
	}
}

// A tiny setter so each test reads as the command line it stands for.
type leafFlags struct {
	t    *testing.T
	leaf *cobra.Command
}

func (f *leafFlags) set(name, value string) {
	f.t.Helper()
	if err := f.leaf.Flags().Set(name, value); err != nil {
		f.t.Fatalf("set --%s=%s: %v", name, value, err)
	}
}
