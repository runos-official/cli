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
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SyncPlan describes every change a sync run would push to the server.
// Each section is nil/empty when there's nothing to do for it. RefusedYAML
// holds yaml fields the user changed locally that the server has no
// endpoint for (vcs/integration, storageMb, etc.), surfaced so we can
// tell the user "this can't be synced, change it via the console."
type SyncPlan struct {
	AppID   string `json:"appId"`
	AppName string `json:"appName"`
	CID     string `json:"cid"`

	YAMLPatch   map[string]any `json:"yamlPatch,omitempty"`   // nil if no change
	YAMLDiff    string         `json:"yamlDiff,omitempty"`    // unified diff for display
	RefusedYAML []string       `json:"refusedYaml,omitempty"` // immutable fields the user touched

	Env *EnvChange `json:"env,omitempty"` // nil when no env change

	SecretFiles *SecretFilesChange `json:"secretFiles,omitempty"` // nil when no secret-file change

	Overrides []OverrideOp `json:"overrides,omitempty"` // empty when nothing to do
}

// EnvChange captures the destructive nature of replace-all env updates so
// the renderer can highlight removals.
type EnvChange struct {
	Add    map[string]string `json:"add,omitempty"`
	Update map[string]string `json:"update,omitempty"` // keys present on both sides, value differs
	Remove []string          `json:"remove,omitempty"` // keys server has, local doesn't
	Final  map[string]string `json:"final"`            // the full set we'll send
}

// HasChanges reports whether the change actually differs from the server.
func (e *EnvChange) HasChanges() bool {
	return len(e.Add) > 0 || len(e.Update) > 0 || len(e.Remove) > 0
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
type OverrideOp struct {
	Op          string `json:"op"`
	ID          string `json:"id,omitempty"` // server id; empty for add
	Name        string `json:"name,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
	Content     []byte `json:"-"` // not serialized; carried only in-process
	Reason      string `json:"reason,omitempty"`
	UnifiedDiff string `json:"unifiedDiff,omitempty"`
}

// HasChanges reports whether the plan touches anything at all.
func (p *SyncPlan) HasChanges() bool {
	if len(p.YAMLPatch) > 0 {
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
	LocalApp        *PulledApp
	LocalEnvVars    map[string]string
	LocalSecretFiles map[string][]byte // filename -> raw decoded bytes
	LocalOverrides  []LocalOverride

	ServerRaw           map[string]any
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

	plan.YAMLPatch, plan.YAMLDiff, plan.RefusedYAML = computeYAMLPatch(in.LocalApp, in.ServerRaw, in.ServerRequires)
	plan.Env = computeEnvChange(in.LocalEnvVars, in.ServerEnvVars)
	plan.SecretFiles = computeSecretFilesChange(in.LocalSecretFiles, in.ServerSecretFiles)
	plan.Overrides = computeOverrideOps(in.LocalOverrides, in.ServerOverrides)

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
func computeEnvChange(local, server map[string]string) *EnvChange {
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
		if _, ok := local[k]; !ok {
			change.Remove = append(change.Remove, k)
		}
	}
	sort.Strings(change.Remove)
	if !change.HasChanges() {
		return nil
	}
	return change
}

// computeSecretFilesChange compares local secret-file bytes (md5'd
// internally) against the server-reported md5s. Updates are anything where
// both sides have the file but bytes differ. Adds are local-only files.
// Removes are server-only files.
func computeSecretFilesChange(localContent map[string][]byte, server []SecretFileSummary) *SecretFilesChange {
	change := &SecretFilesChange{}
	serverByName := map[string]SecretFileSummary{}
	for _, sf := range server {
		serverByName[sf.Filename] = sf
	}

	for name, content := range localContent {
		sum := md5.Sum(content)
		localMd5 := hex.EncodeToString(sum[:])
		mountPath := "/" + name
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
				Op:     "delete",
				ID:     srv.ID,
				Name:   srv.Name,
				Reason: "server has it, local doesn't",
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
func computeYAMLPatch(localApp *PulledApp, server map[string]any, serverRequires map[string]ServiceRequirement) (patch map[string]any, diff string, refused []string) {
	driftFields := map[string]any{}

	if localApp.App != "" && localApp.App != stringOr(server, "name") {
		driftFields["name"] = localApp.App
	}
	if localApp.ClusterDomainID != stringOr(server, "clusterDomainId") {
		driftFields["clusterDomainId"] = localApp.ClusterDomainID
	}
	if localApp.ResourceRequirementClassID != stringOr(server, "resourceRequirementClassId") {
		driftFields["resourceRequirementClassId"] = localApp.ResourceRequirementClassID
	}
	// Replicas drift only when the two sides actually disagree. Server
	// missing the field + local zero is "neither side cares", not drift.
	if serverReplicas, ok := asInt(server["replicas"]); ok {
		if serverReplicas != localApp.Replicas {
			driftFields["replicas"] = localApp.Replicas
		}
	} else if localApp.Replicas != 0 {
		driftFields["replicas"] = localApp.Replicas
	}
	intDrift := func(field string, local *int) {
		if local == nil {
			return
		}
		if serverVal, ok := asInt(server[field]); !ok || serverVal != *local {
			driftFields[field] = *local
		}
	}
	intDrift("cpuRequestMc", localApp.CPURequestMc)
	intDrift("cpuLimitMc", localApp.CPULimitMc)
	intDrift("memoryRequestMb", localApp.MemoryRequestMb)
	intDrift("memoryLimitMb", localApp.MemoryLimitMb)

	if portsDiffer(localApp.ServicePortMappings, server["servicePortMappings"]) {
		driftFields["servicePortMappings"] = portsToWire(localApp.ServicePortMappings)
	}

	// healthCheck / metrics: desired-state on the new conductor. "Local
	// empty + server set" IS drift (sync will clear). Compare unconditionally.
	serverHealth := stringOr(server, "healthCheck")
	serverHealthPort, _ := asInt(server["healthCheckPort"])
	serverHealthPath := stringOr(server, "healthCheckPath")
	if localApp.HealthCheck != serverHealth {
		driftFields["healthCheck"] = localApp.HealthCheck
	}
	localHealthPort := 0
	if localApp.HealthCheckPort != nil {
		localHealthPort = *localApp.HealthCheckPort
	}
	if localHealthPort != serverHealthPort {
		driftFields["healthCheckPort"] = localHealthPort
	}
	if localApp.HealthCheckPath != serverHealthPath {
		driftFields["healthCheckPath"] = localApp.HealthCheckPath
	}
	serverMetricsPort, _ := asInt(server["metricsPort"])
	serverMetricsPath := stringOr(server, "metricsPath")
	localMetricsPort := 0
	if localApp.MetricsPort != nil {
		localMetricsPort = *localApp.MetricsPort
	}
	if localMetricsPort != serverMetricsPort {
		driftFields["metricsPort"] = localMetricsPort
	}
	if localApp.MetricsPath != serverMetricsPath {
		driftFields["metricsPath"] = localApp.MetricsPath
	}

	// VCS / integration: not patchable. Surface user changes as refused so
	// they know to use the console.
	if localApp.Integration != nil {
		serverIntegrationType := stringOr(server, "integrationType")
		serverIntegrationID := stringOr(server, "vcsIntegrationId")
		serverRepoID := int64Or(server, "repoId")
		serverRepoName := stringOr(server, "repoName")
		serverBranch := stringOr(server, "branchName")
		if localApp.DeployType != serverIntegrationType ||
			localApp.Integration.ID != serverIntegrationID ||
			localApp.Integration.RepoID != serverRepoID ||
			localApp.Integration.RepoName != serverRepoName ||
			localApp.Integration.BranchName != serverBranch {
			refused = append(refused, "integration (deployType, repo, branch, change via console)")
		}
	}

	requiresDrift := requiresHaveDrift(localApp, serverRequires)

	if len(driftFields) == 0 && !requiresDrift {
		return nil, "", refused
	}

	// Drift exists. Build the full wire body from the local yaml.
	patch = buildFullYAMLBody(localApp)
	diff = renderYAMLPatchAsDiff(driftFields, server)
	return patch, diff, refused
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
			"standardHttps": p.StandardHttps,
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
		stdHTTPS, _ := m["standardHttps"].(bool)
		if local[i].Port != port || local[i].StandardHttps != stdHTTPS {
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
// summarizing each PATCH field's old → new value. Used purely for human
// display; sync sends `patch` over the wire either way.
func renderYAMLPatchAsDiff(patch, server map[string]any) string {
	var lines []string
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		newVal := patch[k]
		oldVal := server[k]
		lines = append(lines, fmt.Sprintf("- %s: %v", k, oldVal))
		lines = append(lines, fmt.Sprintf("+ %s: %v", k, newVal))
	}
	return strings.Join(lines, "\n")
}

// LoadLocalApp parses a yaml file at the given path into a PulledApp.
// The same struct used for pull is used here in reverse, no schema
// duplication.
func LoadLocalApp(yamlPath string) (*PulledApp, error) {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}
	var app PulledApp
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(yamlPath), err)
	}
	return &app, nil
}

// LoadLocalEnv reads the env file referenced by the app yaml's `env:`
// field. The path is resolved relative to yamlDir when not absolute.
// Returns (empty map, false, nil) when the yaml omits an env reference,
// the caller should interpret that as "don't sync env at all", matching
// the absent-means-hands-off rule baked into pull.
func LoadLocalEnv(yamlDir, envRef string) (map[string]string, bool, error) {
	if envRef == "" {
		return map[string]string{}, false, nil
	}
	path := envRef
	if !filepath.IsAbs(path) {
		path = filepath.Join(yamlDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
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
		out[sf.Filename] = data
	}
	return out, nil
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

func parseEnvBytes(b []byte) map[string]string {
	out := map[string]string{}
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			line := string(b[start:i])
			start = i + 1
			if line == "" {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				continue
			}
			out[line[:eq]] = line[eq+1:]
		}
	}
	return out
}
