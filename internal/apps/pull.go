package apps

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/envfile"
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
	SecretEnv string `yaml:"secretEnv,omitempty"`
	// Env: relative path to the file holding plain (ConfigMap-backed) env
	// vars committed to VCS. Default `runos.{cid}.{id}.config.env`.
	Env string `yaml:"env,omitempty"`
	// SourceDir is the path (relative to the yaml's directory) to the
	// build context. Empty defaults to "." (the yaml's own directory).
	// May start with ".." when the yaml lives deeper than the source
	// tree (monorepo apps). Used by both deploy types: CLI's tarball
	// walker reads it at deploy time; the cluster agent reads it from
	// the committed yaml at the SHA on VCS deploys. Round-trips through
	// the AppDocument for `apps_pull` so fresh checkouts get a complete
	// yaml.
	SourceDir string `yaml:"sourceDir,omitempty"`
	// Dockerfile is the path (relative to SourceDir) to the Dockerfile.
	// Empty defaults to "Dockerfile". Same round-trip semantics as
	// SourceDir.
	Dockerfile string `yaml:"dockerfile,omitempty"`
	// ConfigPath is the repo-relative path where THIS yaml lives in the
	// source repo (e.g. `apps/api/infra/runos-prod.yaml` in a monorepo).
	// Stored on the AppDocument by `apps_add` and persisted by every
	// VCS deploy that sends a non-empty value. Round-tripping it through
	// `apps_pull` lets a fresh `git clone && runos apps pull` produce a
	// pulled yaml that carries the same canonical metadata as the
	// committed yaml. I27-M/N: pre-fix the pulled file omitted this so
	// the user had no breadcrumb to where the canonical yaml lives in
	// their monorepo.
	//
	// Note: deploy auto-derives configPath from the yaml's actual
	// filesystem position relative to `git rev-parse --show-toplevel`
	// when this field is empty (see cmd/deploy_vcs.go:resolveVcsConfigPath).
	// When the yaml IS at the canonical path on disk, the round-trip is
	// a no-op (auto-derive == stored). When the user has vendored the
	// yaml under a different path, the explicit field wins so deploy
	// still targets the committed source location.
	ConfigPath string `yaml:"configPath,omitempty"`
	// Replicas is omitted from the on-disk yaml when the app sits on a
	// named RRC (e.g. `app.sl1.beff`), because the class already bakes
	// the replica count in. Only populated by BuildPulledApp for
	// `custom`-rrc apps (where every resource field must be explicit)
	// and legacy apps with no RRC set. Storing the field as an int with
	// omitempty (rather than *int) keeps the literal `Replicas: 1` test
	// initializers throughout the package working unchanged.
	Replicas int `yaml:"replicas,omitempty"`

	ClusterDomainID string `yaml:"clusterDomainId,omitempty"`

	// Resource fields follow the rrcId rule: named class → only
	// ResourceRequirementClassID is populated and the yaml stays thin
	// (cpu/memory/replicas all omitted). `custom` rrcId → every field is
	// populated and emitted as a fat yaml. Empty rrcId (legacy apps with
	// no class set) emits cpu/memory but not rrcId.
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

	// DeploymentStrategy mirrors DeployConfig.DeploymentStrategy so a
	// pulled yaml round-trips the rollout preset alongside the other
	// AppSpec fields. Empty when the server has nothing set (i.e. the
	// app runs on the conductor's default `rolling`); omitempty keeps
	// the field absent from the pulled yaml in that case.
	DeploymentStrategy string `yaml:"deploymentStrategy,omitempty"`

	// BuildArgs mirrors DeployConfig.BuildArgs for the apps_pull
	// round-trip. Populated from the apps/:id response when conductor
	// surfaces a non-empty `buildArgs` map; empty otherwise (omitempty
	// drops the field from the pulled yaml). Forward-compatible: if the
	// server response does not yet include the key, the projection
	// leaves the field nil and apps_pull continues to behave as today.
	BuildArgs map[string]string `yaml:"buildArgs,omitempty"`

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

// MergeUserEnvPaths preserves the local yaml's authored `secretEnv` /
// `env` path fields. These point at on-disk files (which the CLI reads
// to send env content to the server, and writes when content comes
// back) and are CLI-side bookkeeping only; the server has no notion of
// where the file lives. Without this merge, a re-pull would overwrite
// `secretEnv: .secret.env` with the canonical `.runos.<cid>.<id>.env`
// that BuildServerStateForDiff stamps when env content exists, leaving
// the user with permanent yaml drift on every `apps_diff` (I3-B).
//
// No-op when either argument is nil. An empty local field leaves the
// canonical default in place (fresh pulls and apps that have never
// customised the path keep the canonical name).
func MergeUserEnvPaths(target, local *PulledApp) {
	if target == nil || local == nil {
		return
	}
	if local.SecretEnv != "" {
		target.SecretEnv = local.SecretEnv
	}
	if local.Env != "" {
		target.Env = local.Env
	}
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

	// Build-metadata round-trip fields (V13). The server stores these only
	// when explicitly set via apps_add / apps_update; missing means the
	// user is on the defaults (".", "Dockerfile") and the local yaml stays
	// uncluttered via omitempty.
	p.SourceDir = stringOr(raw, "sourceDir")
	p.Dockerfile = stringOr(raw, "dockerfile")
	// I27-M/N: configPath round-trips on VCS apps so a fresh
	// `git clone && runos apps pull` produces a yaml that carries every
	// build-metadata field needed for a subsequent VCS deploy from a
	// non-canonical path. Conductor sets it from apps_add and persists
	// it on each VCS deploy. Empty on CLI-deploy apps (omitempty drops
	// the field for them).
	p.ConfigPath = stringOr(raw, "configPath")

	// Resources: thin-yaml on named RRC, fat-yaml on custom.
	//
	// Named RRC (e.g. `app.sl1.beff`): the class bakes in every resource
	// dimension (replicas, cpu, memory), so the pulled yaml carries ONLY
	// the class id. cpu/memory pointers stay nil and Replicas stays 0;
	// both omit via omitempty. Re-deploying this thin yaml round-trips
	// to the same resolved state because the conductor re-applies the
	// class on its side.
	//
	// "custom" rrcId is the server-synthesised label for "user supplied
	// a class AND overrode at least one of replicas/cpu/memory with a
	// value that disagrees with the class defaults" (see conductor's
	// resolveRRC). In that branch the yaml must carry every dimension
	// explicitly because there is no class to fall back to. Legacy apps
	// with no rrcId set fall into the same fat branch so the user sees
	// the actual server-side values.
	rrc := stringOr(raw, "resourceRequirementClassId")
	if rrc != "" && rrc != "custom" {
		p.ResourceRequirementClassID = rrc
	} else {
		if rrc == "custom" {
			p.ResourceRequirementClassID = "custom"
		}
		if n, ok := asInt(raw["replicas"]); ok {
			p.Replicas = n
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

	// Health check: copy flat fields independently. Each is omitted via
	// omitempty when zero. The preset (`healthCheck`), port, and path are
	// independent omit-equals-clear desired-state fields server-side, so
	// any subset can be set; pulling must reflect whatever the server has.
	if preset := stringOr(raw, "healthCheck"); preset != "" {
		p.HealthCheck = preset
	}
	if n, ok := asInt(raw["healthCheckPort"]); ok && n > 0 {
		p.HealthCheckPort = &n
	}
	if path := stringOr(raw, "healthCheckPath"); path != "" {
		p.HealthCheckPath = path
	}

	// Metrics: emitted only when a scrape port is set.
	if port, ok := asInt(raw["metricsPort"]); ok && port > 0 {
		p.MetricsPort = &port
		p.MetricsPath = stringOr(raw, "metricsPath")
	}

	// DeploymentStrategy is preserve-on-omit server-side (defaults to
	// `rolling` when the AppDocument has no value), so an empty string
	// from the server means "use the default" and we leave the field
	// off the pulled yaml. Any non-empty value is surfaced verbatim.
	if strat := stringOr(raw, "deploymentStrategy"); strat != "" {
		p.DeploymentStrategy = strat
	}

	// BuildArgs projects the server's `buildArgs` map (when present) into
	// the pulled yaml so the declarative source-of-truth round-trips
	// through `apps pull` -> edit -> `runos deploy`. Forward-compatible:
	// missing key or non-map value leaves p.BuildArgs nil, which
	// omitempty drops from the pulled yaml.
	if rawBA, ok := raw["buildArgs"].(map[string]any); ok && len(rawBA) > 0 {
		out := make(map[string]string, len(rawBA))
		for k, v := range rawBA {
			switch tv := v.(type) {
			case string:
				out[k] = tv
			case bool:
				if tv {
					out[k] = "true"
				} else {
					out[k] = "false"
				}
			case float64:
				out[k] = trimFloat(tv)
			case int:
				out[k] = fmt.Sprintf("%d", tv)
			case int64:
				out[k] = fmt.Sprintf("%d", tv)
			}
		}
		if len(out) > 0 {
			p.BuildArgs = out
		}
	}

	return p
}

// trimFloat formats a JSON-decoded numeric build-arg value as the
// minimal string a user would have written by hand. Whole numbers lose
// the trailing ".0" (`%g` handles this naturally); fractional values
// keep their full precision. Used by BuildPulledApp so a server
// response carrying `buildArgs: {PORT: 443}` round-trips to `PORT:
// "443"` rather than `PORT: "443.000000"`.
func trimFloat(v float64) string {
	return fmt.Sprintf("%g", v)
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

// ConfigPathAction is the apps_pull dispatch decision when the server's
// stored `configPath` is compared against the path the local yaml was
// just written to. Three outcomes:
//
//   - ConfigPathActionSkip: do nothing (non-VCS app, paths match, server
//     has no value yet, or caller couldn't compute a repo-relative path).
//   - ConfigPathActionUpdate: PATCH the server's configPath to the local
//     path so the next VCS deploy reads the right yaml. The default
//     for a VCS app with diverging paths.
//   - ConfigPathActionWarn: emit the existing stderr warning instead of
//     mutating server state. Used when --no-configpath-update is set,
//     or as the fallback when the PATCH itself errors.
type ConfigPathAction int

// ConfigPathAction values.
const (
	ConfigPathActionSkip ConfigPathAction = iota
	ConfigPathActionUpdate
	ConfigPathActionWarn
)

// RepoRelPath returns the slash-form of absPath relative to repoRoot, or
// "" if absPath escapes the root, equals the root, or either input is
// empty. Pure: no git, no filesystem. Extracted from cmd/apps_pull.go's
// vcsRepoRelPath (V17) so the path math is testable without spinning up
// a git fixture.
func RepoRelPath(repoRoot, absPath string) string {
	if repoRoot == "" || absPath == "" {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return ""
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// RelocateSourceDir computes a sourceDir value that, paired with
// newConfigPath, preserves the absolute repo target the existing
// (oldConfigPath, storedSourceDir) pair resolved to. Returns "" when
// no recompute is needed (no relocation, sourceDir is empty or ".",
// the naive merge of storedSourceDir against newConfigPath stays
// inside the repo) or when recovery is impossible (existing pair
// already escapes the repo).
//
// V19: pre-fix, the V14/V17 auto-update hook PATCHed configPath alone;
// conductor's V13/V16 lexical containment validator merged with stored
// sourceDir and rejected when the merge escaped repo root (canonical
// case: relocation from infra/runos/apps/ to a shallower dir like tmp/,
// where sourceDir's `..` traversal counted from too few levels deep).
// The CLI now recomputes sourceDir client-side and PATCHes both fields
// in a single call. Same recompute also feeds serverState before the
// local yaml write, so the on-disk yaml and the server stay in sync at
// the new layout.
//
// All inputs and outputs are repo-relative slash-form paths. Pure: no
// I/O, no git resolution, no filesystem access.
func RelocateSourceDir(oldConfigPath, newConfigPath, storedSourceDir string) string {
	if oldConfigPath == "" || newConfigPath == "" {
		return ""
	}
	if storedSourceDir == "" || storedSourceDir == "." {
		// Empty / default sourceDir means "build context = configPath
		// dir"; that semantic moves with configPath naturally and the
		// V13/V16 validator never rejects it.
		return ""
	}
	oldDir := path.Dir(oldConfigPath)
	newDir := path.Dir(newConfigPath)
	if oldDir == newDir {
		return ""
	}
	// Naive merge: would conductor's containment validator accept the
	// existing storedSourceDir against the new configPath dir? If yes,
	// no recompute needed; the validator passes the partial PATCH.
	naive := path.Clean(path.Join(newDir, storedSourceDir))
	if naive != ".." && !strings.HasPrefix(naive, "../") {
		return ""
	}
	// Pin the absolute repo target the existing pair points at, then
	// re-express it relative to newDir.
	absTarget := path.Clean(path.Join(oldDir, storedSourceDir))
	if absTarget == ".." || strings.HasPrefix(absTarget, "../") {
		// Existing pair already escapes — legacy bad state. The caller
		// falls back to V18's surface-the-error path.
		return ""
	}
	newSourceDir, err := filepath.Rel(newDir, absTarget)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(newSourceDir)
}

// DecideConfigPathAction maps the (serverConfigPath, localRepoRelPath,
// deployType, noUpdate) tuple to one of the three ConfigPathAction
// outcomes. Pure function so the dispatch contract is testable without
// HTTP mocks; the caller (cmd/apps_pull.go:pullOne) does the network
// call only when this returns Update.
//
// V14 / long-term V2 closure: the iteration-2 fix shipped a stderr-only
// warning when paths diverged. The MCP wrapper dropped the warning, and
// even direct-CLI users had to copy-paste the corrective `apps update
// --configPath` command. The auto-update path moves that work into pull
// itself; the Warn outcome stays as the fallback (PATCH failure or
// explicit opt-out).
func DecideConfigPathAction(serverConfigPath, localRepoRelPath, deployType string, noUpdate bool) ConfigPathAction {
	if deployType != "vcs" {
		return ConfigPathActionSkip
	}
	if serverConfigPath == "" || localRepoRelPath == "" {
		return ConfigPathActionSkip
	}
	if serverConfigPath == localRepoRelPath {
		return ConfigPathActionSkip
	}
	if noUpdate {
		return ConfigPathActionWarn
	}
	return ConfigPathActionUpdate
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
//     Typically a fresh, pre-deploy runos.yaml that was never pulled.
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
		// Service yamls (`runos.service.<cid>.<type>.<sid>.yaml`) live
		// next to the app yaml when class-shorthand provisioning runs.
		// They are not app manifests and would otherwise be flagged as
		// candidates here, leading apps_diff/sync auto-detect to refuse
		// with `multiple yaml candidates`. Service yamls go through the
		// dedicated `runos services diff/sync/pull` verbs.
		if strings.HasPrefix(lower, "runos.service.") {
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
//
// Single-app target dirs only. Callers that share a target dir across
// concurrent pulls (cmd/apps_pull.go's `id-flat` mode) must use
// SaveYAMLSuffixed instead to avoid the V1 race for the canonical
// runos.yaml slot.
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

// SaveYAMLSuffixed marshals a PulledApp and writes it to the per-app
// suffixed leaf (`runos.<cid>.<appID>.yaml`) inside <parentDir>/<base>/,
// unconditionally. The deterministic-name shape eliminates the V1 race
// where multiple concurrent apps_pull invocations into a shared target
// directory all see the dir empty during YAMLFilename's resolve step,
// pick the canonical "runos.yaml" leaf, and clobber each other on the
// subsequent writes.
//
// Used by cmd/apps_pull.go's `id-flat` mode (--app-id + --out, the
// LLM/CI fan-out shape). Single-app modes (yaml-positional, id-subdir,
// bulk) keep using SaveYAML so the canonical "runos.yaml" name is
// preserved when there's no race possible.
func SaveYAMLSuffixed(parentDir, base string, app *PulledApp) (WriteResult, error) {
	appDir, err := ensureAppDir(parentDir, base)
	if err != nil {
		return WriteResult{}, err
	}
	leaf := SuffixedYAMLFilename(app.CID, app.ID)
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
runos.yaml.bak
runos.yaml.backup
runos.*.yaml
runos.*.yaml.bak
runos.*.yaml.backup
runos.*.yml
runos.*.yml.bak
runos.*.yml.backup
# Plain ConfigMap-backed env file. Non-sensitive but per-cluster, no
# reason to bake into the image when the platform injects it via the
# ConfigMap volume mount.
runos.*.config.env
.runos.*.env
.runos*.source-version
.secret-files/
overrides/

# Dotfile env (every Node/Python project has these).
.env
.env.*

# VCS metadata.
.git/
.gitignore

# Editor backups.
*.bak
*.backup
*~
.*.swp

# Defensive: directories that conventionally contain credentials.
secrets/
**/secrets/
**/.aws/
**/.ssh/

# Common language ignores. Helpful for Dockerfile-from-scratch builds
# where nothing else excludes them.
node_modules/
__pycache__/
.pytest_cache/
target/
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

// SaveSecretEnvAtPath writes the sensitive env file at the resolved
// path with 0600 perms. Caller is responsible for resolving the path
// (typically by reading the yaml's `secretEnv:` field, falling back
// to SecretEnvFilename + appDir when empty).
//
// I4-L: pre-fix, apps_pull always wrote env content to the canonical
// `.runos.<cid>.<id>.env` leaf via SaveSecretEnv, even when the local
// yaml declared `secretEnv: .secret.env`. Result: two env files
// after pull (the user's authoritative one referenced by `secretEnv:`
// stayed at its prior content, the canonical-named twin held the
// fresh server state, and edits to the canonical name silently
// dropped at the next deploy because the deploy reads the
// yaml-referenced path). The companion MergeUserEnvPaths (I3-B)
// preserved the path field in the yaml; this writer makes the
// content side honour the same field.
func SaveSecretEnvAtPath(path string, envVars map[string]string) (WriteResult, error) {
	if path == "" {
		return WriteResult{}, fmt.Errorf("secret env path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return WriteResult{}, fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}
	return writeIfNeeded(path, RenderEnvBytes(envVars), 0600)
}

// SaveEnvAtPath writes the plain env file at the resolved path with
// 0644 perms. Plain env is committed to VCS; the perms reflect that.
// Same I4-L motivation as SaveSecretEnvAtPath.
func SaveEnvAtPath(path string, envVars map[string]string) (WriteResult, error) {
	if path == "" {
		return WriteResult{}, fmt.Errorf("env path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return WriteResult{}, fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}
	return writeIfNeeded(path, RenderEnvBytes(envVars), 0644)
}

// ResolveLocalEnvPath returns the leaf and full filesystem path the
// caller should use for an env file, given the yaml's authored value
// and a canonical fallback. When authored is non-empty it wins; when
// authored is empty, canonical is used. Relative leaves are joined
// against appDir; absolute leaves are returned verbatim. Mirrors the
// resolution `BuildDiffReport` uses for the same env-section paths so
// pull's reads, writes, and diffs all converge on the same file.
func ResolveLocalEnvPath(appDir, authored, canonical string) (leaf, fullPath string) {
	leaf = authored
	if leaf == "" {
		leaf = canonical
	}
	fullPath = leaf
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(appDir, fullPath)
	}
	return
}

// RenderEnvBytes produces the canonical on-disk representation of a set
// of environment variables. Delegates to internal/envfile.Format so the
// produced bytes are lossless under round-trip (newlines, leading and
// trailing whitespace, and quote characters in values are preserved
// across pull -> edit -> sync). Keys are emitted in sorted order for
// stable diffs. Exposed so the diff command can compare local bytes
// against what SaveEnv would write without duplicating logic. Issue 73.
func RenderEnvBytes(envVars map[string]string) []byte {
	return envfile.Format(envVars)
}
