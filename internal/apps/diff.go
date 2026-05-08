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

	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
)

// DiffStatus describes the relationship between local and server state for
// a single section of the synced config.
type DiffStatus string

// DiffStatus values reported per section.
//
//	StatusInSync       - local file matches server-rendered equivalent.
//	StatusDrift        - local file exists but differs from server.
//	StatusLocalMissing - server has data, local file is absent (or empty).
const (
	StatusInSync       DiffStatus = "in_sync"
	StatusDrift        DiffStatus = "drift"
	StatusLocalMissing DiffStatus = "local_missing"
)

// SectionDiff reports the outcome of comparing one local file against the
// server-rendered equivalent. UnifiedDiff is populated only on drift.
//
// AdditiveOnly captures local ⊆ server: every key/value local has is
// present and equal on the server, the server simply has more. Pull's
// force gate uses this to avoid blocking on purely additive incoming
// changes (pull would only ADD fields locally; nothing local would be
// lost).
//
// LocalIsSuperset captures the dual: server ⊆ local. Every key/value
// the server has is present and equal locally, local may have more.
// Deploy's force gate uses this to allow purely additive local edits
// (the user added new fields, deploy will set them; nothing on the
// server is being clobbered or cleared) without requiring --force.
type SectionDiff struct {
	Status      DiffStatus `json:"status"`
	Path        string     `json:"path,omitempty"`
	UnifiedDiff string     `json:"unifiedDiff,omitempty"`
	// AdditiveOnly and LocalIsSuperset are always emitted in JSON
	// (no omitempty) so callers, especially LLMs reading the report,
	// can rely on their presence and don't have to infer absence as
	// "false".
	AdditiveOnly    bool `json:"additiveOnly"`
	LocalIsSuperset bool `json:"localIsSuperset"`
	// ServerOnlyFields lists dotted-path summaries of fields the
	// server has that the local yaml doesn't. Populated whenever
	// the section is in drift, regardless of which subset relation
	// holds. Used by the deploy gate's deletion warning to
	// enumerate exactly what desired-state fields would be CLEARED
	// when the deploy ships the user's local yaml. Examples:
	//   "servicePortMappings[0].domains (2 entries)"
	//   "clusterDomainId (\"elpfn\")"
	//   "healthCheck (\"standard\")"
	ServerOnlyFields []string `json:"serverOnlyFields,omitempty"`
}

// SecretFileDiff reports the status of a single server-side secret file
// against its local copy. UnifiedDiff is populated by
// EnrichSecretFileDiffsWithContent only when the user explicitly opted
// into seeing decoded secret content.
type SecretFileDiff struct {
	Filename    string     `json:"filename"`
	Status      DiffStatus `json:"status"`
	Local       string     `json:"local,omitempty"`
	ServerMd5   string     `json:"serverMd5,omitempty"`
	LocalMd5    string     `json:"localMd5,omitempty"`
	UnifiedDiff string     `json:"unifiedDiff,omitempty"`
}

// SecretFilesDiff rolls per-file outcomes into an aggregate status.
type SecretFilesDiff struct {
	Status  DiffStatus       `json:"status"`
	Entries []SecretFileDiff `json:"entries"`
}

// OverrideDiff reports the status of a single server-side manifest
// override against its local copy. UnifiedDiff is populated for drift /
// local-missing entries so the renderer can show the exact line-level
// change. Override bodies are non-sensitive YAML, so we always compute it.
type OverrideDiff struct {
	ID          string     `json:"id"`
	Name        string     `json:"name,omitempty"`
	Status      DiffStatus `json:"status"`
	Local       string     `json:"local,omitempty"`
	ServerMd5   string     `json:"serverMd5,omitempty"`
	LocalMd5    string     `json:"localMd5,omitempty"`
	UnifiedDiff string     `json:"unifiedDiff,omitempty"`
}

// OverridesDiff rolls per-override outcomes into an aggregate status.
type OverridesDiff struct {
	Status  DiffStatus     `json:"status"`
	Entries []OverrideDiff `json:"entries"`
}

// DiffReport is the full cross-section report for a single app.
//
// Code is populated when the per-app directory has a sidecar recording
// the cliUploadID its source code came from (set by `apps pull --code`
// or a successful `runos deploy`). Nil when no baseline exists; in
// that case the diff has no opinion on source-code drift.
type DiffReport struct {
	CID         string             `json:"cid"`
	AppID       string             `json:"appId"`
	AppName     string             `json:"appName"`
	// Notes are short informational messages rendered above the section
	// list. Currently used to flag the RRC custom-synthesis flap (server
	// silently set class=custom because cpu/mem/replicas overrides
	// disagreed with the named class), so users don't read the resulting
	// `class` drift as a fresh problem on every sync.
	Notes       []string           `json:"notes,omitempty"`
	YAML        SectionDiff        `json:"yaml"`
	// SecretEnv compares the local sensitive (Secret-backed) env file
	// against the K8s Secret. Values are redacted in display by the
	// diff command's --redact-secrets flag.
	SecretEnv   SectionDiff        `json:"secretEnv"`
	// Env compares the local plain (ConfigMap-backed) env file against
	// the K8s ConfigMap. Values are committed to VCS by definition;
	// no redaction.
	Env         SectionDiff        `json:"env"`
	SecretFiles SecretFilesDiff    `json:"secretFiles"`
	Overrides   OverridesDiff      `json:"overrides"`
	Code        *CodeVersionStatus `json:"code,omitempty"`
}

// HasDrift returns true when any section of the report is out of sync,
// including sections whose only issue is a missing local file, and the
// optional Code section when the local source is behind the server.
// Use this for the diff command's exit code.
func (r *DiffReport) HasDrift() bool {
	if r.YAML.Status != StatusInSync ||
		r.SecretEnv.Status != StatusInSync ||
		r.Env.Status != StatusInSync ||
		r.SecretFiles.Status != StatusInSync ||
		r.Overrides.Status != StatusInSync {
		return true
	}
	return r.Code.IsStale()
}

// NeedsForceToOverwrite is true when pulling would clobber locally-edited
// content. Specifically:
//
//   - yaml / env: drift counts only when local has divergent or extra
//     content. Pure server-side additions (AdditiveOnly) are safe.
//   - secret files / overrides: per-entry drift counts; local_missing
//     entries are safe fresh-writes.
//
// A "true" here is the only thing that triggers pull's refusal.
func (r *DiffReport) NeedsForceToOverwrite() bool {
	if r.YAML.Status == StatusDrift && !r.YAML.AdditiveOnly {
		return true
	}
	if r.SecretEnv.Status == StatusDrift && !r.SecretEnv.AdditiveOnly {
		return true
	}
	if r.Env.Status == StatusDrift && !r.Env.AdditiveOnly {
		return true
	}
	for _, e := range r.SecretFiles.Entries {
		if e.Status == StatusDrift {
			return true
		}
	}
	for _, e := range r.Overrides.Entries {
		if e.Status == StatusDrift {
			return true
		}
	}
	return false
}

// NeedsForceToDeploy is true when running `runos deploy` would do
// something the user might not have intended:
//
//   - yaml: server has fields local doesn't (potential deletion under
//     omit-equals-clear), or shared fields have divergent values
//     (overwrite). Both are captured by `!LocalIsSuperset`. Pure
//     local additions (user wrote new fields, server doesn't have
//     them) are NOT blocking: that's the normal deploy path.
//   - env: additive drift gets merged in by the pre-deploy syncAppState
//     step before the actual upload, so it isn't a deploy concern.
//     Divergent env values would be a real conflict, but syncAppState
//     errors out on those independently.
//   - secret files / overrides: deploy doesn't push them at all (use
//     `apps sync` for those). Drift here isn't relevant to deploy.
//   - code: this IS deploy's payload. Stale source (server has newer
//     archives than the recorded baseline) is always blocking.
func (r *DiffReport) NeedsForceToDeploy() bool {
	if r.YAML.Status == StatusDrift && !r.YAML.LocalIsSuperset {
		return true
	}
	return r.Code.IsStale()
}

// ComputeYAMLDiff marshals the expected server-state PulledApp and diffs it
// against the bytes of localPath. Missing local file surfaces as
// local_missing; identical bytes as in_sync; otherwise a unified diff.
// On drift, also classifies whether local is a strict subset of server
// (purely additive changes incoming), used by pull to avoid the force
// gate when nothing would be overwritten. When additive, lists the
// server-only field paths so the deploy gate can warn about destructive
// --force pushes.
//
// V13: `sourceDir` and `dockerfile` round-trip through the AppDocument,
// so server-rendered bytes carry them when set. The byte comparison is
// directly meaningful; no field-level strip is needed.
func ComputeYAMLDiff(localPath string, serverState *PulledApp) (SectionDiff, error) {
	serverBytes, err := yaml.Marshal(serverState)
	if err != nil {
		return SectionDiff{}, fmt.Errorf("marshal server state: %w", err)
	}

	localBytes, err := os.ReadFile(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return SectionDiff{Status: StatusLocalMissing, Path: localPath}, nil
		}
		return SectionDiff{}, err
	}

	if bytes.Equal(localBytes, serverBytes) {
		return SectionDiff{Status: StatusInSync, Path: localPath}, nil
	}

	diff := SectionDiff{
		Status:      StatusDrift,
		Path:        localPath,
		UnifiedDiff: unifiedDiff(localBytes, serverBytes, "local", "server"),
	}
	diff.AdditiveOnly = yamlIsSubset(localBytes, serverBytes)
	diff.LocalIsSuperset = yamlIsSubset(serverBytes, localBytes)
	// Always enumerate server-only fields when drift exists, regardless of
	// which subset relation holds. The deploy gate uses this list to warn
	// about desired-state field clears (omit-equals-clear), which can fire
	// even when the user is also adding new fields locally.
	diff.ServerOnlyFields = listServerOnlyFields(localBytes, serverBytes)
	return diff, nil
}

// listServerOnlyFields walks the parsed local + server yamls and returns
// dotted-path summaries of fields server has that local doesn't. Used
// by the deploy gate's --force warning to make field deletion explicit.
// Returns nil if either side fails to parse, caller treats absence as
// "we couldn't determine, fall back to the generic diff output".
func listServerOnlyFields(localBytes, serverBytes []byte) []string {
	var local, server map[string]any
	if err := yaml.Unmarshal(localBytes, &local); err != nil {
		return nil
	}
	if err := yaml.Unmarshal(serverBytes, &server); err != nil {
		return nil
	}
	var out []string
	walkServerOnly("", local, server, &out)
	return out
}

// walkServerOnly enumerates fields in server that don't appear in
// local (or whose array contents diverge). Recurses into nested maps
// and into elements of arrays of maps so we surface things like
// servicePortMappings[0].domains.
func walkServerOnly(prefix string, local, server map[string]any, out *[]string) {
	for _, k := range sortedMapKeys(server) {
		sv := server[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		lv, ok := local[k]
		if !ok {
			*out = append(*out, fmt.Sprintf("%s (%s)", path, summarizeValue(sv)))
			continue
		}
		// Recurse into nested maps.
		if sm, ok := sv.(map[string]any); ok {
			if lm, ok := lv.(map[string]any); ok {
				walkServerOnly(path, lm, sm, out)
			}
			continue
		}
		// Recurse into arrays of maps. Differences in array length
		// surface as a summary on the missing index's path.
		if sa, ok := sv.([]any); ok {
			la, _ := lv.([]any)
			for i, sItem := range sa {
				sm, ok := sItem.(map[string]any)
				if !ok {
					continue
				}
				if i >= len(la) {
					*out = append(*out, fmt.Sprintf("%s[%d] (%s)", path, i, summarizeValue(sm)))
					continue
				}
				lm, ok := la[i].(map[string]any)
				if !ok {
					continue
				}
				walkServerOnly(fmt.Sprintf("%s[%d]", path, i), lm, sm, out)
			}
		}
	}
}

// sortedMapKeys returns the keys of m in lexical order.
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// summarizeValue renders a one-line description of a yaml value for
// inclusion in the server-only-fields list. Strings get quoted; arrays
// and maps get a count.
func summarizeValue(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("%q", x)
	case bool:
		return fmt.Sprintf("%t", x)
	case int, int64, float64:
		return fmt.Sprintf("%v", x)
	case []any:
		if len(x) == 1 {
			return "1 entry"
		}
		return fmt.Sprintf("%d entries", len(x))
	case map[string]any:
		if len(x) == 1 {
			return "1 field"
		}
		return fmt.Sprintf("%d fields", len(x))
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ComputeEnvDiff diffs the local env file bytes against the canonical
// rendering of serverVars (same ordering SaveEnv produces).
func ComputeEnvDiff(localPath string, serverVars map[string]string) (SectionDiff, error) {
	diff, err := compareBytes(localPath, RenderEnvBytes(serverVars))
	if err != nil {
		return diff, err
	}
	if diff.Status == StatusDrift {
		localBytes, err := os.ReadFile(localPath)
		if err == nil {
			diff.AdditiveOnly = envIsSubset(parseEnvBytes(localBytes), serverVars)
		}
	}
	return diff, nil
}

// envIsSubset is true when every key in local exists on the server with
// the same value. Server is allowed to have extra keys local doesn't.
func envIsSubset(local, server map[string]string) bool {
	for k, v := range local {
		if sv, ok := server[k]; !ok || sv != v {
			return false
		}
	}
	return true
}

// yamlIsSubset returns true when every field present in localBytes is
// present in serverBytes with the same value. Server may have additional
// fields. Arrays are compared by deep equality (any difference counts as
// drift, since merging-by-position would be ambiguous for our shapes).
func yamlIsSubset(localBytes, serverBytes []byte) bool {
	var local, server map[string]any
	if err := yaml.Unmarshal(localBytes, &local); err != nil {
		return false
	}
	if err := yaml.Unmarshal(serverBytes, &server); err != nil {
		return false
	}
	return mapIsSubset(local, server)
}

func mapIsSubset(local, server map[string]any) bool {
	for k, v := range local {
		sv, ok := server[k]
		if !ok {
			return false
		}
		if !valuesAdditive(v, sv) {
			return false
		}
	}
	return true
}

func valuesAdditive(local, server any) bool {
	switch lv := local.(type) {
	case map[string]any:
		sm, ok := server.(map[string]any)
		if !ok {
			return false
		}
		return mapIsSubset(lv, sm)
	case []any:
		// Arrays must match in full, additive within an array is
		// ambiguous (which entry corresponds to which?), so treat any
		// difference as drift.
		sl, ok := server.([]any)
		if !ok {
			return false
		}
		if len(lv) != len(sl) {
			return false
		}
		for i := range lv {
			if !valuesAdditive(lv[i], sl[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(local, server)
	}
}

// RedactEnvUnifiedDiff replaces values in a `KEY=VALUE`-style unified
// diff with `<redacted>` markers, preserving line markers (+/-/space)
// and diff headers. Used when the diff is being emitted into a context
// where env values are sensitive — most importantly the MCP tool path,
// where output flows into an LLM's context and may be persisted.
//
// Lines that don't look like env entries (diff hunks, headers, blank
// lines) pass through unchanged.
func RedactEnvUnifiedDiff(diff string) string {
	if diff == "" {
		return ""
	}
	var b strings.Builder
	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		if len(line) > 0 {
			marker := line[0]
			if marker == '+' || marker == '-' || marker == ' ' {
				body := line[1:]
				// Skip the unified-diff file headers ("--- local", "+++ server").
				if !(strings.HasPrefix(body, "--") || strings.HasPrefix(body, "++")) {
					if eq := strings.Index(body, "="); eq >= 0 {
						b.WriteByte(marker)
						b.WriteString(body[:eq])
						b.WriteString("=<redacted>")
						if i < len(lines)-1 {
							b.WriteByte('\n')
						}
						continue
					}
				}
			}
		}
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// fileExists reports whether `path` exists and is readable. Returns false
// for any os.Stat error (not-exists, permission denied, etc.) — the env
// diff treats unreadable files as "no local content" rather than failing
// the whole diff.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func compareBytes(localPath string, serverBytes []byte) (SectionDiff, error) {
	localBytes, err := os.ReadFile(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return SectionDiff{Status: StatusLocalMissing, Path: localPath}, nil
		}
		return SectionDiff{}, err
	}
	if bytes.Equal(localBytes, serverBytes) {
		return SectionDiff{Status: StatusInSync, Path: localPath}, nil
	}
	return SectionDiff{
		Status:      StatusDrift,
		Path:        localPath,
		UnifiedDiff: unifiedDiff(localBytes, serverBytes, "local", "server"),
	}, nil
}

func unifiedDiff(localBytes, serverBytes []byte, localLabel, serverLabel string) string {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(localBytes)),
		B:        difflib.SplitLines(string(serverBytes)),
		FromFile: localLabel,
		ToFile:   serverLabel,
		Context:  3,
	}
	out, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return fmt.Sprintf("<unable to compute diff: %v>", err)
	}
	return out
}

// ComputeSecretFilesDiff walks the server-side file list, comparing each
// file's md5 against the equivalent local file on disk. The local path
// for each server file is supplied via localPaths (filename → resolved
// absolute path). Server files not present in localPaths are reported as
// local_missing, they exist on the server but aren't referenced by the
// local yaml's secretFiles block.
func ComputeSecretFilesDiff(localPaths map[string]string, serverFiles []SecretFileSummary) (SecretFilesDiff, error) {
	entries := make([]SecretFileDiff, 0, len(serverFiles))
	driftSeen := false

	for _, sf := range serverFiles {
		entry := SecretFileDiff{
			Filename:  sf.Filename,
			ServerMd5: sf.MD5,
		}
		localPath, ok := localPaths[sf.Filename]
		if !ok {
			entry.Status = StatusLocalMissing
			driftSeen = true
			entries = append(entries, entry)
			continue
		}
		entry.Local = localPath

		data, err := os.ReadFile(localPath)
		if err != nil {
			if os.IsNotExist(err) {
				entry.Status = StatusLocalMissing
				driftSeen = true
				entries = append(entries, entry)
				continue
			}
			return SecretFilesDiff{}, fmt.Errorf("read %s: %w", localPath, err)
		}

		sum := md5.Sum(data)
		entry.LocalMd5 = hex.EncodeToString(sum[:])
		if entry.LocalMd5 == entry.ServerMd5 {
			entry.Status = StatusInSync
		} else {
			entry.Status = StatusDrift
			driftSeen = true
		}
		entries = append(entries, entry)
	}

	agg := StatusInSync
	if driftSeen {
		agg = StatusDrift
	}
	return SecretFilesDiff{Status: agg, Entries: entries}, nil
}

// ResolveLocalSecretPaths produces a filename → resolved-path map from the
// local yaml's secretFiles block, suitable for passing to
// ComputeSecretFilesDiff. Relative paths are resolved against yamlDir.
func ResolveLocalSecretPaths(yamlDir string, files []SecretFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, sf := range files {
		p := sf.Local
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(yamlDir, p)
		}
		out[sf.Filename] = p
	}
	return out
}

// EnrichSecretFileDiffsWithContent fetches the server bytes for each
// drifted or locally-missing secret file and writes a unified diff into
// the entry's UnifiedDiff field. Only drifted/missing entries trigger a
// network call; in-sync entries are left untouched (already confirmed
// identical via md5). Local file paths come from the entry's Local field
//, populated upstream by ComputeSecretFilesDiff from the yaml manifest.
func EnrichSecretFileDiffsWithContent(svc *Service, appID string, files *SecretFilesDiff) error {
	for i := range files.Entries {
		e := &files.Entries[i]
		if e.Status == StatusInSync {
			continue
		}
		content, err := svc.GetSecretFile(appID, e.Filename)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", e.Filename, err)
		}
		decoded, err := base64.StdEncoding.DecodeString(content.Content)
		if err != nil {
			return fmt.Errorf("decode %s: %w", e.Filename, err)
		}
		var localBytes []byte
		if e.Status == StatusDrift && e.Local != "" {
			localBytes, err = os.ReadFile(e.Local)
			if err != nil {
				return fmt.Errorf("read local %s: %w", e.Filename, err)
			}
		}
		e.UnifiedDiff = unifiedDiff(localBytes, decoded, "local", "server")
	}
	return nil
}

// ComputeOverridesDiff walks the server-side overrides list, comparing
// each override's decoded md5 against the md5 of the equivalent local
// file. The local file path for each override id is supplied via
// localPathsByID. Server overrides not present in the map are reported
// as local_missing.
func ComputeOverridesDiff(localPathsByID map[string]string, serverOverrides []OverrideSummary) (OverridesDiff, error) {
	entries := make([]OverrideDiff, 0, len(serverOverrides))
	driftSeen := false

	for _, o := range serverOverrides {
		decoded, err := base64.StdEncoding.DecodeString(o.Data)
		if err != nil {
			return OverridesDiff{}, fmt.Errorf("decode override %s: %w", o.ID, err)
		}
		serverSum := md5.Sum(decoded)
		serverMd5 := hex.EncodeToString(serverSum[:])

		entry := OverrideDiff{
			ID:        o.ID,
			Name:      o.Name,
			ServerMd5: serverMd5,
		}

		localPath, ok := localPathsByID[o.ID]
		if !ok {
			entry.Status = StatusLocalMissing
			entry.UnifiedDiff = unifiedDiff(nil, decoded, "local", "server")
			driftSeen = true
			entries = append(entries, entry)
			continue
		}
		entry.Local = localPath

		data, err := os.ReadFile(localPath)
		if err != nil {
			if os.IsNotExist(err) {
				entry.Status = StatusLocalMissing
				entry.UnifiedDiff = unifiedDiff(nil, decoded, "local", "server")
				driftSeen = true
				entries = append(entries, entry)
				continue
			}
			return OverridesDiff{}, fmt.Errorf("read %s: %w", localPath, err)
		}

		localSum := md5.Sum(data)
		entry.LocalMd5 = hex.EncodeToString(localSum[:])
		if entry.LocalMd5 == entry.ServerMd5 {
			entry.Status = StatusInSync
		} else {
			entry.Status = StatusDrift
			entry.UnifiedDiff = unifiedDiff(data, decoded, "local", "server")
			driftSeen = true
		}
		entries = append(entries, entry)
	}

	agg := StatusInSync
	if driftSeen {
		agg = StatusDrift
	}
	return OverridesDiff{Status: agg, Entries: entries}, nil
}

// ResolveLocalOverridePaths produces an override-id → resolved-path map
// from the local yaml's overrides block, keyed by server override id.
// Relative paths are resolved against yamlDir.
func ResolveLocalOverridePaths(yamlDir string, overrides []Override) map[string]string {
	out := make(map[string]string, len(overrides))
	for _, o := range overrides {
		if o.ID == "" || o.Local == "" {
			continue
		}
		p := o.Local
		if !filepath.IsAbs(p) {
			p = filepath.Join(yamlDir, p)
		}
		out[o.ID] = p
	}
	return out
}

// BuildDiffReport runs the same per-section diff that "runos apps diff"
// produces for the app described by localApp. yamlPath is the on-disk
// location of the manifest (used both for the yaml-bytes comparison and
// to resolve relative env/secret/override paths). expectedAID and
// expectedCID are validated against localApp's own fields so the caller
// can't accidentally diff against the wrong account or cluster.
//
// Caller is responsible for loading localApp via LoadLocalApp and
// confirming id/cid/aid are populated. Returns errors for context
// mismatches and HTTP/parse failures.
func BuildDiffReport(svc *Service, localApp *PulledApp, yamlPath, expectedAID, expectedCID string) (*DiffReport, error) {
	if localApp.AID != expectedAID {
		return nil, fmt.Errorf("yaml is for account %q but you're logged in as %q", localApp.AID, expectedAID)
	}
	if localApp.CID != expectedCID {
		return nil, fmt.Errorf("cluster mismatch: yaml is for cluster %q but --cid (or default) is %q", localApp.CID, expectedCID)
	}

	raw, err := svc.GetApp(localApp.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch app: %w", err)
	}
	secretEnvVars, err := svc.GetAppSecretEnvVars(localApp.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch secret env vars: %w", err)
	}
	envVars, err := svc.GetAppEnvVars(localApp.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch env vars: %w", err)
	}
	secretFiles, err := svc.ListSecretFiles(localApp.ID)
	if err != nil {
		return nil, fmt.Errorf("list secret files: %w", err)
	}
	overrides, err := svc.ListOverrides(localApp.ID)
	if err != nil {
		return nil, fmt.Errorf("list overrides: %w", err)
	}
	requires, err := svc.GetAppRequires(localApp.ID)
	if err != nil {
		return nil, fmt.Errorf("read requires: %w", err)
	}

	serverState := BuildServerStateForDiff(raw, localApp.CID, localApp.AID, secretEnvVars, envVars, secretFiles, overrides, requires)
	if serverState.App == "" {
		serverState.App = localApp.App
	}
	if serverState.ID == "" {
		serverState.ID = localApp.ID
	}

	// Class is local-only (conductor doesn't store it); legacy apps may
	// also return empty Config/Env that the local yaml fills in. Merge
	// covers both so the diff doesn't report false drift.
	MergeRequiresUserAuthored(serverState, localApp)

	yamlDir := filepath.Dir(yamlPath)
	yamlDiff, err := ComputeYAMLDiff(yamlPath, serverState)
	if err != nil {
		return nil, fmt.Errorf("yaml diff: %w", err)
	}

	// V3 fix: compute the env diff sections whenever EITHER side has
	// content. The pre-fix gate (`len(serverVars) > 0`) ignored a local
	// file at the documented default path when the server had zero vars,
	// reporting `in_sync` while the file's contents were silently absent
	// from the cluster. Now: server has content OR local file exists at
	// the resolved path → compute and surface drift; both empty → leave
	// the default `in_sync`.
	secretEnvDiff := SectionDiff{Status: StatusInSync}
	secretField := localApp.SecretEnv
	if secretField == "" {
		secretField = SecretEnvFilename(localApp.CID, localApp.ID)
	}
	secretPath := secretField
	if !filepath.IsAbs(secretPath) {
		secretPath = filepath.Join(yamlDir, secretPath)
	}
	if len(secretEnvVars) > 0 || fileExists(secretPath) {
		secretEnvDiff, err = ComputeEnvDiff(secretPath, secretEnvVars)
		if err != nil {
			return nil, fmt.Errorf("secret env diff: %w", err)
		}
	}

	envDiff := SectionDiff{Status: StatusInSync}
	envField := localApp.Env
	if envField == "" {
		envField = EnvFilename(localApp.CID, localApp.ID)
	}
	envPath := envField
	if !filepath.IsAbs(envPath) {
		envPath = filepath.Join(yamlDir, envPath)
	}
	if len(envVars) > 0 || fileExists(envPath) {
		envDiff, err = ComputeEnvDiff(envPath, envVars)
		if err != nil {
			return nil, fmt.Errorf("env diff: %w", err)
		}
	}

	secretFilesDiff := SecretFilesDiff{Status: StatusInSync, Entries: []SecretFileDiff{}}
	if len(secretFiles) > 0 {
		localPaths := ResolveLocalSecretPaths(yamlDir, localApp.SecretFiles)
		secretFilesDiff, err = ComputeSecretFilesDiff(localPaths, secretFiles)
		if err != nil {
			return nil, fmt.Errorf("secret files diff: %w", err)
		}
	}

	overridesDiff := OverridesDiff{Status: StatusInSync, Entries: []OverrideDiff{}}
	if len(overrides) > 0 {
		localPaths := ResolveLocalOverridePaths(yamlDir, localApp.Overrides)
		overridesDiff, err = ComputeOverridesDiff(localPaths, overrides)
		if err != nil {
			return nil, fmt.Errorf("overrides diff: %w", err)
		}
	}

	// Code-version status is best-effort: if the sidecar is missing or
	// the archive list endpoint hiccups we still return the rest of the
	// report. ListCliArchives only fires when a sidecar exists.
	code, err := ComputeCodeVersionStatus(svc, localApp.CID, localApp.ID, yamlDir)
	if err != nil {
		// Fail-open: the section is informational; surface the error
		// in the report's Code field as a missing baseline rather than
		// blocking the whole diff.
		code = nil
	}

	return &DiffReport{
		CID:         localApp.CID,
		AppID:       serverState.ID,
		AppName:     serverState.App,
		Notes:       buildClassFlapNotes(localApp, raw),
		YAML:        yamlDiff,
		SecretEnv:   secretEnvDiff,
		Env:         envDiff,
		SecretFiles: secretFilesDiff,
		Overrides:   overridesDiff,
		Code:        code,
	}, nil
}

// buildClassFlapNotes detects the resourceRequirementClassId custom-synthesis
// flap and returns at most one heads-up line. The flap pattern: local yaml
// names a real class (app.sl1.beff, valkey.c0.small, etc.) but server
// returned `custom` because at least one of cpu/mem/replicas was overridden
// to a value that disagrees with the named class's defaults
// (`util/services/resolveRRC.ts:hasCustomOverrides`). On every subsequent
// pull/diff this looks like genuine class drift even though it's working as
// designed. The note nudges the user toward the round-trip-clean fix
// without explaining which field caused the flip (the field-level diff
// already shows that).
func buildClassFlapNotes(localApp *PulledApp, server map[string]any) []string {
	if localApp == nil || server == nil {
		return nil
	}
	localClass := localApp.ResourceRequirementClassID
	serverClass := stringOr(server, "resourceRequirementClassId")
	if serverClass != "custom" || localClass == "" || localClass == "custom" {
		return nil
	}
	return []string{
		"server stored resourceRequirementClassId=custom (overrides on cpu/memory/replicas disagree " +
			"with " + localClass + "'s defaults). To round-trip cleanly: drop the override, or set " +
			"resourceRequirementClassId: custom and pin all of cpu/memory/replicas explicitly.",
	}
}

// BuildServerStateForDiff produces the PulledApp shape the server would
// pull into, same ordering + fields as the real pull flow, so yaml diffs
// are meaningful. The `local` paths inside the yaml are leaf-relative
// (".env", ".secret-files/foo", "overrides/bar.yaml"), resolved against
// the per-app directory the yaml itself lives in. Does NOT fetch secret
// file content; overrides are decoded here just to compute md5 fingerprints.
//
// requires is the runos.yaml-shaped map returned by /requires:
// alias -> {Type, ID, Config, Env}. Pass nil when the listing isn't
// available (the field is then omitted via omitempty); pass an empty
// non-nil map to assert "this app has no dependencies". Class is never
// returned by the server; pull merges it from the local yaml.
func BuildServerStateForDiff(raw map[string]any, cid, aid string, secretEnvVars, envVars map[string]string, secretFiles []SecretFileSummary, overrides []OverrideSummary, requires map[string]ServiceRequirement) *PulledApp {
	p := BuildPulledApp(raw, cid, aid)

	if requires != nil {
		// Take a defensive copy so callers' maps aren't aliased into
		// the returned PulledApp. Empty non-nil input still produces
		// an empty (non-nil) Requires map; omitempty drops it from
		// the yaml output.
		p.Requires = make(map[string]ServiceRequirement, len(requires))
		for alias, r := range requires {
			if alias == "" {
				continue
			}
			p.Requires[alias] = r
		}
	}

	if len(secretEnvVars) > 0 {
		p.SecretEnv = SecretEnvFilename(p.CID, p.ID)
	}
	if len(envVars) > 0 {
		p.Env = EnvFilename(p.CID, p.ID)
	}

	if len(secretFiles) > 0 {
		localDir := SecretFilesDirname()
		p.SecretFiles = make([]SecretFile, 0, len(secretFiles))
		for _, sf := range secretFiles {
			p.SecretFiles = append(p.SecretFiles, SecretFile{
				Filename:  sf.Filename,
				MountPath: sf.MountPath,
				Local:     filepath.Join(localDir, sf.Filename),
				MD5:       sf.MD5,
			})
		}
	}

	if len(overrides) > 0 {
		localDir := OverridesDirname()
		filenames := OverrideFilenames(overrides)
		p.Overrides = make([]Override, 0, len(overrides))
		for i, o := range overrides {
			decoded, err := base64.StdEncoding.DecodeString(o.Data)
			sum := ""
			if err == nil {
				h := md5.Sum(decoded)
				sum = hex.EncodeToString(h[:])
			}
			p.Overrides = append(p.Overrides, Override{
				ID:      o.ID,
				Name:    o.Name,
				Enabled: o.Enabled,
				Local:   filepath.Join(localDir, filenames[i]),
				MD5:     sum,
			})
		}
	}

	return p
}
