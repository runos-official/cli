package services

import (
	"encoding/json"
	"fmt"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// classCoupledFields are the cpu/memory/replica values a non-custom
// resource class encapsulates server-side. The API exposes them on the
// update endpoint so they CAN be overridden, but a server-stored class
// other than "custom" means the values are the class's, not the user's.
// Pulling them back into yaml in that case would re-pin derived values
// the next sync would either echo (no-op) or accidentally flip the
// class to "custom" (see services topic, "Resource class + overrides").
//
// Mirrors the apps_pull preset-wins gate in internal/apps/pull.go.
var classCoupledFields = []string{
	"replicas",
	"cpuRequestMc",
	"cpuLimitMc",
	"memoryRequestMb",
	"memoryLimitMb",
}

// Pull fetches the current server state of a single service via the
// services/<type>/{id}/show endpoint and projects it into a ServiceYAML.
//
// The yaml is desired-state only: only fields the user can actually
// change end up on disk. Pull looks up the manifest's add + update
// commands for the type and uses their Input.Fields / Input.Flags as the
// allow-list. Audit fields (createdAt, updatedAt), computed fields
// (cpuLimitMc derived from the resource class, internalEndpoint),
// operational state (_slow, advanced), and read-only flag subkeys all
// land in the show response but are filtered out here.
//
// Conductor changes flow through automatically: a new field added to
// update.Input.Fields appears in the yaml on the next pull; an audit
// field added to show but not to add/update is dropped without a CLI
// release.
func Pull(exec *dynacmd.Executor, m *manifest.Manifest, serviceType, cid, aid, sid string) (*ServiceYAML, error) {
	if !IsSupportedType(m, serviceType) {
		return nil, fmt.Errorf("service type %q is not supported by the current manifest (known types: %v)", serviceType, ListSupportedTypes(m))
	}
	if sid == "" {
		return nil, fmt.Errorf("service id is required")
	}
	showCmd, err := ShowCommand(m, serviceType)
	if err != nil {
		return nil, err
	}
	addCmd, err := AddCommand(m, serviceType)
	if err != nil {
		return nil, err
	}
	updateCmd, err := UpdateCommand(m, serviceType)
	if err != nil {
		return nil, err
	}

	// id can be either a positional arg or a flag-style input depending
	// on how the manifest declares it. Pass it as an input map entry so
	// buildEndpoint's `:id` substitution finds it either way.
	respBody, err := exec.ExecuteWithInput(*showCmd, nil, map[string]any{"id": sid}, cid)
	if err != nil {
		return nil, fmt.Errorf("fetch %s/%s: %w", serviceType, sid, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("parse show response: %w", err)
	}

	return BuildPulledService(raw, serviceType, cid, aid, sid, addCmd, updateCmd), nil
}

// BuildPulledService projects a raw show response into the on-disk
// ServiceYAML shape, restricted to fields the user can actually set:
// the union of addCmd and updateCmd's Input.Fields, plus addCmd's
// Input.Flags (nested under `flags:` to mirror the wire shape).
//
// Pure function (no I/O); separated from Pull so tests can drive it
// with synthesized JSON without an httptest server.
//
// Header fields (type/cid/aid) live in the typed struct and are never
// pulled from the show response. id is taken from the response if
// present, falling back to the sid the caller asked for.
func BuildPulledService(raw map[string]any, serviceType, cid, aid, sid string, addCmd, updateCmd *manifest.Command) *ServiceYAML {
	out := &ServiceYAML{
		Type:   serviceType,
		ID:     stringField(raw, "id", sid),
		CID:    cid,
		AID:    aid,
		Fields: map[string]any{},
	}

	// Settable scalar fields = add.Input.Fields ∪ update.Input.Fields,
	// minus path-param positionals (id) and minus the typed header
	// fields (in case some future manifest declares them as inputs).
	settable := map[string]bool{}
	collectSettable := func(cmd *manifest.Command) {
		if cmd == nil || cmd.Input == nil {
			return
		}
		for _, f := range cmd.Input.Fields {
			if f.Positional {
				continue
			}
			if f.Name == "id" || f.Name == "type" || f.Name == "cid" || f.Name == "aid" {
				continue
			}
			settable[f.Name] = true
		}
	}
	collectSettable(addCmd)
	collectSettable(updateCmd)

	for k := range settable {
		if v, ok := raw[k]; ok {
			out.Fields[k] = v
		}
	}

	// Preset-wins: when the stored class is a real class (not "custom"
	// or empty), strip the cpu/memory/replica fields the class supplies
	// from the projection. Otherwise the yaml records the class AND the
	// derived values, which round-trips as drift and (on sync) would
	// flip the server-stored class to "custom" because the API treats
	// any class-plus-override submission as a custom config.
	if classID, _ := out.Fields["resourceRequirementClassId"].(string); classID != "" && classID != "custom" {
		for _, f := range classCoupledFields {
			delete(out.Fields, f)
		}
	}

	// Flags: when addCmd declares Input.Flags (e.g. valkey `secured`),
	// the show response nests them under "flags". Copy only the flag
	// names the manifest declares; drop everything else under flags
	// (e.g. read-only postgres flags like apacheAge / vector that the
	// user can't toggle from yaml).
	if addCmd != nil && addCmd.Input != nil && len(addCmd.Input.Flags) > 0 {
		if rawFlags, ok := raw["flags"].(map[string]any); ok {
			kept := map[string]any{}
			for _, fl := range addCmd.Input.Flags {
				if v, present := rawFlags[fl.Name]; present {
					kept[fl.Name] = v
				}
			}
			if len(kept) > 0 {
				out.Fields["flags"] = kept
			}
		}
	}

	return out
}

// stringField returns raw[key] as a string, or fallback when absent or
// not a string. Used so Pull can survive a server response that omits
// the id (it falls back to the id the caller asked for).
func stringField(raw map[string]any, key, fallback string) string {
	if v, ok := raw[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
