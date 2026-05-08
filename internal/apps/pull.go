package apps

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// PulledApp is the shape we write to disk when down-syncing an app's config.
// Field order matches the intended on-disk ordering because gopkg.in/yaml.v3
// emits keys in struct order.
type PulledApp struct {
	App        string `yaml:"app"`
	DeployType string `yaml:"deployType"`
	ID         string `yaml:"id"`
	CID        string `yaml:"cid"`
	AID        string `yaml:"aid"`
	// SecretEnv: relative path to the file holding sensitive (Secret-backed)
	// env vars. Default `.runos.{cid}.{id}.env`. Gitignored — never commit.
	SecretEnv  string `yaml:"secretEnv,omitempty"`
	// Env: relative path to the file holding plain (ConfigMap-backed) env
	// vars committed to VCS. Default `runos.{cid}.{id}.config.env`.
	Env        string `yaml:"env,omitempty"`
	// SourceDir is the path (relative to the yaml's directory) to the
	// build context tarballed by `runos deploy`. Empty defaults to "."
	// (the yaml's own directory). Set to ".." when the yaml lives in a
	// per-app subdirectory and the actual source code lives at the
	// parent (project root). Inert for VCS-deployed apps because CI
	// owns the build context.
	SourceDir string `yaml:"sourceDir,omitempty"`
	Replicas  int    `yaml:"replicas"`

	ClusterDomainID string `yaml:"clusterDomainId,omitempty"`

	// Exactly one of ResourceRequirementClassID (preset) or the four mc/mb
	// pointers (custom) is populated by BuildPulledApp.
	ResourceRequirementClassID string `yaml:"resourceRequirementClassId,omitempty"`
	CPURequestMc               *int   `yaml:"cpuRequestMc,omitempty"`
	CPULimitMc                 *int   `yaml:"cpuLimitMc,omitempty"`
	MemoryRequestMb            *int   `yaml:"memoryRequestMb,omitempty"`
	MemoryLimitMb              *int   `yaml:"memoryLimitMb,omitempty"`

	Integration         *Integration `yaml:"integration,omitempty"`
	ServicePortMappings []Port       `yaml:"servicePortMappings"`

	// Health check and metrics use the flat wire shape. Conductor's
	// API returns these as top-level fields, deploy sends them flat
	// (DeployConfig.HealthCheck/HealthCheckPort/HealthCheckPath +
	// MetricsPort/MetricsPath), and matching here keeps yamls round-
	// tripping byte-clean between pull and deploy.
	HealthCheck     string `yaml:"healthCheck,omitempty"`
	HealthCheckPort *int   `yaml:"healthCheckPort,omitempty"`
	HealthCheckPath string `yaml:"healthCheckPath,omitempty"`
	MetricsPort     *int   `yaml:"metricsPort,omitempty"`
	MetricsPath     string `yaml:"metricsPath,omitempty"`

	// SecretFiles is intentionally omitted from the yaml when empty. A
	// present-but-empty `secretFiles: []` in the file is authoritative and
	// would mean "no secret files" on a future up-sync, while omission
	// means "don't touch secret files", two different semantics.
	SecretFiles []SecretFile `yaml:"secretFiles,omitempty"`

	// Overrides follows the same "absent = hands off" rule as SecretFiles.
	Overrides []Override `yaml:"overrides,omitempty"`

	// Requires maps an alias (== service display name) to the linked
	// service. Pull populates Type and ID from conductor's
	// /apps/:id/dependencies endpoint. Class, Config, and Env are
	// user-authored and preserved across re-pulls (see pickRequires);
	// the CLI never fetches or pushes them on its own. Class is treated
	// as creation-time infra spec (lives in console / future service
	// IAC) and should be dropped after first deploy.
	Requires map[string]ServiceRequirement `yaml:"requires,omitempty"`
}

// MergeRequiresUserAuthored reconciles a server-built Requires map with
// the user's local yaml. The /apps/:id/requires endpoint is the
// authoritative source for Type, ID, Config, and Env; Class is the only
// remaining local-only field (conductor doesn't store or return it).
//
// Migration safety net: apps deployed before the requires-reader landed
// have edges with no metadata; the server returns empty Config and Env
// for those aliases. When that happens AND the local yaml has populated
// values, this function preserves the local values so the user's
// existing config is not silently wiped on first pull. The next
// `runos deploy` ships those local values to the server, and subsequent
// pulls return populated values from /requires (server wins).
//
// Aliases present in target but not in local: untouched (no Class to
// merge, server already provides the rest). Aliases in local but not
// in target: dropped, the link no longer exists on the server.
//
// No-op when either argument is nil or local has no Requires map.
func MergeRequiresUserAuthored(target, local *PulledApp) {
	if target == nil || local == nil || len(local.Requires) == 0 {
		return
	}
	for alias, srv := range target.Requires {
		loc, ok := local.Requires[alias]
		if !ok {
			continue
		}
		// Class is always local-authored.
		srv.Class = loc.Class
		// Config / Env: server wins when populated; fall back to local
		// for legacy apps where the server returns empty maps.
		if len(srv.Config) == 0 && len(loc.Config) > 0 {
			srv.Config = loc.Config
		}
		if len(srv.Env) == 0 && len(loc.Env) > 0 {
			srv.Env = loc.Env
		}
		target.Requires[alias] = srv
	}
}

// ServiceRequirement mirrors deploy.ServiceRequirement so pulled yamls
// re-deploy byte-clean. JSON tags target conductor's
// /apps/:id/requires response, which is the authoritative source
// for everything except Class.
//
//   - ID: service id (5-char). Server-authoritative.
//   - Type: service type (postgresql, valkey, mysql). Server-authoritative.
//   - Class: resource class (e.g. postgresql.c0.tiny). Local-only;
//     conductor doesn't store or return it. Sent on `runos deploy` at
//     create time and ignored on update. The user keeps it in their
//     yaml as long as they want; pull never touches it.
//   - Config: service-specific logical config (databaseName, etc.).
//     Server-authoritative via /requires. Legacy apps deployed
//     before that endpoint return empty Config; pull falls back to
//     local in that case so the next deploy can populate the server.
//   - Env: credential-field-to-env-var mapping. Same server-authoritative
//     story as Config, with the same legacy fallback.
type ServiceRequirement struct {
	ID     string            `yaml:"id,omitempty" json:"id,omitempty"`
	Type   string            `yaml:"type" json:"type,omitempty"`
	Class  string            `yaml:"class,omitempty" json:"-"`
	Config map[string]any    `yaml:"config,omitempty" json:"config,omitempty"`
	Env    map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// Override references a kubectl manifest override configured for the app.
// The actual override body lives on disk at Local; MD5 is a fingerprint of
// those bytes for drift detection.
type Override struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name,omitempty"`
	Enabled bool   `yaml:"enabled"`
	Local   string `yaml:"local"`
	MD5     string `yaml:"md5,omitempty"`
}

// SecretFile references a secret file mounted into the app container. The
// actual file bytes live on disk at Local (outside the yaml) to keep the
// yaml scannable. MD5 is the server-reported content fingerprint.
type SecretFile struct {
	Filename  string `yaml:"filename"`
	MountPath string `yaml:"mountPath"`
	Local     string `yaml:"local"`
	MD5       string `yaml:"md5,omitempty"`
}

// Integration captures VCS integration details. Only populated for
// non-cli deployments (gitlab-runner, github-actions, etc.).
type Integration struct {
	ID         string `yaml:"id,omitempty"`
	RepoID     int64  `yaml:"repoId,omitempty"`
	RepoName   string `yaml:"repoName,omitempty"`
	BranchName string `yaml:"branchName,omitempty"`
}

// MappingDomain mirrors the conductor's MappingDomain. One FQDN attached to
// a port mapping with an optional Cloudflare proxy flag.
type MappingDomain struct {
	Fqdn                  string `yaml:"fqdn"`
	EnableCloudflareProxy bool   `yaml:"enableCloudflareProxy,omitempty"`
}

// Port models a single service port exposure.
//
// Domains are FQDNs that route to this port. They are populated by
// BuildPulledApp from the server response (which joins the standalone
// domains table by targetService=osid + targetPort=port and also includes
// the proxied flag from providerOptions.proxy) and round-trip cleanly
// through `runos deploy`, so a pulled yaml can be deployed as-is.
type Port struct {
	Port          int             `yaml:"port"`
	StandardHttps bool            `yaml:"standardHttps"`
	Domains       []MappingDomain `yaml:"domains,omitempty"`
}

// BuildPulledApp projects a raw apps/:id response into a PulledApp. It is
// tolerant of missing fields: anything absent is left at the zero value and
// gets omitted via `omitempty` where applicable.
func BuildPulledApp(raw map[string]any, cid, aid string) *PulledApp {
	p := &PulledApp{
		CID:                 cid,
		AID:                 aid,
		App:                 stringOr(raw, "name"),
		ID:                  stringOr(raw, "id"),
		ClusterDomainID:     stringOr(raw, "clusterDomainId"),
		ServicePortMappings: []Port{},
		// SecretFiles intentionally left nil; the pull command appends
		// entries as files are fetched, and omitempty drops the key from
		// the YAML when the app has none.
	}

	if n, ok := asInt(raw["replicas"]); ok {
		p.Replicas = n
	}

	// Deploy type: prefer the server's `deployType` ('cli' | 'vcs') from the
	// canonical contract. Fall back to deriving from `integrationType` for
	// legacy conductors that pre-date the deployType field on the GET
	// response (treat any non-empty integrationType as a VCS app).
	//
	// Earlier versions of this code conflated the two fields and stored the
	// provider name ('github-arc', 'gitlab-runner') as DeployType, producing
	// perpetual drift on apps_diff because the server emits 'vcs' while the
	// local yaml carried 'github-arc'. Provider identity now flows via the
	// Integration block alongside repo/branch metadata, not via DeployType.
	integrationType, _ := raw["integrationType"].(string)
	deployType, _ := raw["deployType"].(string)
	if deployType == "" {
		if integrationType == "" {
			deployType = "cli"
		} else {
			deployType = "vcs"
		}
	}
	p.DeployType = deployType

	if integrationType != "" {
		intg := &Integration{
			ID:         stringOr(raw, "vcsIntegrationId"),
			RepoID:     int64Or(raw, "repoId"),
			RepoName:   stringOr(raw, "repoName"),
			BranchName: stringOr(raw, "branchName"),
		}
		if *intg != (Integration{}) {
			p.Integration = intg
		}
	}

	// Resources: preset wins when it's a real class, otherwise emit all
	// four custom fields (including zeros, so the user knows they must set them).
	//
	// "custom" is the server-synthesised label for "user supplied a class
	// AND overrode at least one of cpu/memory/replicas with a value that
	// disagrees with the class defaults" (see conductor's resolveRRC).
	// We preserve the literal "custom" string in the pulled yaml so
	// apps_show and apps_pull return symmetric views (apps_show already
	// surfaces resourceRequirementClassId="custom"). Materialised
	// cpu/memory fields are still emitted alongside, since "custom" by
	// itself carries no values.
	rrc := stringOr(raw, "resourceRequirementClassId")
	if rrc != "" && rrc != "custom" {
		p.ResourceRequirementClassID = rrc
	} else {
		if rrc == "custom" {
			p.ResourceRequirementClassID = "custom"
		}
		cpuReq := 0
		if v, ok := asInt(raw["cpuRequestMc"]); ok {
			cpuReq = v
		}
		cpuLim := 0
		if v, ok := asInt(raw["cpuLimitMc"]); ok {
			cpuLim = v
		}
		memReq := 0
		if v, ok := asInt(raw["memoryRequestMb"]); ok {
			memReq = v
		}
		memLim := 0
		if v, ok := asInt(raw["memoryLimitMb"]); ok {
			memLim = v
		}
		p.CPURequestMc = &cpuReq
		p.CPULimitMc = &cpuLim
		p.MemoryRequestMb = &memReq
		p.MemoryLimitMb = &memLim
	}

	// Service port mappings: drawn from servicePortMappings. Leave as empty
	// slice when the app is a pure background worker with no exposed ports.
	// `domains` per mapping is populated server-side from the standalone
	// domains table (see getAppDetails in conductor) and arrives as
	// [{fqdn, proxied}], matching the canonical AppSpec shape.
	if arr, ok := raw["servicePortMappings"].([]any); ok {
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			port, _ := asInt(m["port"])
			stdHTTPS, _ := m["standardHttps"].(bool)
			var domains []MappingDomain
			if rawDomains, ok := m["domains"].([]any); ok {
				for _, d := range rawDomains {
					switch v := d.(type) {
					case string:
						// Defensive: handle responses from older conductors
						// that still emit string FQDNs without the object wrapper.
						domains = append(domains, MappingDomain{Fqdn: v})
					case map[string]any:
						fqdn, _ := v["fqdn"].(string)
						if fqdn == "" {
							continue
						}
						enableCloudflareProxy, _ := v["enableCloudflareProxy"].(bool)
						domains = append(domains, MappingDomain{Fqdn: fqdn, EnableCloudflareProxy: enableCloudflareProxy})
					}
				}
			}
			p.ServicePortMappings = append(p.ServicePortMappings, Port{
				Port:          port,
				StandardHttps: stdHTTPS,
				Domains:       domains,
			})
		}
	}

	// Health check: copy flat fields verbatim. The preset string is the
	// gate, everything else is omitted via omitempty when zero.
	if preset := stringOr(raw, "healthCheck"); preset != "" {
		p.HealthCheck = preset
		if n, ok := asInt(raw["healthCheckPort"]); ok && n > 0 {
			p.HealthCheckPort = &n
		}
		p.HealthCheckPath = stringOr(raw, "healthCheckPath")
	}

	// Metrics: emitted only when a scrape port is set.
	if port, ok := asInt(raw["metricsPort"]); ok && port > 0 {
		p.MetricsPort = &port
		p.MetricsPath = stringOr(raw, "metricsPath")
	}

	return p
}

func stringOr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func int64Or(m map[string]any, key string) int64 {
	switch n := m[key].(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// SanitizeName returns a filesystem-safe version of an app name. Spaces,
// slashes, and other awkward characters become '-'.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "app"
	}
	return out
}

// DefaultBaseName returns the per-app directory name. Each app pulled from
// a cluster lives in its own directory so the yaml and any pulled source
// code sit side-by-side, matching what `runos deploy` expects (yaml at the
// project root).
// Example: DefaultBaseName("k1", "appid4") -> "runos.k1.appid4"
func DefaultBaseName(cid, appID string) string {
	return strings.ToLower(fmt.Sprintf("runos.%s.%s", cid, appID))
}

// ValidateIdentifier rejects identifiers (cid / app id) that contain
// path separators, parent-directory references, leading dots, or any
// character outside the conductor's identifier alphabet
// ([A-Za-z0-9_-]). Used to guard against a malicious or compromised
// server response containing an id like `appid4/../../tmp/evil`, which
// would otherwise traverse out of the per-app directory when joined
// into a filesystem path via DefaultBaseName / EnvFilename.
//
// kind is the human-readable name of the field for error messages
// ("app id", "cluster id", etc.).
func ValidateIdentifier(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", kind)
	}
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return fmt.Errorf("%s %q contains invalid character %q at position %d (allowed: alphanumeric, dash, underscore)", kind, value, r, i)
		}
	}
	return nil
}

// SuffixedYAMLFilename returns the per-app yaml leaf name for
// (cid, appID): "runos.<cid>.<appID>.yaml". Lowercased to match
// EnvFilename. Used when a directory already holds a sibling app's
// runos.yaml and a re-pull would otherwise clobber it.
func SuffixedYAMLFilename(cid, appID string) string {
	return strings.ToLower(fmt.Sprintf("runos.%s.%s.yaml", cid, appID))
}

// YAMLFilename returns the yaml leaf name to use for an app's manifest
// inside appDir, picking either "runos.yaml" or the per-app suffixed
// "runos.<cid>.<appID>.yaml" so multiple apps can coexist in one
// directory without clobbering each other.
//
// Resolution order:
//
//  1. If runos.<cid>.<appID>.yaml already exists in appDir, return it.
//     This is a re-pull of an app that already lives in a multi-yaml
//     directory.
//  2. If runos.yaml exists in appDir AND parses as the same (cid, id),
//     return "runos.yaml". Single-app project re-pulls keep the
//     canonical name.
//  3. If no runos*.yaml exists in appDir, return "runos.yaml". Fresh
//     pull into an empty directory matches today's behaviour.
//  4. Otherwise (a runos*.yaml exists for a different app, or a
//     runos.yaml that doesn't parse as a pulled-app yaml), return
//     "runos.<cid>.<appID>.yaml" so the new pull lands beside the
//     existing manifest instead of overwriting it.
//
// Errors only on filesystem I/O failures other than "not exists"; a
// non-existent appDir returns the default "runos.yaml" with no error
// because ensureAppDir will create it on the subsequent write.
func YAMLFilename(appDir, cid, appID string) (string, error) {
	suffixed := SuffixedYAMLFilename(cid, appID)

	if _, err := os.Lstat(filepath.Join(appDir, suffixed)); err == nil {
		return suffixed, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", suffixed, err)
	}

	entries, err := os.ReadDir(appDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "runos.yaml", nil
		}
		return "", fmt.Errorf("read %s: %w", appDir, err)
	}

	var sawRunosYaml, sawSibling bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "runos.yaml" {
			sawRunosYaml = true
			continue
		}
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "runos") {
			continue
		}
		if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			sawSibling = true
		}
	}

	if sawRunosYaml {
		data, readErr := os.ReadFile(filepath.Join(appDir, "runos.yaml"))
		if readErr == nil {
			var existing PulledApp
			if yaml.Unmarshal(data, &existing) == nil && existing.CID == cid && existing.ID == appID {
				return "runos.yaml", nil
			}
		}
		// Unreadable, unparseable, or a different app's manifest:
		// treat the slot as occupied so we don't clobber it.
		sawSibling = true
	}

	if sawSibling {
		return suffixed, nil
	}
	return "runos.yaml", nil
}

// SecretEnvFilename returns the leaf name of the sensitive (Secret-backed)
// env file inside a per-app directory. Mirrors deploy.DefaultSecretEnvFilename
// so a yaml written by `apps pull` round-trips through `runos deploy`. The
// leading dot keeps it gitignored by default.
func SecretEnvFilename(cid, appID string) string {
	return strings.ToLower(fmt.Sprintf(".runos.%s.%s.env", cid, appID))
}

// EnvFilename returns the leaf name of the plain (ConfigMap-backed) env file
// inside a per-app directory. Mirrors deploy.DefaultEnvFilename. No leading
// dot — this file IS committed to VCS.
func EnvFilename(cid, appID string) string {
	return strings.ToLower(fmt.Sprintf("runos.%s.%s.config.env", cid, appID))
}

// ConfigPathMismatchWarning returns a non-empty warning when a VCS-deploy
// app's server-stored `configPath` differs from where apps_pull is about
// to write the local yaml. Empty otherwise.
//
// V2 fix: previously, apps_pull would write the local yaml to its own
// canonical naming (`runos.<cid>.<id>.yaml`) without any awareness of the
// server-side configPath. After the next VCS deploy, the cluster agent
// would fetch the yaml from the OLD configPath on the committed tree and
// fail with "yaml not found" because the user committed it at the new
// path. This warning nudges users to run `apps_update --configPath <new>`
// after committing.
//
// Pure helper: the caller computes the repo-relative local path (typically
// via `git rev-parse --show-toplevel` + filepath.Rel) and passes it in.
// Empty input on either side suppresses the warning, the caller doesn't
// have enough information to assert mismatch.
func ConfigPathMismatchWarning(serverConfigPath, localRepoRelPath, deployType string) string {
	if deployType != "vcs" {
		return ""
	}
	if serverConfigPath == "" || localRepoRelPath == "" {
		return ""
	}
	if serverConfigPath == localRepoRelPath {
		return ""
	}
	return fmt.Sprintf(
		"server stores configPath=%q but the local yaml will be written to %q. "+
			"After committing the new layout, run `runos apps_update --configPath %s` "+
			"to keep the next VCS deploy from reading the yaml at the OLD path.",
		serverConfigPath, localRepoRelPath, localRepoRelPath,
	)
}

// SecretFilesDirname returns the secret-files directory leaf name inside
// a per-app directory.
func SecretFilesDirname() string {
	return ".secret-files"
}

// OverridesDirname returns the overrides directory leaf name inside a
// per-app directory.
func OverridesDirname() string {
	return "overrides"
}

// AppDir returns the per-app directory path: <parentDir>/<base>.
// When base is empty, the returned path is just parentDir (cleaned),
// which is what callers want for "flat" pulls (yaml-anchored or
// --app-id with --out, the named directory is itself the per-app dir).
func AppDir(parentDir, base string) string {
	return filepath.Join(parentDir, base)
}

// YAMLScan splits files matching the runos*.yaml filename pattern into
// two buckets so callers can give precise errors instead of a flat
// "no yaml found":
//
//   - Valid:    file exists and parses as a pulled-app yaml (id/cid/aid set).
//   - Partial:  filename matches and parses as YAML, but is missing id/cid/aid.
//               Typically a fresh, pre-deploy runos.yaml that was never pulled.
type YAMLScan struct {
	Valid   []string
	Partial []string
}

// FindPulledYAMLs scans dir for files whose names contain "runos"
// (case-insensitive) and end in .yaml or .yml. Files that parse as a
// pulled-app yaml (id+cid+aid all populated) are returned in Valid;
// files that match the filename but are missing those fields are returned
// in Partial so the caller can tell the user "you have a runos.yaml here
// but it hasn't been pulled yet" instead of the misleading "no yaml found".
//
// Used by apps pull/diff/sync to auto-detect the per-app yaml when the
// user runs the command from inside an app directory without arguments.
func FindPulledYAMLs(dir string) (YAMLScan, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return YAMLScan{}, err
	}
	scan := YAMLScan{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "runos") {
			continue
		}
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		app, err := loadPulledAppLite(path)
		if err != nil {
			// File exists with the right name but can't be parsed as YAML.
			// Treat as partial so the user is told what's there, even if
			// the file is unreadable.
			scan.Partial = append(scan.Partial, path)
			continue
		}
		if app.ID == "" || app.CID == "" || app.AID == "" {
			scan.Partial = append(scan.Partial, path)
			continue
		}
		scan.Valid = append(scan.Valid, path)
	}
	return scan, nil
}

// loadPulledAppLite is a minimal yaml read that pulls just enough
// fields to validate the file is a pulled-app manifest. Mirrors
// LoadLocalApp (which lives in sync.go) without the import cycle when
// callers are inside the apps package.
func loadPulledAppLite(path string) (*PulledApp, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var app PulledApp
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

// OverrideFilenames returns a filename for each OverrideSummary, stable
// across a single pull and collision-free within the batch. Falls back
// to a shortened id when two overrides sanitize to the same name.
func OverrideFilenames(overrides []OverrideSummary) []string {
	names := make([]string, len(overrides))
	seen := make(map[string]int)
	for i, o := range overrides {
		base := sanitizeOverrideName(o.Name)
		if base == "" {
			base = shortID(o.ID)
		}
		names[i] = base + ".yaml"
		seen[names[i]]++
	}
	for i, o := range overrides {
		if seen[names[i]] > 1 {
			base := sanitizeOverrideName(o.Name)
			if base == "" {
				base = "override"
			}
			names[i] = base + "-" + shortID(o.ID) + ".yaml"
		}
	}
	return names
}

// sanitizeOverrideName lowercases name, replaces awkward characters
// with `-`, collapses runs of dashes, and trims leading/trailing
// dashes. Returns "" when the input contains nothing usable, when the
// only output would be `.` or `..` (which mean special things to the
// filesystem), or when the result starts with `.` (which would create
// a hidden file the user wouldn't notice in a listing).
func sanitizeOverrideName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	out = strings.TrimLeft(out, ".")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

func shortID(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}

// ValidateSecretFilename rejects filenames that could escape the secret
// files directory. Filenames from the server should be bare (no path
// separators); anything else is treated as a server-side bug or tampering.
func ValidateSecretFilename(name string) error {
	if name == "" {
		return fmt.Errorf("secret filename is empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("secret filename contains a path separator: %q", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("secret filename cannot be %q", name)
	}
	return nil
}

// WriteResult describes what happened when writing a single file. InSync
// means the on-disk bytes already matched, so no write was performed.
type WriteResult struct {
	Path   string `json:"path"`
	InSync bool   `json:"inSync,omitempty"`
}

// writeIfNeeded writes newContent to path unless the on-disk file already
// holds identical bytes. The drift-vs-overwrite decision lives one level
// up in the caller (pull / diff); by the time this helper runs the caller
// has decided it's OK to clobber whatever is there.
//
// Uses O_NOFOLLOW on the write so a malicious local user (or process)
// can't plant a symlink at the target path and trick us into writing
// secret content elsewhere on disk. This is a defence-in-depth measure;
// the per-app directory is normally user-private. Mode is applied on
// both create and re-write paths so a 0600 secret never keeps a looser
// pre-existing mode.
func writeIfNeeded(path string, newContent []byte, mode fs.FileMode) (WriteResult, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return WriteResult{}, err
	}
	if err == nil && bytes.Equal(existing, newContent) {
		if chmodErr := os.Chmod(path, mode); chmodErr != nil && !os.IsNotExist(chmodErr) {
			return WriteResult{}, fmt.Errorf("failed to chmod %s: %w", filepath.Base(path), chmodErr)
		}
		return WriteResult{Path: path, InSync: true}, nil
	}
	// O_NOFOLLOW: refuse to write through a symlink. Combined with
	// O_TRUNC, an existing regular file gets clobbered (intended); an
	// existing symlink at the same path returns ELOOP and we surface
	// that as an error rather than silently following.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|nofollowOpenFlag, mode)
	if err != nil {
		return WriteResult{}, fmt.Errorf("failed to open %s: %w", filepath.Base(path), err)
	}
	if _, err := f.Write(newContent); err != nil {
		f.Close()
		return WriteResult{}, fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("failed to close %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return WriteResult{}, fmt.Errorf("failed to chmod %s: %w", filepath.Base(path), err)
	}
	return WriteResult{Path: path}, nil
}

// ensureAppDir makes sure the per-app directory exists with 0755 perms.
func ensureAppDir(parentDir, base string) (string, error) {
	dir := AppDir(parentDir, base)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", dir, err)
	}
	return dir, nil
}

// SaveYAML marshals a PulledApp and writes it to the resolved leaf
// inside <parentDir>/<base>/. The leaf is YAMLFilename(appDir, cid, id),
// which prefers "runos.yaml" but falls back to the per-app suffixed
// "runos.<cid>.<appID>.yaml" when the directory already holds another
// app's manifest. Overwrites the resolved file silently; the caller is
// expected to have reconciled any drift (via `pull --force` or a prior
// `diff`).
func SaveYAML(parentDir, base string, app *PulledApp) (WriteResult, error) {
	appDir, err := ensureAppDir(parentDir, base)
	if err != nil {
		return WriteResult{}, err
	}
	leaf, err := YAMLFilename(appDir, app.CID, app.ID)
	if err != nil {
		return WriteResult{}, fmt.Errorf("resolve yaml filename: %w", err)
	}
	data, err := yaml.Marshal(app)
	if err != nil {
		return WriteResult{}, fmt.Errorf("failed to marshal config: %w", err)
	}
	return writeIfNeeded(filepath.Join(appDir, leaf), data, 0644)
}

// SaveSecretFile writes a single secret-file's decoded bytes into the
// per-app secret-files dir with 0600 perms. Creates the directory on
// demand with 0700 perms.
func SaveSecretFile(parentDir, base, filename string, content []byte) (WriteResult, error) {
	if err := ValidateSecretFilename(filename); err != nil {
		return WriteResult{}, err
	}
	if _, err := ensureAppDir(parentDir, base); err != nil {
		return WriteResult{}, err
	}
	dir := filepath.Join(AppDir(parentDir, base), SecretFilesDirname())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return WriteResult{}, fmt.Errorf("failed to create %s: %w", dir, err)
	}
	return writeIfNeeded(filepath.Join(dir, filename), content, 0600)
}

// SaveOverride writes a single override's decoded body into the per-app
// overrides dir with 0644 perms (overrides are plain manifest fragments,
// not secrets). Creates the directory on demand with 0755 perms.
func SaveOverride(parentDir, base, filename string, content []byte) (WriteResult, error) {
	if filename == "" {
		return WriteResult{}, fmt.Errorf("override filename is empty")
	}
	if strings.ContainsAny(filename, `/\`) {
		return WriteResult{}, fmt.Errorf("override filename contains a path separator: %q", filename)
	}
	if _, err := ensureAppDir(parentDir, base); err != nil {
		return WriteResult{}, err
	}
	dir := filepath.Join(AppDir(parentDir, base), OverridesDirname())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return WriteResult{}, fmt.Errorf("failed to create %s: %w", dir, err)
	}
	return writeIfNeeded(filepath.Join(dir, filename), content, 0644)
}

// defaultDockerignore is the canonical exclusion block written by
// EnsureDockerignore on first pull. The tarball walker hard-excludes the
// same set in deploy/tarball.go regardless of this file's presence, so
// a user who deletes .dockerignore does not lose the protection. The
// file exists for two reasons:
//   - discoverability: a human reading the repo can see why these files
//     don't end up in the image build context.
//   - external builders: docker build, kaniko, and buildkit run their
//     own dockerignore parser and won't honour the runos CLI's walker
//     exclusion.
const defaultDockerignore = `# RunOS-managed files. Excluded from the build context so per-cluster
# config and per-app secrets don't bleed into the image. The CLI's
# tarball walker hard-excludes these too, so removing this file doesn't
# expose the source archive, but external Docker builders (docker build,
# kaniko, buildkit) need it to honour the same exclusions.
runos.yaml
runos.*.yaml
runos.*.yml
.runos.*.env
.runos*.source-version
.secret-files/
overrides/
`

// EnsureDockerignore writes the canonical .dockerignore (defaultDockerignore)
// into parentDir if no .dockerignore exists there. When a .dockerignore is
// already present it is left untouched, even if its contents do not exclude
// every RunOS-managed file: the tarball walker is the security boundary, so
// this helper never fights with the user's own ignore list.
//
// Returns InSync=true when the file already existed (no write performed),
// false when a fresh file was written.
func EnsureDockerignore(parentDir string) (WriteResult, error) {
	path := filepath.Join(parentDir, ".dockerignore")
	if _, err := os.Lstat(path); err == nil {
		return WriteResult{Path: path, InSync: true}, nil
	} else if !os.IsNotExist(err) {
		return WriteResult{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return writeIfNeeded(path, []byte(defaultDockerignore), 0644)
}

// SaveSecretEnv writes the sensitive (Secret-backed) env file inside
// <parentDir>/<base>/ with 0600 perms. Mirrors deploy.DefaultSecretEnvFilename
// so a yaml written by pull works with `runos deploy` without translation.
func SaveSecretEnv(parentDir, base, cid, appID string, envVars map[string]string) (WriteResult, error) {
	appDir, err := ensureAppDir(parentDir, base)
	if err != nil {
		return WriteResult{}, err
	}
	return writeIfNeeded(filepath.Join(appDir, SecretEnvFilename(cid, appID)), RenderEnvBytes(envVars), 0600)
}

// SaveEnv writes the plain (ConfigMap-backed) env file inside
// <parentDir>/<base>/ with 0644 perms (this file is committed to VCS).
func SaveEnv(parentDir, base, cid, appID string, envVars map[string]string) (WriteResult, error) {
	appDir, err := ensureAppDir(parentDir, base)
	if err != nil {
		return WriteResult{}, err
	}
	return writeIfNeeded(filepath.Join(appDir, EnvFilename(cid, appID)), RenderEnvBytes(envVars), 0644)
}

// RenderEnvBytes produces the canonical on-disk representation of a set of
// environment variables: keys sorted alphabetically, KEY=VALUE per line,
// trailing newline when non-empty. Exposed so the diff command can compare
// local bytes against what SaveEnv would write without duplicating logic.
func RenderEnvBytes(envVars map[string]string) []byte {
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, envVars[k]))
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	return []byte(content)
}
