package apps

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/envfile"
	"gopkg.in/yaml.v3"
)

// OmitClearFields lists the top-level dotted field names the
// conductor's PATCH /apps/:id endpoint treats as desired-state with
// omit-equals-clear semantics. A local yaml that omits any of these
// wipes the server-side value on push. Every other top-level field is
// omit-equals-preserve unless it matches a nested-path pattern in
// IsOmitClearPath (see below).
//
// Canonical source: see computeYAMLPatch + buildFullYAMLBody in this
// file. `domain` is included because conductor's reconciler removes
// the mapping (and any provider-managed DNS record) when local omits
// it, even though the actual delete happens via the domains/services
// orchestration rather than the apps PATCH. Surfacing it here keeps
// the pre-deploy "will be cleared" classification honest.
var OmitClearFields = []string{
	"healthCheck",
	"healthCheckPort",
	"healthCheckPath",
	"metricsPort",
	"metricsPath",
	"domain",
}

// IsOmitClearField reports whether the named top-level yaml field
// would be cleared on a PATCH that omits it. Matches against the
// top-level-only dotted-path summaries listServerOnlyFields produces.
func IsOmitClearField(name string) bool {
	return slices.Contains(OmitClearFields, name)
}

// IsOmitClearPath reports whether a full dotted-path summary (as
// emitted by listServerOnlyFields, e.g. "servicePortMappings[0].domains
// (1 entry)") describes a field with omit-equals-clear semantics. Used
// alongside IsOmitClearField to catch nested cases the top-level check
// misses, namely:
//
//   - servicePortMappings[N].domains : per-mapping custom-domain list,
//     same omit-deletes contract as top-level `domain:`. Without this
//     match the partition lumps a removed `servicePortMappings[].domains`
//     entry under "Preserved server-side" while the conductor goes
//     ahead and removes the mapping.
//
// The function is intentionally pattern-matched rather than regex-
// driven so future additions stay surveyable.
func IsOmitClearPath(path string) bool {
	// servicePortMappings[N].domains{,.<sub>} — any path that walks
	// through a mapping's `domains` array.
	if strings.HasPrefix(path, "servicePortMappings[") {
		if i := strings.IndexByte(path, ']'); i >= 0 && i+1 < len(path) {
			rest := path[i+1:]
			// rest looks like ".domains" or ".domains[0].fqdn (...)".
			if strings.HasPrefix(rest, ".domains") {
				return true
			}
		}
	}
	return false
}

// PartitionServerOnlyByClearSemantics splits a list of server-only
// fields (the output of listServerOnlyFields) into the two buckets the
// pre-deploy warning needs: clearOnOmit (will be wiped on push) vs
// preserveOnOmit (left alone server-side). Order within each bucket
// preserves the input order so the warning's bulleted list still
// matches the diff's display order.
//
// A field is classified clearOnOmit when EITHER its top-level name is
// in OmitClearFields OR its full path matches IsOmitClearPath.
func PartitionServerOnlyByClearSemantics(serverOnly []string) (clearOnOmit, preserveOnOmit []string) {
	for _, f := range serverOnly {
		topLevel := f
		if i := strings.IndexAny(f, ". ["); i >= 0 {
			topLevel = f[:i]
		}
		if IsOmitClearField(topLevel) || IsOmitClearPath(f) {
			clearOnOmit = append(clearOnOmit, f)
		} else {
			preserveOnOmit = append(preserveOnOmit, f)
		}
	}
	return
}

// IsOrchestrationRemovedPath reports whether a server-only field path
// (as produced by listServerOnlyFields) describes state the conductor's
// deploy orchestration removes via a step OTHER than the apps PATCH —
// so the pre-deploy gate's "Preserved server-side / WILL be cleared"
// classification doesn't apply.
//
// Currently the only entry is the `requires` block: the deploy
// orchestration's `replaceDependencies` step runs unconditionally on
// the new edge set, so a `requires.<alias>` the local yaml dropped is
// removed regardless of the apps PATCH's omit semantics. Pre-fix
// (I4-F R1 retest), the gate listed it under "Preserved server-side
// (no action needed)", which contradicted what the user just chose to
// do (intentional removal) and disagreed with the post-deploy state.
//
// As of conductor iter-4 R2, the same orchestration also strips the
// platform-injected secret env keys claimed by removed requires
// entries (I4-C/E full fix), so the requires-removed scenario has no
// orphan side effects to warn about either. Filtering the entries out
// here removes the misleading message entirely.
//
// Pure pattern matcher; no regex.
func IsOrchestrationRemovedPath(path string) bool {
	if path == "requires" || strings.HasPrefix(path, "requires.") || strings.HasPrefix(path, "requires (") {
		return true
	}
	return false
}

// FilterOrchestrationRemoved drops every entry classified as
// "removed by orchestration, not by the apps PATCH" from a list of
// server-only field paths. Used by the deploy gate's
// emitDeletionWarning so the bulleted summary only mentions
// PATCH-relevant fields, the ones whose presence/absence on the
// next push really determines server state. Returns a freshly
// allocated slice; never mutates the input.
func FilterOrchestrationRemoved(serverOnly []string) []string {
	if len(serverOnly) == 0 {
		return nil
	}
	out := make([]string, 0, len(serverOnly))
	for _, f := range serverOnly {
		topLevel := f
		if i := strings.IndexAny(f, ". ["); i >= 0 {
			topLevel = f[:i]
		}
		if IsOrchestrationRemovedPath(topLevel) || IsOrchestrationRemovedPath(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// splitServerOnlyEntry breaks a listServerOnlyFields entry like
// `cpuRequestMc (0)` into (name="cpuRequestMc", summary="0"). Returns
// the input unchanged as name with empty summary when the trailing
// "(...)" wrapper is missing, so callers don't need to special-case
// pre-summary entries.
func splitServerOnlyEntry(entry string) (name, summary string) {
	if i := strings.LastIndex(entry, " ("); i >= 0 && strings.HasSuffix(entry, ")") {
		return entry[:i], entry[i+2 : len(entry)-1]
	}
	return entry, ""
}

// isZeroValueSummary recognises the value-summary strings
// summarizeValue emits for zero-defaulted yaml values. Used by
// IsBenignPreserveZero; pinned to the canonical summary shapes so a
// future formatter change won't silently inflate the benign set.
func isZeroValueSummary(summary string) bool {
	switch summary {
	case "0", `""`, "false", "null", "0 entries", "0 fields":
		return true
	}
	return false
}

// IsBenignPreserveZero reports whether a server-only field entry
// (as emitted by listServerOnlyFields, e.g. `cpuRequestMc (0)` or
// `memoryRequestMb (0)`) has preserve-on-omit semantics AND a
// zero-default value summary. For these, omitting the field from the
// local yaml leaves server state unchanged, so the pre-deploy drift
// gate can wave the deploy through without `--force`.
//
// I3-E retest follow-up: a freshly-provisioned app emits zero-valued
// resource fields (cpuRequestMc, memoryRequestMb, etc.) even when the
// user has nothing customised locally; pre-fix the gate refused on
// every second deploy until the user pulled. The classification is
// conservative — clearOnOmit fields are never benign (their absence
// removes server state, which is meaningful), and non-zero values are
// never benign (a non-zero value the user hasn't pulled may be a
// customisation they'd want to round-trip rather than silently push
// over with their local omission).
func IsBenignPreserveZero(entry string) bool {
	name, summary := splitServerOnlyEntry(entry)
	if name == "" {
		return false
	}
	topLevel := name
	if i := strings.IndexAny(name, ".["); i >= 0 {
		topLevel = name[:i]
	}
	if IsOmitClearField(topLevel) || IsOmitClearPath(name) {
		return false
	}
	return isZeroValueSummary(summary)
}

// AnyServerOnlyIsBlocking returns true when at least one entry in
// the list either has clearOnOmit semantics or a non-zero
// preserve-on-omit value. Used by NeedsForceToDeploy to skip the
// hard refusal when every server-only field is a benign zero
// preserve-on-omit default (the I3-E retest case).
//
// An empty list reports false (no blocking entries) so a degenerate
// drift with no server-only fields doesn't accidentally trip the
// gate via this path; the caller's other refusal conditions still
// apply.
func AnyServerOnlyIsBlocking(entries []string) bool {
	for _, e := range entries {
		if !IsBenignPreserveZero(e) {
			return true
		}
	}
	return false
}

// SyncPlan describes every change a sync run would push to the server.
// Each section is nil/empty when there's nothing to do for it. RefusedYAML
// holds yaml fields the user changed locally that the server has no
// endpoint for (vcs/integration, storageMb, etc.), surfaced so we can
// tell the user "this can't be synced, change it via the console."
type SyncPlan struct {
	AppID   string `json:"appId"`
	AppName string `json:"appName"`
	CID     string `json:"cid"`

	// HasChanges is true when the plan touches anything at all. Always
	// emitted (no omitempty) so `apps_sync --dry-run --json` consumers
	// can branch on it without having to inspect every section. Pre-fix
	// (I4-I), the JSON output collapsed to `{appId, appName, cid}` on
	// no-op plans, leaving CI parsers no signal between "ran the plan,
	// nothing to do" and "ran a partial plan we should investigate".
	HasChangesField bool `json:"hasChanges"`

	// Notes are short informational messages rendered above the section
	// headers. Mirror of DiffReport.Notes; same generator is used for both
	// (see buildClassFlapNotes in diff.go).
	Notes []string `json:"notes,omitempty"`

	YAMLPatch   map[string]any `json:"yamlPatch,omitempty"`   // nil if no change
	YAMLDiff    string         `json:"yamlDiff,omitempty"`    // unified diff for display
	RefusedYAML []string       `json:"refusedYaml,omitempty"` // immutable fields the user touched

	// SecretEnv covers the Secret-backed env vars (sensitive). Sourced from
	// `.runos.{cid}.{id}.env`. Nil when no change.
	SecretEnv *EnvChange `json:"secretEnv,omitempty"`
	// Env covers the ConfigMap-backed env vars (plain, committed to VCS).
	// Sourced from `runos.{cid}.{id}.config.env`. Nil when no change.
	Env *EnvChange `json:"env,omitempty"`

	SecretFiles *SecretFilesChange `json:"secretFiles,omitempty"` // nil when no secret-file change

	Overrides []OverrideOp `json:"overrides,omitempty"` // empty when nothing to do
}

// EnvChange captures the destructive nature of replace-all env updates so
// the renderer can highlight removals.
//
// Remove vs PreservedByPlatform: the secret-env-vars Secret is the union of
// the user's env file and the requires-derived env vars (DATABASE_URL,
// REDIS_HOST, etc.) the conductor injects on every push. When the local
// file is missing one of those names, naive set-diff lists it under Remove
// alongside genuinely-removed user keys. The conductor's
// `app.updateSecretEnvVars` orchestration re-derives requires-injected vars
// from the registered requires on every call and always wins on conflict
// (see conductor `util/services/syncRequires.ts:mergeRequiresAndUser`), so
// those names will reappear on the next push. Splitting the bucket keeps
// the plan honest: the user reads "platform-injected, will not be deleted"
// instead of a misleading "replace-all will delete DATABASE_URL".
type EnvChange struct {
	Add                 map[string]string `json:"add,omitempty"`
	Update              map[string]string `json:"update,omitempty"`              // keys present on both sides, value differs
	Remove              []string          `json:"remove,omitempty"`              // keys server has, local doesn't, NOT requires-injected
	PreservedByPlatform []string          `json:"preservedByPlatform,omitempty"` // server has, local doesn't, but a requires.<alias>.env claims the name
	Final               map[string]string `json:"final"`                         // the full set we'll send
}

// HasChanges reports whether the change actually differs from the server.
// PreservedByPlatform is intentionally excluded: those keys WILL come back
// on the next push, so they don't represent a meaningful state delta.
func (e *EnvChange) HasChanges() bool {
	return len(e.Add) > 0 || len(e.Update) > 0 || len(e.Remove) > 0
}

// CheckEmptySecretEnvWipe returns a non-nil error when the plan would
// replace-all server-side secret env vars with an effectively-empty
// user-set — a silent wipe most often caused by a missing or empty
// local secret-env file (e.g. fresh checkout where the gitignored file
// isn't on disk yet).
//
// Detection signal: every key in SecretEnv.Final is platform-injected
// (claimed by `requires.<alias>.env` on the local yaml) AND
// SecretEnv.Remove is non-empty. The conjunction means "the user side
// of the push is empty and the server has user-set keys that will be
// deleted."
//
// Why we filter Final by platform-injected names rather than just
// checking byte-empty: a local file containing ONLY DATABASE_URL=... is
// effectively empty from the user's perspective — DATABASE_URL gets
// re-injected on every push by the requires-merge, so its presence in
// the wire body doesn't represent user intent. Without the filter the
// gate misses this scenario and the user silently loses APP_KEY etc.
//
// localPlatformInjected is the set of keys in the LOCAL secret env file
// that are claimed by the LOCAL yaml's `requires.<alias>.env` mappings.
// Compute via FindServerInjectedEnvCollisions(localSecretEnv,
// localApp.Requires). nil/empty is fine — the gate degrades to the
// byte-empty check.
//
// allowEmpty is the explicit opt-in (the user passed
// `--allow-empty-secret-env`). The `--yes` confirm-skip flag does NOT
// waive this gate — `--yes` is "skip the prompt for the safe case",
// not "I'm OK with destructive ops."
func CheckEmptySecretEnvWipe(
	plan *SyncPlan,
	allowEmpty bool,
	localPlatformInjected map[string]bool,
) error {
	if plan == nil || plan.SecretEnv == nil {
		return nil
	}
	// Count keys in Final that aren't platform-injected. If at least one
	// user-authored key remains, this is a normal push, not a wipe.
	for k := range plan.SecretEnv.Final {
		if !localPlatformInjected[k] {
			return nil
		}
	}
	if len(plan.SecretEnv.Remove) == 0 {
		return nil
	}
	if allowEmpty {
		return nil
	}
	keys := append([]string(nil), plan.SecretEnv.Remove...)
	sort.Strings(keys)
	return fmt.Errorf(
		"refusing to wipe %d server-side secret env key(s): %s.\n"+
			"the local secret-env file is empty, missing, or carries only platform-injected names.\n"+
			"if this is intentional (e.g. clearing all secrets), re-run with --allow-empty-secret-env.\n"+
			"if not, run `runos apps pull --force` to bring the server values into local first.",
		len(keys),
		strings.Join(keys, ", "),
	)
}

// SecretFilesChange holds add+remove deltas plus the full content for the
// add side (we read local files at plan time so apply doesn't have to).
type SecretFilesChange struct {
	Add    []SecretFilePayload `json:"add,omitempty"`    // brand-new files
	Update []SecretFilePayload `json:"update,omitempty"` // local md5 differs from server
	Remove []string            `json:"remove,omitempty"` // server has, local doesn't
}

// HasChanges reports whether the secret-files plan would touch anything
// (any add, update, or remove). False means local + server are already
// in sync and the sync command can skip the secret-files endpoint.
func (s *SecretFilesChange) HasChanges() bool {
	return len(s.Add) > 0 || len(s.Update) > 0 || len(s.Remove) > 0
}

// AllAddPayloads returns the combined add+update slice. The server's
// secret-files endpoint doesn't distinguish "add" from "update", both
// fall under the same `add: [...]` array, where existing files are
// overwritten by filename. We track them separately client-side only for
// nicer plan output.
func (s *SecretFilesChange) AllAddPayloads() []SecretFilePayload {
	out := make([]SecretFilePayload, 0, len(s.Add)+len(s.Update))
	out = append(out, s.Add...)
	out = append(out, s.Update...)
	return out
}

// OverrideOp is a single CRUD action on an override. Op is one of "add",
// "update", "delete". Content is the raw bytes of the override body when
// applicable. UnifiedDiff is populated for "update" ops so the plan can
// show exactly which lines change.
//
// LocalLeaf is the leaf filename inside the appDir/overrides/ folder
// that pull would have written for this override. Populated only for
// "delete" ops so the apply step can clean up the local file alongside
// the server delete. Best-effort — derived from OverrideFilenames over
// the full server list at plan time, so collision-disambiguated names
// (`<name>-<shortID>.yaml`) match what pull wrote.
type OverrideOp struct {
	Op          string `json:"op"`
	ID          string `json:"id,omitempty"` // server id; empty for add
	Name        string `json:"name,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
	Content     []byte `json:"-"` // not serialized; carried only in-process
	Reason      string `json:"reason,omitempty"`
	UnifiedDiff string `json:"unifiedDiff,omitempty"`
	LocalLeaf   string `json:"-"` // not serialized; only used by apply
}

// RedactSecrets replaces every env value (both SecretEnv and Env) in
// the plan with the "<redacted>" marker. The text renderer already
// honours --redact-secrets via printEnvChange; this method extends the
// same redaction to the JSON path so `apps sync --dry-run --json
// --redact-secrets` doesn't leak ADMIN_TOKEN / JWT_SECRET / DATABASE_URL
// (full conn string with password) / etc. into LLM context via the MCP
// wrapper. Mutates in place; nil-safe on the receiver.
//
// Regression target: I10-M. The --redact-secrets flag's stated contract
// is "keep secrets out of LLM context, even if a non-secret config.env
// mistakenly carries an API key" — applies to BOTH env files and BOTH
// output shapes.
func (p *SyncPlan) RedactSecrets() {
	if p == nil {
		return
	}
	if p.SecretEnv != nil {
		redactEnvChange(p.SecretEnv)
	}
	if p.Env != nil {
		redactEnvChange(p.Env)
	}
}

func redactEnvChange(e *EnvChange) {
	if e == nil {
		return
	}
	for k := range e.Add {
		e.Add[k] = "<redacted>"
	}
	for k := range e.Update {
		e.Update[k] = "<redacted>"
	}
	for k := range e.Final {
		e.Final[k] = "<redacted>"
	}
}

// HasChanges reports whether the plan touches anything at all.
func (p *SyncPlan) HasChanges() bool {
	if len(p.YAMLPatch) > 0 {
		return true
	}
	if p.SecretEnv != nil && p.SecretEnv.HasChanges() {
		return true
	}
	if p.Env != nil && p.Env.HasChanges() {
		return true
	}
	if p.SecretFiles != nil && p.SecretFiles.HasChanges() {
		return true
	}
	return len(p.Overrides) > 0
}

// SyncInputs is the bag of "what the local file system + parsed yaml say"
// passed into ComputeSyncPlan. Splitting it out keeps the function signature
// readable and makes tests easy.
type SyncInputs struct {
	LocalApp           *PulledApp
	LocalSecretEnvVars map[string]string // sensitive (Secret-backed)
	LocalEnvVars       map[string]string // plain (ConfigMap-backed)
	LocalSecretFiles   map[string][]byte // filename -> raw decoded bytes
	LocalOverrides     []LocalOverride

	ServerRaw           map[string]any
	ServerSecretEnvVars map[string]string
	ServerEnvVars       map[string]string
	ServerSecretFiles   []SecretFileSummary
	ServerOverrides     []OverrideSummary
	// ServerRequires is the result of GetAppRequires: alias -> the
	// authoritative {Type, ID, Config, Env} for each linked service.
	// Compared against LocalApp.Requires to surface "requires drifted"
	// as a refused entry; sync never patches the requires block
	// (re-deploy is the only way to change linked services). Class is
	// never compared, conductor doesn't store it.
	ServerRequires map[string]ServiceRequirement
}

// LocalOverride is a single override read from disk: filename mapping is
// resolved by the caller via OverrideFilenames + the local YAML's id ↔
// content pairing.
type LocalOverride struct {
	ID      string // server id from the local yaml; empty for ones that don't yet exist on server
	Name    string
	Enabled bool
	Content []byte
}

// ComputeSyncPlan diffs local against server and produces a SyncPlan. It
// performs no I/O of its own; callers gather inputs and pass them in.
func ComputeSyncPlan(in SyncInputs) *SyncPlan {
	plan := &SyncPlan{
		AppID:   in.LocalApp.ID,
		AppName: in.LocalApp.App,
		CID:     in.LocalApp.CID,
	}

	var promotion string
	plan.YAMLPatch, plan.YAMLDiff, plan.RefusedYAML, promotion = computeYAMLPatch(in.LocalApp, in.ServerRaw, in.ServerRequires)
	plan.Notes = buildClassFlapNotes(in.LocalApp, in.ServerRaw)
	// Promotion notice rendered alongside the existing class-flap note.
	// The two surface adjacent but distinct conditions: class-flap fires
	// when the server has ALREADY flipped to custom (post-sync state);
	// promotion fires when the CLI is ABOUT to send a body that will
	// trigger the flip. Surface both so the user sees a coherent story
	// across the diff -> sync transition.
	if promotion != "" {
		plan.Notes = append(plan.Notes, promotion)
	}
	// Requires-injected names live only in the secret-env-vars Secret (never
	// in the plain ConfigMap), so the partition only applies to the secret
	// side. Built from the local yaml because it's the authoritative record
	// of what the user has linked; the server's /requires response could be
	// used too but we prefer the local view for plan-time honesty.
	platformInjected := requiresOwnedEnvNames(in.LocalApp)
	plan.SecretEnv = computeEnvChange(in.LocalSecretEnvVars, in.ServerSecretEnvVars, platformInjected)
	plan.Env = computeEnvChange(in.LocalEnvVars, in.ServerEnvVars, nil)
	plan.SecretFiles = computeSecretFilesChange(in.LocalApp.SecretFiles, in.LocalSecretFiles, in.ServerSecretFiles)
	plan.Overrides = computeOverrideOps(in.LocalOverrides, in.ServerOverrides)
	// Snapshot the boolean for JSON consumers (I4-I); HasChanges() is
	// the canonical predicate that printSyncPlan and the apply path
	// already use.
	plan.HasChangesField = plan.HasChanges()

	return plan
}

// requiresHaveDrift reports whether the local yaml's requires block
// disagrees with the server's authoritative /requires response.
// Compares Type, ID, Config, and Env per alias; ignores Class (local-only).
// Treats nil and empty maps as equivalent so legacy apps with empty
// server-side metadata don't flag drift when the local yaml is also empty.
//
// Unlike pull/diff's MergeRequiresUserAuthored (which suppresses drift
// when the server returns empty Config/Env so legacy apps don't show
// false positives in the diff display), sync IS allowed to push from
// local-non-empty into server-empty, because pushing is precisely the
// migration path that fills in the server's missing metadata. So the
// "empty server, populated local" case counts as drift here.
func requiresHaveDrift(local *PulledApp, serverRequires map[string]ServiceRequirement) bool {
	if local == nil {
		return false
	}
	if len(local.Requires) != len(serverRequires) {
		return true
	}
	for alias, loc := range local.Requires {
		srv, ok := serverRequires[alias]
		if !ok {
			return true
		}
		if loc.Type != srv.Type || loc.ID != srv.ID {
			return true
		}
		if !mapsEqualNormalised(loc.Config, srv.Config) {
			return true
		}
		if !envEqualNormalised(loc.Env, srv.Env) {
			return true
		}
	}
	return false
}

// mapsEqualNormalised compares two map[string]any treating nil and empty
// as equivalent so an absent yaml key (decoded as nil) and an empty
// {} from the server compare equal.
func mapsEqualNormalised(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// envEqualNormalised mirrors mapsEqualNormalised for the env mapping
// (string→string).
func envEqualNormalised(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// requiresPatchPayload serialises local.Requires into the wire shape
// PATCH /apps/:id expects: alias -> {id, type, config?, env?}. Class is
// stripped (the endpoint rejects it; sizing is service-side state).
// Empty Config/Env are omitted so the JSON stays compact.
func requiresPatchPayload(local *PulledApp) map[string]any {
	if local == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(local.Requires))
	for alias, r := range local.Requires {
		entry := map[string]any{"type": r.Type}
		if r.ID != "" {
			entry["id"] = r.ID
		}
		if len(r.Config) > 0 {
			entry["config"] = r.Config
		}
		if len(r.Env) > 0 {
			entry["env"] = r.Env
		}
		out[alias] = entry
	}
	return out
}

// computeEnvChange compares two env-var maps and produces the buckets.
// Returns nil when the maps are equal, sync should leave env alone.
//
// platformInjected names server keys the user did NOT explicitly write that
// nonetheless reappear on every push because a requires.<alias>.env entry
// claims them. Pass nil for the plain (ConfigMap) side, which never gets
// requires-derived injection. See the EnvChange doc comment for the full
// rationale.
func computeEnvChange(local, server map[string]string, platformInjected map[string]bool) *EnvChange {
	change := &EnvChange{Final: local}
	for k, v := range local {
		if existing, ok := server[k]; !ok {
			if change.Add == nil {
				change.Add = map[string]string{}
			}
			change.Add[k] = v
		} else if existing != v {
			if change.Update == nil {
				change.Update = map[string]string{}
			}
			change.Update[k] = v
		}
	}
	for k := range server {
		if _, ok := local[k]; ok {
			continue
		}
		if platformInjected[k] {
			change.PreservedByPlatform = append(change.PreservedByPlatform, k)
			continue
		}
		change.Remove = append(change.Remove, k)
	}
	sort.Strings(change.Remove)
	sort.Strings(change.PreservedByPlatform)
	// PreservedByPlatform alone is a no-op (those keys always come back),
	// so a section with only that bucket would just confuse the user.
	if !change.HasChanges() {
		return nil
	}
	return change
}

// requiresOwnedEnvNames returns the set of env-var names claimed by some
// requires.<alias>.env mapping in the local yaml. The right-hand side of
// each requires.env entry names a runtime-injected env var, so any server
// key with that name is platform-managed even if the local file doesn't
// list it. Used by computeEnvChange to keep the secret-side Remove bucket
// honest.
func requiresOwnedEnvNames(app *PulledApp) map[string]bool {
	if app == nil || len(app.Requires) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, req := range app.Requires {
		for _, envName := range req.Env {
			if envName != "" {
				out[envName] = true
			}
		}
	}
	return out
}

// computeSecretFilesChange compares local secret-file bytes (md5'd
// internally) against the server-reported md5s. Updates are anything where
// both sides have the file but bytes differ. Adds are local-only files.
// Removes are server-only files.
//
// localManifest is the yaml's `secretFiles:` list, used to honor a
// user-specified `mountPath:`. When the manifest entry omits mountPath
// the default is `/<filename>`. For UPDATEs, the server's existing mount
// path always wins (an in-place mountPath rotation would require deleting
// and re-adding the file); the diff renders this as "mount preserved".
func computeSecretFilesChange(localManifest []SecretFile, localContent map[string][]byte, server []SecretFileSummary) *SecretFilesChange {
	change := &SecretFilesChange{}
	serverByName := map[string]SecretFileSummary{}
	for _, sf := range server {
		serverByName[sf.Filename] = sf
	}

	// Index manifest entries by filename so we can look up the user's
	// mountPath for each local file. Falls back to "/<filename>" when
	// the entry is absent or doesn't specify mountPath.
	manifestByName := map[string]SecretFile{}
	for _, sf := range localManifest {
		manifestByName[sf.Filename] = sf
	}
	defaultMountPath := func(filename string) string {
		if entry, ok := manifestByName[filename]; ok && entry.MountPath != "" {
			return entry.MountPath
		}
		return "/" + filename
	}

	for name, content := range localContent {
		sum := md5.Sum(content)
		localMd5 := hex.EncodeToString(sum[:])
		mountPath := defaultMountPath(name)
		payload := SecretFilePayload{
			Filename:  name,
			MountPath: mountPath,
			Content:   base64.StdEncoding.EncodeToString(content),
		}
		if existing, ok := serverByName[name]; ok {
			payload.MountPath = existing.MountPath
			if existing.MD5 != localMd5 {
				change.Update = append(change.Update, payload)
			}
		} else {
			change.Add = append(change.Add, payload)
		}
	}
	for _, sf := range server {
		if _, ok := localContent[sf.Filename]; !ok {
			change.Remove = append(change.Remove, sf.Filename)
		}
	}
	sort.Slice(change.Add, func(i, j int) bool { return change.Add[i].Filename < change.Add[j].Filename })
	sort.Slice(change.Update, func(i, j int) bool { return change.Update[i].Filename < change.Update[j].Filename })
	sort.Strings(change.Remove)
	if !change.HasChanges() {
		return nil
	}
	return change
}

// computeOverrideOps compares the local + server override lists. Local
// overrides without an ID are treated as fresh adds; those with an ID are
// matched against the server by id and become updates if any field differs.
// Server overrides not referenced locally become deletes.
func computeOverrideOps(local []LocalOverride, server []OverrideSummary) []OverrideOp {
	var ops []OverrideOp
	seen := map[string]bool{}
	serverByID := map[string]OverrideSummary{}
	for _, o := range server {
		serverByID[o.ID] = o
	}

	// Pre-compute the on-disk filename for every server override. Pull
	// uses OverrideFilenames over the full set so collision
	// disambiguation (`<name>-<shortID>.yaml`) is consistent — derive
	// the same map here so a delete op can find and unlink its file.
	serverFilenames := OverrideFilenames(server)
	leafByID := make(map[string]string, len(server))
	for i, srv := range server {
		leafByID[srv.ID] = serverFilenames[i]
	}

	for _, l := range local {
		if l.ID == "" {
			ops = append(ops, OverrideOp{
				Op:      "add",
				Name:    l.Name,
				Enabled: l.Enabled,
				Content: l.Content,
			})
			continue
		}
		seen[l.ID] = true
		existing, ok := serverByID[l.ID]
		if !ok {
			// Local references an id the server no longer knows about. Recreate.
			ops = append(ops, OverrideOp{
				Op:      "add",
				Name:    l.Name,
				Enabled: l.Enabled,
				Content: l.Content,
				Reason:  "local id no longer on server; will be re-created",
			})
			continue
		}
		if overrideDiffers(l, existing) {
			serverContent, _ := base64.StdEncoding.DecodeString(existing.Data)
			ops = append(ops, OverrideOp{
				Op:          "update",
				ID:          l.ID,
				Name:        l.Name,
				Enabled:     l.Enabled,
				Content:     l.Content,
				UnifiedDiff: unifiedDiff(serverContent, l.Content, "server", "local"),
			})
		}
	}

	for _, srv := range server {
		if !seen[srv.ID] {
			ops = append(ops, OverrideOp{
				Op:        "delete",
				ID:        srv.ID,
				Name:      srv.Name,
				LocalLeaf: leafByID[srv.ID],
				Reason:    "server has it, local doesn't",
			})
		}
	}
	return ops
}

func overrideDiffers(local LocalOverride, server OverrideSummary) bool {
	if local.Name != server.Name || local.Enabled != server.Enabled {
		return true
	}
	serverContent, err := base64.StdEncoding.DecodeString(server.Data)
	if err != nil {
		return true // can't compare, assume different
	}
	return !bytes.Equal(serverContent, local.Content)
}

// computeYAMLPatch decides whether sync needs to PATCH /apps/:id and, if
// so, builds the wire body. The body is the full local yaml projected to
// the server's accepted field set, NOT a sparse diff. Conductor's PATCH
// endpoint treats five fields (healthCheck, healthCheckPort,
// healthCheckPath, metricsPort, metricsPath) as desired-state with
// omit-equals-clear semantics, so any partial body that drops them
// silently wipes server state. Sending the full local yaml every time
// gives the headline cross-user property: two CLIs with the same yaml
// converge on identical server state.
//
// The drift walk that used to double as the wire body is kept for two
// purposes: (a) deciding whether to call the API at all (no point
// PATCHing a no-op), and (b) rendering the unified diff the user sees
// in the plan. The desired-state guard that previously skipped
// "local-empty + server-set" entries (`if localApp.HealthCheck != ""`)
// is gone, because under the new semantics that case IS drift, sync
// will clear the server side.
//
// Fields the user touched but the server can't update (vcs/integration)
// are returned in `refused` so the plan can warn.
//
// localApp is the parsed local yaml. server is the raw GET /apps/:id
// response (covers most fields). serverRequires is the separate
// /requires response, GET /apps/:id doesn't expose the requires
// block so we need it as its own input.
// promoteToCustomIfConflicted detects a named-RRC override that will
// flip the server-side rrcId to "custom" on apply. When the local yaml
// sits on a named class (e.g. `app.sl1.beff`) AND a resource field
// disagrees with the class's defaults (the server snapshot is the
// authoritative source of those defaults for this app), the function
// returns a shallow copy of localApp with the flip applied: rrcId is
// set to "custom" and any nil cpu/memory pointer is backfilled from
// the server snapshot so the wire body carries a complete custom
// payload. The Replicas override (or backfill) is overlaid on top.
//
// Returns (copy, notice) when promoted, (localApp, "") otherwise. The
// notice is the one-liner the plan surfaces above the section headers.
// The caller's localApp is never mutated.
//
// Detection rule: a local resource field "disagrees" if it's set to a
// non-zero value (Replicas) or non-nil pointer (cpu/memory) AND its
// value differs from the server snapshot. A thin pulled yaml carries
// none of these fields (omitempty drops them when the class baked the
// value in), so any value present came from the user editing the yaml
// by hand. That's the signal we use to decide a flip is intentional
// rather than an artefact of stale pull data.
func promoteToCustomIfConflicted(localApp *PulledApp, server map[string]any) (*PulledApp, string) {
	if localApp == nil {
		return localApp, ""
	}
	localClass := localApp.ResourceRequirementClassID
	if localClass == "" || localClass == "custom" {
		return localApp, ""
	}
	serverReplicas, _ := asInt(server["replicas"])
	serverCpuReq, _ := asInt(server["cpuRequestMc"])
	serverCpuLim, _ := asInt(server["cpuLimitMc"])
	serverMemReq, _ := asInt(server["memoryRequestMb"])
	serverMemLim, _ := asInt(server["memoryLimitMb"])

	var conflicts []string
	if localApp.Replicas != 0 && localApp.Replicas != serverReplicas {
		conflicts = append(conflicts, fmt.Sprintf("replicas %d (default %d)", localApp.Replicas, serverReplicas))
	}
	if localApp.CPURequestMc != nil && *localApp.CPURequestMc != serverCpuReq {
		conflicts = append(conflicts, fmt.Sprintf("cpuRequestMc %d (default %d)", *localApp.CPURequestMc, serverCpuReq))
	}
	if localApp.CPULimitMc != nil && *localApp.CPULimitMc != serverCpuLim {
		conflicts = append(conflicts, fmt.Sprintf("cpuLimitMc %d (default %d)", *localApp.CPULimitMc, serverCpuLim))
	}
	if localApp.MemoryRequestMb != nil && *localApp.MemoryRequestMb != serverMemReq {
		conflicts = append(conflicts, fmt.Sprintf("memoryRequestMb %d (default %d)", *localApp.MemoryRequestMb, serverMemReq))
	}
	if localApp.MemoryLimitMb != nil && *localApp.MemoryLimitMb != serverMemLim {
		conflicts = append(conflicts, fmt.Sprintf("memoryLimitMb %d (default %d)", *localApp.MemoryLimitMb, serverMemLim))
	}
	if len(conflicts) == 0 {
		return localApp, ""
	}

	// Shallow copy is sufficient: the only fields we mutate are the
	// scalar rrcId, Replicas, and the four resource pointers (each
	// reassigned to a fresh address rather than mutated through). Slice
	// and map fields (SecretFiles, Requires, ServicePortMappings, etc.)
	// alias into the caller's struct, which is fine because we never
	// read-modify-write them here.
	out := *localApp
	out.ResourceRequirementClassID = "custom"
	if out.Replicas == 0 {
		out.Replicas = serverReplicas
	}
	if out.CPURequestMc == nil {
		v := serverCpuReq
		out.CPURequestMc = &v
	}
	if out.CPULimitMc == nil {
		v := serverCpuLim
		out.CPULimitMc = &v
	}
	if out.MemoryRequestMb == nil {
		v := serverMemReq
		out.MemoryRequestMb = &v
	}
	if out.MemoryLimitMb == nil {
		v := serverMemLim
		out.MemoryLimitMb = &v
	}
	notice := fmt.Sprintf(
		"flipping resourceRequirementClassId %s -> custom (%s). cpu/memory backfilled from %s snapshot so the full custom payload reaches the conductor.",
		localClass, strings.Join(conflicts, ", "), localClass,
	)
	return &out, notice
}

func computeYAMLPatch(localApp *PulledApp, server map[string]any, serverRequires map[string]ServiceRequirement) (patch map[string]any, diff string, refused []string, promotion string) {
	// Named-RRC override flip: when the local yaml carries a named class
	// (e.g. `app.sl1.beff`) AND a replicas/cpu/memory value that disagrees
	// with the class's defaults, the conductor's resolveRRC will silently
	// flip rrcId to "custom" on apply. Detect that client-side so the
	// plan / wire body reflect the post-flip state explicitly: rrcId
	// becomes "custom" in the patch, and any resource fields the thin
	// local yaml didn't carry are backfilled from the server snapshot
	// so the custom payload is complete. Mutation is confined to a
	// shallow copy; the caller's localApp stays pristine.
	effective := localApp
	if local, note := promoteToCustomIfConflicted(localApp, server); note != "" {
		effective = local
		promotion = note
	}
	driftFields := map[string]any{}

	if effective.App != "" && effective.App != stringOr(server, "name") {
		driftFields["name"] = effective.App
	}
	// clusterDomainId, resourceRequirementClassId, replicas are partial-update
	// fields on the conductor's PATCH endpoint: an omitted local value means
	// "preserve the current server value", NOT "clear / set to zero". The wire
	// body builder (buildFullYAMLBody) correctly omits these when local is
	// zero/empty, so the sync push is harmless either way. Without these gates
	// the dry-run plan would render alarming `replicas: 1 -> replicas: 0` lines
	// for every yaml that omitted them, scaring users into either refusing the
	// (harmless) sync or pulling-and-re-syncing as a workaround. Gate drift
	// reporting on local being non-empty to mirror the wire body's omit logic.
	if effective.ClusterDomainID != "" && effective.ClusterDomainID != stringOr(server, "clusterDomainId") {
		driftFields["clusterDomainId"] = effective.ClusterDomainID
	}
	if effective.ResourceRequirementClassID != "" &&
		effective.ResourceRequirementClassID != stringOr(server, "resourceRequirementClassId") {
		driftFields["resourceRequirementClassId"] = effective.ResourceRequirementClassID
	}
	if effective.Replicas != 0 {
		if serverReplicas, ok := asInt(server["replicas"]); !ok || serverReplicas != effective.Replicas {
			driftFields["replicas"] = effective.Replicas
		}
	}
	intDrift := func(field string, local *int) {
		if local == nil {
			return
		}
		if serverVal, ok := asInt(server[field]); !ok || serverVal != *local {
			driftFields[field] = *local
		}
	}
	intDrift("cpuRequestMc", effective.CPURequestMc)
	intDrift("cpuLimitMc", effective.CPULimitMc)
	intDrift("memoryRequestMb", effective.MemoryRequestMb)
	intDrift("memoryLimitMb", effective.MemoryLimitMb)

	if portsDiffer(effective.ServicePortMappings, server["servicePortMappings"]) {
		driftFields["servicePortMappings"] = portsToWire(effective.ServicePortMappings)
	}

	// healthCheck / metrics: desired-state on the new conductor. "Local
	// empty + server set" IS drift (sync will clear). Compare unconditionally.
	serverHealth := stringOr(server, "healthCheck")
	serverHealthPort, _ := asInt(server["healthCheckPort"])
	serverHealthPath := stringOr(server, "healthCheckPath")
	if effective.HealthCheck != serverHealth {
		driftFields["healthCheck"] = effective.HealthCheck
	}
	localHealthPort := 0
	if effective.HealthCheckPort != nil {
		localHealthPort = *effective.HealthCheckPort
	}
	if localHealthPort != serverHealthPort {
		driftFields["healthCheckPort"] = localHealthPort
	}
	if effective.HealthCheckPath != serverHealthPath {
		driftFields["healthCheckPath"] = effective.HealthCheckPath
	}
	serverMetricsPort, _ := asInt(server["metricsPort"])
	serverMetricsPath := stringOr(server, "metricsPath")
	localMetricsPort := 0
	if effective.MetricsPort != nil {
		localMetricsPort = *effective.MetricsPort
	}
	if localMetricsPort != serverMetricsPort {
		driftFields["metricsPort"] = localMetricsPort
	}
	if effective.MetricsPath != serverMetricsPath {
		driftFields["metricsPath"] = effective.MetricsPath
	}

	// deploymentStrategy + nodeAffinityTags: desired-state like healthCheck
	// ("local empty + server set" IS drift; sync will clear). Both were/are
	// in the conductor's omit-equals-clear set, so omitting them from the
	// wire body clears the server value; drift detection must mirror that
	// or the plan hides a clearing (or never pushes a local value).
	// deploymentStrategy was previously missing here entirely, so a synced
	// yaml carrying it never pushed the preset and a yaml without it
	// silently cleared a console-set preset with no plan line.
	if effective.DeploymentStrategy != stringOr(server, "deploymentStrategy") {
		driftFields["deploymentStrategy"] = effective.DeploymentStrategy
	}
	serverAffinity := stringSliceOr(server, "nodeAffinityTags")
	if !stringSlicesEqual(effective.NodeAffinityTags, serverAffinity) {
		driftFields["nodeAffinityTags"] = effective.NodeAffinityTags
	}

	// Build-metadata round-trip (V13). sourceDir and dockerfile are partial-
	// update fields like clusterDomainId: an omitted local value means
	// "preserve" (the wire body builder skips empty), so drift only surfaces
	// when the local yaml has a non-empty value that disagrees with the
	// server's. Mirrors the configPath round-trip story.
	if effective.SourceDir != "" && effective.SourceDir != stringOr(server, "sourceDir") {
		driftFields["sourceDir"] = effective.SourceDir
	}
	if effective.Dockerfile != "" && effective.Dockerfile != stringOr(server, "dockerfile") {
		driftFields["dockerfile"] = effective.Dockerfile
	}

	// VCS / integration: not patchable. Surface user changes as refused so
	// they know to use the console.
	//
	// `deployType` is its own dimension ('cli' | 'vcs'); we compare it
	// against the server's deployType, NOT against integrationType (which is
	// the provider slug 'github-arc' / 'gitlab-runner' and lives separately).
	// Earlier code conflated these and produced perpetual drift on every
	// pulled-then-synced VCS app — see the matching note in pull.go.
	if effective.Integration != nil {
		serverDeployType := stringOr(server, "deployType")
		serverIntegrationID := stringOr(server, "vcsIntegrationId")
		serverRepoID := int64Or(server, "repoId")
		serverRepoName := stringOr(server, "repoName")
		serverBranch := stringOr(server, "branchName")
		if effective.DeployType != serverDeployType ||
			effective.Integration.ID != serverIntegrationID ||
			effective.Integration.RepoID != serverRepoID ||
			effective.Integration.RepoName != serverRepoName ||
			effective.Integration.BranchName != serverBranch {
			refused = append(refused, "integration (deployType, repo, branch, change via console)")
		}
	}

	requiresDrift := requiresHaveDrift(effective, serverRequires)

	if len(driftFields) == 0 && !requiresDrift {
		return nil, "", refused, promotion
	}

	// Drift exists. Build the full wire body from the effective (post-
	// promotion) local yaml so a named-RRC-with-overrides app sends a
	// complete custom payload rather than a half-baked named-class one
	// the conductor would have to re-resolve.
	patch = buildFullYAMLBody(effective)
	diff = renderYAMLPatchAsDiff(driftFields, server)
	if requiresDrift {
		diff = appendRequiresDiff(diff, effective, serverRequires)
	}
	return patch, diff, refused, promotion
}

// appendRequiresDiff renders per-alias drift between the local yaml's
// requires block and the server's /requires response. Each alias gets a
// nested view of changed fields (id, type, config, env). The output is
// appended to the standard YAML diff so the user sees both the top-level
// and the requires-level drift in one section, matching the wire body
// (sync sends the whole body in a single PATCH). Without this, the plan
// silently omitted requires changes even though they were on the wire.
func appendRequiresDiff(existing string, local *PulledApp, server map[string]ServiceRequirement) string {
	lines := []string{}
	if local != nil {
		// Walk aliases in stable order: union of local + server keys.
		seen := map[string]bool{}
		aliases := []string{}
		for a := range local.Requires {
			if !seen[a] {
				aliases = append(aliases, a)
				seen[a] = true
			}
		}
		for a := range server {
			if !seen[a] {
				aliases = append(aliases, a)
				seen[a] = true
			}
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			loc, hasLoc := local.Requires[alias]
			srv, hasSrv := server[alias]
			if !hasLoc {
				lines = append(lines, fmt.Sprintf("- requires.%s: %s/%s", alias, srv.Type, srv.ID))
				continue
			}
			if !hasSrv {
				lines = append(lines, fmt.Sprintf("+ requires.%s: %s/%s", alias, loc.Type, loc.ID))
				continue
			}
			if loc.Type != srv.Type {
				lines = append(lines, fmt.Sprintf("- requires.%s.type: %s", alias, srv.Type))
				lines = append(lines, fmt.Sprintf("+ requires.%s.type: %s", alias, loc.Type))
			}
			if loc.ID != srv.ID {
				lines = append(lines, fmt.Sprintf("- requires.%s.id: %s", alias, srv.ID))
				lines = append(lines, fmt.Sprintf("+ requires.%s.id: %s", alias, loc.ID))
			}
			if !mapsEqualNormalised(loc.Config, srv.Config) {
				cfgKeys := map[string]bool{}
				for k := range loc.Config {
					cfgKeys[k] = true
				}
				for k := range srv.Config {
					cfgKeys[k] = true
				}
				keys := make([]string, 0, len(cfgKeys))
				for k := range cfgKeys {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					lv, lok := loc.Config[k]
					sv, sok := srv.Config[k]
					if lok && sok && reflect.DeepEqual(lv, sv) {
						continue
					}
					if sok {
						lines = append(lines, fmt.Sprintf("- requires.%s.config.%s: %v", alias, k, sv))
					}
					if lok {
						lines = append(lines, fmt.Sprintf("+ requires.%s.config.%s: %v", alias, k, lv))
					}
				}
			}
			if !envEqualNormalised(loc.Env, srv.Env) {
				envKeys := map[string]bool{}
				for k := range loc.Env {
					envKeys[k] = true
				}
				for k := range srv.Env {
					envKeys[k] = true
				}
				keys := make([]string, 0, len(envKeys))
				for k := range envKeys {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					lv, lok := loc.Env[k]
					sv, sok := srv.Env[k]
					if lok && sok && lv == sv {
						continue
					}
					if sok {
						lines = append(lines, fmt.Sprintf("- requires.%s.env.%s: %s", alias, k, sv))
					}
					if lok {
						lines = append(lines, fmt.Sprintf("+ requires.%s.env.%s: %s", alias, k, lv))
					}
				}
			}
		}
	}
	if len(lines) == 0 {
		return existing
	}
	if existing == "" {
		return strings.Join(lines, "\n")
	}
	return existing + "\n" + strings.Join(lines, "\n")
}

// buildFullYAMLBody projects a local PulledApp into the JSON body shape
// PATCH /apps/:id (and POST /prepare-cli-deployment) accept. Every field
// the local yaml has set is included verbatim. Fields the local yaml
// omits are absent from the body, which under the new conductor
// semantics means "clear" for desired-state fields (healthCheck, metrics)
// and "preserve" for partial-update fields (everything else).
//
// Integration fields are intentionally not emitted: the server has no
// PATCH path for them and the conductor preserves whatever it has on
// omission. The CLI surfaces local edits to those fields as `refused`
// for user awareness.
func buildFullYAMLBody(localApp *PulledApp) map[string]any {
	body := map[string]any{}
	if localApp.App != "" {
		body["name"] = localApp.App
	}
	if localApp.ClusterDomainID != "" {
		body["clusterDomainId"] = localApp.ClusterDomainID
	}
	if localApp.ResourceRequirementClassID != "" {
		body["resourceRequirementClassId"] = localApp.ResourceRequirementClassID
	}
	// Replicas is an int with no omitempty in PulledApp (a pulled yaml
	// always carries an explicit value). Treat 0 as "not explicitly set"
	// to avoid clobbering server-side defaults when a yaml has the line
	// stripped, the practical case for that is "leave it alone".
	if localApp.Replicas > 0 {
		body["replicas"] = localApp.Replicas
	}
	if localApp.CPURequestMc != nil {
		body["cpuRequestMc"] = *localApp.CPURequestMc
	}
	if localApp.CPULimitMc != nil {
		body["cpuLimitMc"] = *localApp.CPULimitMc
	}
	if localApp.MemoryRequestMb != nil {
		body["memoryRequestMb"] = *localApp.MemoryRequestMb
	}
	if localApp.MemoryLimitMb != nil {
		body["memoryLimitMb"] = *localApp.MemoryLimitMb
	}
	if len(localApp.ServicePortMappings) > 0 {
		body["servicePortMappings"] = portsToWire(localApp.ServicePortMappings)
	}
	// Desired-state fields on the new conductor: present = set,
	// absent = clear. We send only when the local yaml has a value;
	// silence on these keys is the user's "clear it" signal.
	if localApp.HealthCheck != "" {
		body["healthCheck"] = localApp.HealthCheck
	}
	if localApp.HealthCheckPort != nil {
		body["healthCheckPort"] = *localApp.HealthCheckPort
	}
	if localApp.HealthCheckPath != "" {
		body["healthCheckPath"] = localApp.HealthCheckPath
	}
	if localApp.MetricsPort != nil {
		body["metricsPort"] = *localApp.MetricsPort
	}
	if localApp.MetricsPath != "" {
		body["metricsPath"] = localApp.MetricsPath
	}
	// Build-metadata round-trip (V13). Partial-update fields: empty means
	// "preserve current server value", non-empty means "set". Sync the
	// PATCH gates these so a configPath/sourceDir/dockerfile-only edit
	// hits the conductor's sync fast-path (no redeploy).
	if localApp.SourceDir != "" {
		body["sourceDir"] = localApp.SourceDir
	}
	if localApp.Dockerfile != "" {
		body["dockerfile"] = localApp.Dockerfile
	}
	// Desired-state fields in the conductor's omit-equals-clear set:
	// silence on these keys is the user's "clear it" signal, so only emit
	// when the local yaml carries a value (mirrors healthCheck above).
	if localApp.DeploymentStrategy != "" {
		body["deploymentStrategy"] = localApp.DeploymentStrategy
	}
	if len(localApp.NodeAffinityTags) > 0 {
		body["nodeAffinityTags"] = localApp.NodeAffinityTags
	}
	// Requires uses full-replacement semantics on the wire: aliases
	// absent from the body are removed server-side. Always include the
	// field (even when empty) so a user who deleted every requires
	// entry actually wipes the server's set, rather than being silently
	// preserved.
	body["requires"] = requiresPatchPayload(localApp)
	return body
}

// portsToWire converts the local []Port slice into the wire shape used
// by both PATCH /apps/:id and POST /prepare-cli-deployment.
func portsToWire(local []Port) []map[string]any {
	mappings := make([]map[string]any, 0, len(local))
	for _, p := range local {
		entry := map[string]any{
			"port":          p.Port,
			"standardHttps": p.StandardHTTPSValue(),
		}
		if len(p.Domains) > 0 {
			domains := make([]map[string]any, 0, len(p.Domains))
			for _, d := range p.Domains {
				md := map[string]any{"fqdn": d.Fqdn}
				if d.EnableCloudflareProxy {
					md["enableCloudflareProxy"] = true
				}
				domains = append(domains, md)
			}
			entry["domains"] = domains
		}
		mappings = append(mappings, entry)
	}
	return mappings
}

// portsDiffer is a structural compare between our []Port and the
// server-side untyped servicePortMappings array. Compares port, standardHttps,
// and the per-mapping domains list (matched on fqdn, with proxied state
// compared per-fqdn; order ignored).
func portsDiffer(local []Port, server any) bool {
	arr, ok := server.([]any)
	if !ok {
		return len(local) != 0
	}
	if len(local) != len(arr) {
		return true
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return true
		}
		port, _ := asInt(m["port"])
		// Both sides resolve absence to the platform default (true) so a
		// legacy server doc without the field doesn't read as drift against
		// a local yaml that spells the default out (or vice versa).
		stdHTTPS := true
		if v, ok := m["standardHttps"].(bool); ok {
			stdHTTPS = v
		}
		if local[i].Port != port || local[i].StandardHTTPSValue() != stdHTTPS {
			return true
		}
		serverDomains := decodeServerMappingDomains(m["domains"])
		if !mappingDomainsEqual(local[i].Domains, serverDomains) {
			return true
		}
	}
	return false
}

// decodeServerMappingDomains accepts the server's loose `domains` shape
// ([]any of strings or {fqdn, enableCloudflareProxy} maps) and returns
// canonical MappingDomain entries.
func decodeServerMappingDomains(raw any) []MappingDomain {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]MappingDomain, 0, len(arr))
	for _, d := range arr {
		switch v := d.(type) {
		case string:
			out = append(out, MappingDomain{Fqdn: v})
		case map[string]any:
			fqdn, _ := v["fqdn"].(string)
			if fqdn == "" {
				continue
			}
			enableCloudflareProxy, _ := v["enableCloudflareProxy"].(bool)
			out = append(out, MappingDomain{Fqdn: fqdn, EnableCloudflareProxy: enableCloudflareProxy})
		}
	}
	return out
}

// mappingDomainsEqual reports whether two MappingDomain slices represent the
// same (fqdn -> enableCloudflareProxy) set. Order is ignored.
func mappingDomainsEqual(a, b []MappingDomain) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	toMap := func(in []MappingDomain) map[string]bool {
		out := make(map[string]bool, len(in))
		for _, d := range in {
			out[d.Fqdn] = d.EnableCloudflareProxy
		}
		return out
	}
	ma, mb := toMap(a), toMap(b)
	if len(ma) != len(mb) {
		return false
	}
	for fqdn, prox := range ma {
		other, ok := mb[fqdn]
		if !ok || other != prox {
			return false
		}
	}
	return true
}

// renderYAMLPatchAsDiff produces a small unified-diff-flavored string
// summarizing each PATCH field's old to new value. Used purely for human
// display; sync sends `patch` over the wire either way. Non-scalar
// values (maps, slices) are rendered as nested YAML rather than Go map
// literals, so the diff is readable for users and parseable by
// downstream JSON consumers under --json (I11-G). Scalars stay on the
// same line as their key.
func renderYAMLPatchAsDiff(patch, server map[string]any) string {
	var lines []string
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, formatDiffSide("-", k, server[k])...)
		lines = append(lines, formatDiffSide("+", k, patch[k])...)
	}
	return strings.Join(lines, "\n")
}

// formatDiffSide renders one side ("-" old or "+" new) of a key's diff
// entry. Scalars produce a single line "± key: value". Maps and slices
// produce one "± key:" header followed by the value marshalled as YAML,
// with each line of the marshalled value prefixed by "± " so the diff
// keeps its read-flow at every line. nil values render as "(unset)".
func formatDiffSide(sign, key string, val any) []string {
	if val == nil {
		return []string{fmt.Sprintf("%s %s: (unset)", sign, key)}
	}
	switch val.(type) {
	case string, bool, int, int32, int64, float32, float64:
		return []string{fmt.Sprintf("%s %s: %v", sign, key, val)}
	}
	yamlBytes, err := yaml.Marshal(val)
	if err != nil {
		return []string{fmt.Sprintf("%s %s: %v", sign, key, val)}
	}
	lines := []string{fmt.Sprintf("%s %s:", sign, key)}
	for _, l := range strings.Split(strings.TrimRight(string(yamlBytes), "\n"), "\n") {
		lines = append(lines, fmt.Sprintf("%s   %s", sign, l))
	}
	return lines
}

// LoadLocalApp parses a yaml file at the given path into a PulledApp.
// The same struct used for pull is used here in reverse, no schema
// duplication.
//
// The decode is deliberately lenient about unknown fields (a newer
// server may pull fields an older CLI doesn't know; diff/sync must not
// hard-fail on those), with one carve-out: the apps-add HTTP body
// fields `envVars` / `secretEnvVars` are rejected with the same hint
// the deploy loader emits. Pre-fix, an inline `envVars:` block was
// silently dropped by the typed decode, so `apps sync` no-op'd it and
// `apps diff` showed it only as generic yaml drift while the env
// category stayed in_sync. The vars never reached the container and
// nothing said so.
func LoadLocalApp(yamlPath string) (*PulledApp, error) {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}
	if err := rejectAppsBodyEnvFields(data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(yamlPath), err)
	}
	var app PulledApp
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(yamlPath), err)
	}
	return &app, nil
}

// rejectAppsBodyEnvFields refuses a runos.yaml that carries the
// apps-add HTTP body env fields (`envVars` / `secretEnvVars`) at the
// top level. Both are valid on `apps add -f body.yaml` but have no
// effect in runos.yaml; the typed PulledApp decode would silently drop
// them. Nested occurrences (e.g. a key inside `requires.*.config`) are
// untouched. Pure helper for test coverage.
func rejectAppsBodyEnvFields(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Malformed yaml: let the typed decode downstream surface its
		// own parse error rather than duplicating it here.
		return nil
	}
	for _, field := range []string{"envVars", "secretEnvVars"} {
		if _, present := raw[field]; present {
			return fmt.Errorf("field %s not supported in runos.yaml (it is an `apps add` body field; inline values here are never applied)\n  %s",
				field, deploy.EnvVarsBodyFieldHint(field))
		}
	}
	return nil
}

// LoadLocalEnv reads the env file referenced by the app yaml's `env:` (or
// `secretEnv:`) field. The path is resolved relative to yamlDir when not
// absolute.
//
// When envRef is empty, falls back to defaultRef (the documented per-app
// default like `runos.<cid>.<id>.config.env`). The caller computes that
// default via EnvFilename / SecretEnvFilename and passes it in. The fallback
// fixes V3 (apps_sync silently skipping a file at the default path because
// the yaml didn't carry an explicit ref), and matches what apps_pull writes
// when it materialises env values from server.
//
// Returns (empty map, false, nil) when neither envRef nor defaultRef is set,
// or when an auto-derived defaultRef file doesn't exist on disk. The caller
// treats exists==false as "no local content for this side" — sync skips the
// push.
//
// When envRef is set explicitly (the yaml named the file) but it's missing,
// returns deploy.ErrMissingExplicitEnvFile rather than empty: apps sync is
// replace-all per source, so silently treating a typo'd `env:` path as empty
// would DELETE every server-side env var for that source. An explicit
// reference must point at a real file — same fail-loud contract as
// LoadLocalSecretFiles / LoadLocalOverrides.
func LoadLocalEnv(yamlDir, envRef, defaultRef string) (map[string]string, bool, error) {
	ref := envRef
	explicit := envRef != ""
	if ref == "" {
		ref = defaultRef
	}
	if ref == "" {
		return map[string]string{}, false, nil
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(yamlDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if explicit {
				return nil, false, fmt.Errorf("env file %q (referenced in runos.yaml): %w", path, deploy.ErrMissingExplicitEnvFile)
			}
			return map[string]string{}, false, nil
		}
		return nil, false, err
	}
	return parseEnvBytes(data), true, nil
}

// LoadLocalSecretFiles follows each entry in the yaml's secretFiles list
// and reads its referenced bytes. The yaml is the manifest, anything not
// listed there is not part of the local state. Paths are resolved relative
// to yamlDir.
func LoadLocalSecretFiles(yamlDir string, files []SecretFile) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for _, sf := range files {
		path := sf.Local
		if path == "" {
			return nil, fmt.Errorf("secret file %q has no local path in yaml", sf.Filename)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(yamlDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read secret file %q (%s): %w", sf.Filename, path, err)
		}
		// Infer filename from basename(local) when the yaml entry omits
		// it. Keying on an empty string would silently collapse multiple
		// entries into one slot; the basename matches what the API
		// expects on the wire and round-trips through pull.
		key := sf.Filename
		if key == "" {
			key = filepath.Base(sf.Local)
		}
		out[key] = data
	}
	return out, nil
}

// NormalizeSecretFiles fills in defaults for the yaml's secretFiles
// entries: when `filename:` is omitted, it's derived from
// `basename(local)`. Mutates the slice in place. Used by sync so that
// both the manifest projection and the loaded content map agree on the
// filename key. Without this, dry-run plans render `+ (mount /)`
// (empty filename, root mount) for an entry that's perfectly usable
// once filename is inferred.
func NormalizeSecretFiles(files []SecretFile) {
	for i := range files {
		if files[i].Filename == "" && files[i].Local != "" {
			files[i].Filename = filepath.Base(files[i].Local)
		}
	}
}

// LoadLocalOverrides reads each override's body referenced by overrides[i].Local.
// Paths are resolved relative to yamlDir.
func LoadLocalOverrides(yamlDir string, overrides []Override) ([]LocalOverride, error) {
	out := make([]LocalOverride, 0, len(overrides))
	for _, o := range overrides {
		path := o.Local
		if path == "" {
			return nil, fmt.Errorf("override %q has no local path in yaml", o.Name)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(yamlDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read override %q (%s): %w", o.Name, path, err)
		}
		out = append(out, LocalOverride{
			ID:      o.ID,
			Name:    o.Name,
			Enabled: o.Enabled,
			Content: data,
		})
	}
	return out, nil
}

// parseEnvBytes parses a dotenv-style payload into a map. Delegates to
// internal/envfile.Parse so the same `.env` file round-trips losslessly
// through `runos deploy`, `runos apps_pull`, and `runos apps_sync`
// regardless of whether the values contain newlines, leading/trailing
// whitespace, or quote characters. Issue 73.
func parseEnvBytes(b []byte) map[string]string {
	return envfile.Parse(b)
}
