package services

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
	"gopkg.in/yaml.v3"
)

// SyncPlan captures everything services_sync would do when applied. The
// command surface mirrors apps' SyncPlan structure: a body the CLI would
// send to either POST (create) or PATCH (update), plus a list of refused
// fields (local edits the conductor PATCH cannot accept).
type SyncPlan struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	CID  string `json:"cid"`

	// CreateBody is non-nil when local has no id; sync will POST to
	// services/<type>/add.
	CreateBody map[string]any `json:"createBody,omitempty"`

	// PatchBody is non-nil when local has an id; sync will PATCH
	// services/<type>/{id}/update. Contains every key the local yaml
	// declares that the manifest's update endpoint accepts. Conductor's
	// per-type omit-equals-preserve / omit-equals-clear rules apply on
	// the server side; the CLI does not need to encode them.
	PatchBody map[string]any `json:"patchBody,omitempty"`

	// Refused enumerates keys present in the local yaml that the
	// relevant write endpoint doesn't accept. Typical cases: a yaml
	// edits an immutable-after-create field (mysql storageMb,
	// maxConnections, storageClass) or a read-only field returned by
	// show. Each entry is a one-line human-readable string.
	Refused []string `json:"refused,omitempty"`

	// Diff is a unified-diff rendering of (local yaml) → (server state),
	// used by the plan output and the dry-run preview. Empty when there
	// is no drift.
	Diff string `json:"diff,omitempty"`
}

// HasChanges reports whether applying the plan would touch the cluster.
func (p *SyncPlan) HasChanges() bool {
	return len(p.CreateBody) > 0 || len(p.PatchBody) > 0
}

// ComputeSyncPlan diffs local against server and produces a SyncPlan. No
// I/O is performed; callers fetch server state and pass it in.
//
// When local.ID is empty, the plan is a CREATE: CreateBody is populated
// from local.Fields filtered through addCmd's input field set.
//
// When local.ID is non-empty, the plan is a PATCH: PatchBody is populated
// from local.Fields filtered through updateCmd's input field set, but
// only when the marshalled local and server forms actually differ (no
// drift means no PATCH).
//
// Either way, any local field that the relevant write endpoint doesn't
// accept AND that drifts from the server lands in Refused.
func ComputeSyncPlan(local *ServiceYAML, server *ServiceYAML, addCmd, updateCmd *manifest.Command) *SyncPlan {
	plan := &SyncPlan{
		Type: local.Type,
		ID:   local.ID,
		CID:  local.CID,
	}

	if local.ID == "" {
		plan.CreateBody = filterToInputFields(local.Fields, AddInputFieldNames(addCmd))
		plan.Refused = refusedDrift(local.Fields, nil, AddInputFieldNames(addCmd))
		return plan
	}

	// Update path: drift first, only PATCH when something actually changed.
	if !servicesEqual(local, server) {
		plan.PatchBody = filterToInputFields(local.Fields, UpdateInputFieldNames(updateCmd))
		var serverFields map[string]any
		if server != nil {
			serverFields = server.Fields
		}
		plan.Refused = refusedDrift(local.Fields, serverFields, UpdateInputFieldNames(updateCmd))
		plan.Diff = renderFieldDiff(local, server)
	}
	return plan
}

// servicesEqual reports whether the two service yamls would marshal
// byte-identical at a value level (numeric int vs float64 differences
// from yaml/json decoding are normalised). Header fields ignored: the
// CLI never PATCHes type/cid/aid (they're identity, not state).
func servicesEqual(a, b *ServiceYAML) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID {
		return false
	}
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for k, av := range a.Fields {
		bv, ok := b.Fields[k]
		if !ok {
			return false
		}
		if !jsonEqual(av, bv) {
			return false
		}
	}
	return true
}

// filterToInputFields returns the subset of fields whose key is in
// allowed. The output map is never nil so callers can range over it
// safely; an empty result still means "no PATCH body to send".
func filterToInputFields(fields map[string]any, allowed map[string]bool) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

// refusedDrift returns one-line messages for every field in local that
// is NOT in allowed AND whose value differs from server (or has no
// server counterpart, in the create case where server is nil).
//
// The intent is "tell the user the parts of their yaml that won't be
// reflected on the cluster", not "every key that's not patchable".
// Read-only fields the user pulled and never edited shouldn't show up.
//
// Equality is computed via JSON canonicalisation rather than DeepEqual:
// a yaml-decoded int and a JSON-decoded float64 of the same numeric
// value should compare equal here, since they round-trip through the
// same conductor wire format. Without this, every numeric field on a
// freshly-pulled yaml would falsely show up as refused.
func refusedDrift(local, server map[string]any, allowed map[string]bool) []string {
	var refused []string
	for k, lv := range local {
		if allowed[k] {
			continue
		}
		// Value matches server: not drift, no need to refuse.
		if server != nil {
			if sv, present := server[k]; present && jsonEqual(lv, sv) {
				continue
			}
		}
		refused = append(refused, fmt.Sprintf("%s: not patchable on this service type (read-only or immutable after create); change requires service recreation", k))
	}
	sort.Strings(refused)
	return refused
}

// jsonEqual compares two values by their canonical JSON encoding. Used
// in places where we want yaml-int and json-float64 to compare equal,
// e.g. when both sides came through different decoders for the same
// underlying conductor value.
func jsonEqual(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}
	return string(ab) == string(bb)
}

// FilterAllowed is exposed so callers (e.g. the cobra command) can
// shape a generic input map without re-importing the manifest helpers.
// Equivalent to filterToInputFields against UpdateInputFieldNames(cmd).
func FilterAllowed(fields map[string]any, allowed map[string]bool) map[string]any {
	return filterToInputFields(fields, allowed)
}

// ApplyResult is what ApplySyncPlan returns on success. JobID is the
// async job identifier the conductor returned (sync, like apps_sync, is
// fire-and-forget on the wire); NewID is set on the create path.
type ApplyResult struct {
	JobID string
	NewID string
}

// ApplySyncPlan executes the plan via the dynacmd Executor. POST for
// create (CreateBody non-empty), PATCH for update (PatchBody non-empty),
// no-op when neither is populated.
//
// On the create path, ApplySyncPlan does NOT save the new id back to
// disk; the caller (services_sync command) decides where to write,
// using the path it already opened the local yaml from.
func ApplySyncPlan(exec *dynacmd.Executor, plan *SyncPlan, addCmd, updateCmd *manifest.Command) (*ApplyResult, error) {
	if plan == nil || !plan.HasChanges() {
		return &ApplyResult{}, nil
	}
	if plan.CreateBody != nil {
		if addCmd == nil {
			return nil, fmt.Errorf("internal error: create plan but no add command")
		}
		respBody, err := exec.ExecuteWithInput(*addCmd, nil, plan.CreateBody, plan.CID)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", plan.Type, err)
		}
		var resp struct {
			ID    string `json:"id"`
			OSID  string `json:"osid"`
			JobID string `json:"jobId"`
		}
		_ = json.Unmarshal(respBody, &resp)
		newID := resp.ID
		if newID == "" {
			newID = resp.OSID
		}
		return &ApplyResult{JobID: resp.JobID, NewID: newID}, nil
	}
	if plan.PatchBody != nil {
		if updateCmd == nil {
			return nil, fmt.Errorf("internal error: patch plan but no update command")
		}
		// Pass id via the input map so buildEndpoint's `:id` placeholder
		// substitutes correctly regardless of whether the manifest
		// declares id as positional or as a flag-style field.
		// filterPathParamsFromBody strips it back out before the wire
		// body is built, so the PATCH JSON stays clean.
		input := make(map[string]any, len(plan.PatchBody)+1)
		for k, v := range plan.PatchBody {
			input[k] = v
		}
		input["id"] = plan.ID
		respBody, err := exec.ExecuteWithInput(*updateCmd, nil, input, plan.CID)
		if err != nil {
			return nil, fmt.Errorf("update %s/%s: %w", plan.Type, plan.ID, err)
		}
		var resp struct {
			JobID string `json:"jobId"`
		}
		_ = json.Unmarshal(respBody, &resp)
		return &ApplyResult{JobID: resp.JobID}, nil
	}
	return &ApplyResult{}, nil
}

// renderFieldDiff produces a unified diff between marshalled local and
// server yamls so the plan output matches the visual style of apps_diff.
// Marshal errors fall back to a placeholder string rather than abort
// because diff is informational; the actual PATCH/POST has its own
// error handling.
func renderFieldDiff(local, server *ServiceYAML) string {
	localBytes, err := yamlMarshal(local)
	if err != nil {
		return fmt.Sprintf("<unable to render local: %v>", err)
	}
	serverBytes, err := yamlMarshal(server)
	if err != nil {
		return fmt.Sprintf("<unable to render server: %v>", err)
	}
	return renderUnifiedDiff(localBytes, serverBytes, "local", "server")
}

// yamlMarshal is a small wrapper that returns empty bytes for a nil
// service yaml (rather than the zero-value rendering yaml.Marshal would
// produce). Used by renderFieldDiff so a nil server (e.g. service was
// deleted out from under the user) shows up as an empty server side.
func yamlMarshal(s *ServiceYAML) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	return yaml.Marshal(s)
}
