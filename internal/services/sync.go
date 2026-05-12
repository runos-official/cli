package services

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

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

	// ServerRRC is the resourceRequirementClassId currently stored on
	// the service (empty for CREATE flows). Used by the custom-synthesis
	// hint to detect implicit RRC flips on PATCH (a body containing only
	// an override field flips named->custom server-side when the stored
	// class is named). Regression target: I9-H.
	ServerRRC string `json:"-"`
}

// HasChanges reports whether applying the plan would touch the cluster.
func (p *SyncPlan) HasChanges() bool {
	return len(p.CreateBody) > 0 || len(p.PatchBody) > 0
}

// RedactSecrets replaces every value in CreateBody / PatchBody whose
// key looks like a secret (password, secret, token, apikey, ...) with
// the "<redacted>" marker. Non-secret config (name, resource class
// enum, capacity, replicas) stays legible. Used by the JSON dry-run
// path so the sync-plan JSON doesn't leak credentials into LLM context
// when invoked through the MCP wrapper.
//
// The text plan rendering already redacts via isSensitiveKey at print
// time; this method mirrors that for the JSON shape. Mutates in place;
// nil-safe.
//
// Regression target: I10-I client-side parity + I10-M-style
// secrets-in-json safety on the services surface.
func (p *SyncPlan) RedactSecrets() {
	if p == nil {
		return
	}
	redactBodyValues(p.CreateBody)
	redactBodyValues(p.PatchBody)
}

func redactBodyValues(body map[string]any) {
	for k := range body {
		if looksSensitive(k) {
			body[k] = "<redacted>"
		}
	}
}

// looksSensitive reports whether a service field name conventionally
// holds sensitive data. Mirrors the cmd/services_sync.go gate so the
// JSON path and the text-render path use the same heuristic.
func looksSensitive(k string) bool {
	lower := strings.ToLower(k)
	for _, marker := range []string{"password", "secret", "token", "apikey", "api_key", "credential", "privatekey", "private_key"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
// accept AND that drifts from the server lands in Refused. showCmd is
// consulted to split "unknown field, typo?" from "known but read-only /
// immutable after create" in the refused message (I9-G); pass nil to
// skip the split and use the generic immutable wording.
func ComputeSyncPlan(local *ServiceYAML, server *ServiceYAML, addCmd, updateCmd, showCmd *manifest.Command) *SyncPlan {
	plan := &SyncPlan{
		Type: local.Type,
		ID:   local.ID,
		CID:  local.CID,
	}

	knownFields := showFieldNames(showCmd)

	if local.ID == "" {
		// Lift any nested `flags: {X: true}` mapping into top-level keys
		// matching the addCmd's declared Input.Flags. Pull writes flags
		// nested to mirror the conductor's show response shape, but the
		// wire body for CREATE expects each flag at the top level (per
		// dynacmd's executor flag handling). Without the lift,
		// `filterToInputFields` would drop the entire `flags` block and
		// `refusedDrift` would surface it as refused. Regression target:
		// I9-I (flags half).
		liftedLocal := liftServiceFlags(local.Fields, addCmd)
		plan.CreateBody = filterToInputFields(liftedLocal, AddInputFieldNames(addCmd))
		plan.Refused = refusedDrift(liftedLocal, nil, AddInputFieldNames(addCmd), true, knownFields)
		return plan
	}

	// Update path: drift first, only PATCH when something actually changed.
	if !servicesEqual(local, server) {
		var serverFields map[string]any
		if server != nil {
			serverFields = server.Fields
		}
		plan.PatchBody = computeDriftPatch(local.Fields, serverFields, UpdateInputFieldNames(updateCmd))
		plan.Refused = refusedDrift(local.Fields, serverFields, UpdateInputFieldNames(updateCmd), false, knownFields)
		plan.Diff = renderFieldDiff(local, server)
		if server != nil {
			if rrc, ok := server.Fields["resourceRequirementClassId"].(string); ok {
				plan.ServerRRC = rrc
			}
		}
	}
	return plan
}

// showFieldNames returns the set of field names the show endpoint emits,
// or nil when showCmd is nil. Used by refusedDrift to split "unknown
// field, typo?" from "known but read-only / immutable after create" in
// the refusal message (I9-G).
func showFieldNames(showCmd *manifest.Command) map[string]bool {
	if showCmd == nil {
		return nil
	}
	names := ShowOutputFields(showCmd)
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// customSynthesisOverrideFields are the cpu/memory/replica fields that,
// when submitted alongside a named (non-custom) resourceRequirementClassId,
// flip the server-stored class to `custom` and materialise the named
// class's other defaults. The conductor's "Resource class + overrides"
// rule treats every class-plus-override submission as a custom config;
// the user often doesn't expect this until the NEXT pull reveals the
// flip. Used by CustomSynthesisHint to give a heads-up at plan time.
//
// Regression target: I9-H.
var customSynthesisOverrideFields = []string{
	"replicas",
	"cpuRequestMc",
	"cpuLimitMc",
	"memoryRequestMb",
	"memoryLimitMb",
}

// CustomSynthesisHint returns a one-line warning when applying body
// against serverRRC would flip the server-stored resource class from a
// named class to `custom`. Two flip paths trigger the hint:
//
//  1. CREATE / explicit re-submit: body itself carries both a named
//     (non-custom, non-empty) `resourceRequirementClassId` AND one or
//     more class-coupled override fields (replicas / cpu* / memory*).
//  2. Implicit flip on PATCH: body changes only an override field, but
//     the server-stored class is named — conductor's synthesis rule
//     treats the override as flipping RRC to `custom`.
//
// serverRRC is the resourceRequirementClassId currently stored on the
// service (pass "" for CREATE flows or when unknown — case 2 then
// degrades to a no-op).
//
// Mirrors the iter-6 I6-F pattern: the conductor emits an
// `rrcFlipHint` on the `apps_update` response when an override flips
// RRC named→custom. Services don't emit that hint server-side, so the
// CLI surfaces it at plan time. Regression target: I9-H.
func CustomSynthesisHint(body map[string]any, serverRRC string) string {
	if len(body) == 0 {
		return ""
	}
	var overrides []string
	for _, k := range customSynthesisOverrideFields {
		if _, has := body[k]; has {
			overrides = append(overrides, k)
		}
	}
	if len(overrides) == 0 {
		return ""
	}
	bodyRRC, _ := body["resourceRequirementClassId"].(string)
	effectiveRRC := bodyRRC
	if effectiveRRC == "" {
		effectiveRRC = serverRRC
	}
	if effectiveRRC == "" || effectiveRRC == "custom" {
		return ""
	}
	return fmt.Sprintf(
		"Note: this submission combines resourceRequirementClassId=%q with override field(s) %v. The conductor will store resourceRequirementClassId='custom' on the service and materialise %q's other defaults; the next 'services pull' will write 'custom' back into the yaml. To stay on %q, drop the override(s); to pin this configuration cleanly, set resourceRequirementClassId='custom' explicitly.",
		effectiveRRC, overrides, effectiveRRC, effectiveRRC,
	)
}

// liftServiceFlags returns a copy of local with any nested
// `flags: {X: true, Y: false}` mapping expanded into top-level keys
// when those keys are declared in addCmd.Input.Flags. Pull writes flags
// nested to mirror the show response shape, but the wire body for the
// add endpoint expects each flag at the top level. Keys not declared in
// Input.Flags stay nested under `flags` so they surface as refused (a
// typo'd flag name shouldn't silently disappear).
//
// Returns local unchanged when there's no `flags` key, when addCmd has
// no Input.Flags, or when the flags value isn't a map (defensive: a
// hand-edited yaml might shape it as a list of strings).
//
// Regression target: I9-I (flags half).
func liftServiceFlags(local map[string]any, addCmd *manifest.Command) map[string]any {
	if local == nil || addCmd == nil || addCmd.Input == nil || len(addCmd.Input.Flags) == 0 {
		return local
	}
	rawFlags, ok := local["flags"].(map[string]any)
	if !ok || len(rawFlags) == 0 {
		return local
	}
	known := make(map[string]bool, len(addCmd.Input.Flags))
	for _, fl := range addCmd.Input.Flags {
		known[fl.Name] = true
	}
	out := make(map[string]any, len(local)+len(rawFlags))
	for k, v := range local {
		if k == "flags" {
			continue
		}
		out[k] = v
	}
	leftover := map[string]any{}
	for k, v := range rawFlags {
		if known[k] {
			out[k] = v
			continue
		}
		leftover[k] = v
	}
	if len(leftover) > 0 {
		out["flags"] = leftover
	}
	return out
}

// computeDriftPatch returns the local-vs-server drift restricted to fields
// the update command accepts. Fields whose local value matches server are
// omitted so the conductor's per-service omit=preserve handlers don't
// mis-synthesize overrides.
//
// Concrete bug this prevents: pulling a service materializes the active
// class's cpu/memory baseline into the local yaml. Editing only the class
// line and syncing without this gate sends the OLD baseline cpu/memory in
// the wire body. resolveRRC interprets those as overrides against the NEW
// class baseline, flips class to "custom", and the new class baseline
// never reaches the running pod.
func computeDriftPatch(local, server map[string]any, allowed map[string]bool) map[string]any {
	if len(local) == 0 {
		return nil
	}
	out := make(map[string]any)
	for k, lv := range local {
		if !allowed[k] {
			continue
		}
		if server != nil {
			if sv, present := server[k]; present && jsonEqual(lv, sv) {
				continue
			}
		}
		out[k] = lv
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
// `knownFields` is the set of field names the show endpoint emits (or
// nil to skip the split). When non-nil, the refused message splits on
// whether the field is known-but-immutable (in knownFields) or
// unknown-typo (not in knownFields). Pre-fix every refusal said
// "requires service recreation", which actively misled users who'd
// simply typo'd a field name. Regression target: I9-G.
//
// Equality is computed via JSON canonicalisation rather than DeepEqual:
// a yaml-decoded int and a JSON-decoded float64 of the same numeric
// value should compare equal here, since they round-trip through the
// same conductor wire format. Without this, every numeric field on a
// freshly-pulled yaml would falsely show up as refused.
func refusedDrift(local, server map[string]any, allowed map[string]bool, isCreate bool, knownFields map[string]bool) []string {
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
		known := knownFields != nil && knownFields[k]
		// Distinct wording for CREATE vs PATCH: "not patchable" was
		// misleading on the CREATE path (no service exists yet to
		// patch), where the refusal really means "the add endpoint
		// doesn't accept this field, drop it from the yaml or set
		// it via a service-specific writer post-create".
		var msg string
		switch {
		case isCreate && known:
			msg = fmt.Sprintf("%s: not accepted by this service type's add endpoint (read-only on creation; set via the service's dedicated writer post-create if needed)", k)
		case isCreate:
			msg = fmt.Sprintf("%s: not a recognised field on this service type's add endpoint; did you typo the name? Remove from yaml or check the manifest for the correct spelling", k)
		case known:
			msg = fmt.Sprintf("%s: not patchable on this service type (read-only or immutable after create); change requires service recreation", k)
		default:
			msg = fmt.Sprintf("%s: not a recognised field on this service type; did you typo the name? Remove from yaml or check the manifest for the correct spelling", k)
		}
		refused = append(refused, msg)
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
			// Conductor returns `osid` on the create response (e.g.
			// "minio-v2k5k") and not always `id`. The service yaml's
			// `id:` field uses the bare 5-char id, so strip the
			// `<type>-` prefix when falling back. Without this strip
			// the yaml gets the OSID written into `id:` and every
			// subsequent diff/pull/sync 404s ("Service not found")
			// until the user fixes it by hand.
			if osid := resp.OSID; osid != "" {
				prefix := plan.Type + "-"
				if len(osid) > len(prefix) && osid[:len(prefix)] == prefix {
					newID = osid[len(prefix):]
				} else {
					newID = osid
				}
			}
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
