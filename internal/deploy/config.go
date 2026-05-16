package deploy

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/envfile"
	"gopkg.in/yaml.v3"
)

// StringOrSlice handles YAML fields that can be either a single string or an array of strings
type StringOrSlice []string

// UnmarshalYAML implements the yaml.Unmarshaler interface, accepting either a single string or an array of strings.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// Single string value
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		// Array of strings
		var items []string
		if err := value.Decode(&items); err != nil {
			return err
		}
		*s = items
		return nil
	default:
		return fmt.Errorf("domain must be a string or array of strings")
	}
}

// MarshalYAML implements the yaml.Marshaler interface, encoding a single-element slice as a scalar string.
func (s StringOrSlice) MarshalYAML() (any, error) { //nolint:ireturn
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

// MarshalJSON implements the json.Marshaler interface, always encoding as a JSON array of strings.
func (s StringOrSlice) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(s))
}

// MappingDomain mirrors the conductor's MappingDomain. One FQDN attached to
// a port mapping, with optional Cloudflare proxy ("orange cloud"). The object
// form is intentional so future per-domain knobs (path matching, requestCert
// overrides, etc.) can be added without another schema change.
type MappingDomain struct {
	Fqdn string `yaml:"fqdn" json:"fqdn"`
	// EnableCloudflareProxy routes traffic via Cloudflare's edge instead of
	// resolving directly to the cluster. Requires a Cloudflare DNS
	// integration covering the zone to take effect; without one, the flag is
	// stored but inert until the integration is added.
	EnableCloudflareProxy bool `yaml:"enableCloudflareProxy,omitempty" json:"enableCloudflareProxy,omitempty"`
}

// ServicePortMapping mirrors the conductor's canonical ServicePortMapping.
// Sent verbatim to the prepare-cli-deployment endpoint.
//
// Domains belong on the mapping they route to: each FQDN listed under
// `domains` is created as a standalone domain record whose target is this
// mapping's port. Multi-port apps can therefore say "this domain → port 3000,
// that domain → port 9090" unambiguously.
type ServicePortMapping struct {
	Port          int             `yaml:"port" json:"port"`
	StandardHttps *bool           `yaml:"standardHttps,omitempty" json:"standardHttps,omitempty"`
	Domains       []MappingDomain `yaml:"domains,omitempty" json:"domains,omitempty"`
}

// DeployConfig represents the runos.yaml configuration file.
//
// Field order intentionally mirrors apps.PulledApp's marshal order for
// the fields they share, so a yaml written by deploy round-trips
// byte-clean against a yaml rendered by `apps pull` / the diff engine.
// The two structs are not identical (DeployConfig has CLI-only fields
// like Dockerfile/Domain/Requires that PulledApp doesn't track) but
// the AppSpec block in the middle aligns one-for-one.
//
// Three logical groups:
//
//  1. AppSpec block (matches PulledApp): App, DeployType, ID, CID, AID,
//     Env, Replicas, ClusterDomainID, ResourceRequirementClassID,
//     CPU/Memory overrides, ServicePortMappings, Metrics*, HealthCheck*.
//     The conductor accepts these verbatim on prepare-cli-deployment
//     and PATCH /apps/:id.
//
//  2. CLI-only block: Domain, Dockerfile, Requires, CustomEnvVars,
//     StorageMb. Used by the CLI (and the upload path) but not part of
//     PulledApp's projection of server state.
//
//  3. Legacy shorthand: Port, StandardHttps, sugar for a single
//     ServicePortMappings entry. Normalized server-side. Kept for
//     backwards compatibility with older runos.yaml files.
//
// All fields use omitempty so minimal configs and pulled yamls both
// round-trip cleanly.
type DeployConfig struct {
	// --- AppSpec block (PulledApp-aligned) ---
	App                        string               `yaml:"app" json:"app"`
	DeployType                 string               `yaml:"deployType,omitempty" json:"deployType,omitempty"`
	ID                         string               `yaml:"id,omitempty" json:"id,omitempty"`
	CID                        string               `yaml:"cid,omitempty" json:"cid,omitempty"`
	AID                        string               `yaml:"aid,omitempty" json:"aid,omitempty"`
	// SecretEnv points at the local file holding sensitive (Secret-backed)
	// env vars. Default `.runos.{cid}.{id}.env`. The leading dot keeps it
	// gitignored by default; the file must NEVER be committed to VCS.
	SecretEnv                  string               `yaml:"secretEnv,omitempty" json:"secretEnv,omitempty"`
	// Env points at the local file holding plain (ConfigMap-backed) env vars.
	// Default `runos.{cid}.{id}.config.env`. No leading dot — this file IS
	// committed to VCS.
	Env                        string               `yaml:"env,omitempty" json:"env,omitempty"`
	// SourceDir is the build-context path (relative to this yaml's
	// directory) that `runos deploy` tarballs and uploads. Empty defaults
	// to "." (the yaml's own directory). Set to ".." when the yaml lives
	// in a per-app subdirectory and the source code lives at the parent
	// (project root). For VCS apps the cluster agent now reads this from
	// the committed yaml at deploy time too, so monorepo VCS apps in a
	// subdirectory work with the same `sourceDir: ..` shape.
	SourceDir                  string               `yaml:"sourceDir,omitempty" json:"-"`
	// ConfigPath is the repo-relative path of THIS yaml file. Optional —
	// when omitted, `runos deploy` for VCS apps auto-derives it from the
	// yaml's location relative to the git repo root, so the user normally
	// doesn't have to set it. Set explicitly only when auto-derivation is
	// wrong (e.g. running outside the repo, deploying a vendored copy).
	// Sent to conductor on every VCS deploy and persisted to the
	// AppDocument so subsequent CI deploys (without a checkout) reuse it.
	ConfigPath                 string               `yaml:"configPath,omitempty" json:"-"`
	Replicas                   *int                 `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	ClusterDomainID            string               `yaml:"clusterDomainId,omitempty" json:"clusterDomainId,omitempty"`
	ResourceRequirementClassID string               `yaml:"resourceRequirementClassId,omitempty" json:"resourceRequirementClassId,omitempty"`
	CPURequestMc               int                  `yaml:"cpuRequestMc,omitempty" json:"cpuRequestMc,omitempty"`
	CPULimitMc                 int                  `yaml:"cpuLimitMc,omitempty" json:"cpuLimitMc,omitempty"`
	MemoryRequestMb            int                  `yaml:"memoryRequestMb,omitempty" json:"memoryRequestMb,omitempty"`
	MemoryLimitMb              int                  `yaml:"memoryLimitMb,omitempty" json:"memoryLimitMb,omitempty"`
	ServicePortMappings        []ServicePortMapping `yaml:"servicePortMappings,omitempty" json:"servicePortMappings,omitempty"`
	HealthCheck                string               `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
	HealthCheckPort            *int                 `yaml:"healthCheckPort,omitempty" json:"healthCheckPort,omitempty"`
	HealthCheckPath            string               `yaml:"healthCheckPath,omitempty" json:"healthCheckPath,omitempty"`
	MetricsPort                *int                 `yaml:"metricsPort,omitempty" json:"metricsPort,omitempty"`
	MetricsPath                string               `yaml:"metricsPath,omitempty" json:"metricsPath,omitempty"`

	// --- CLI-only / not-in-PulledApp ---
	// Domain is the legacy top-level domain field. New yamls put FQDNs
	// under `servicePortMappings[].domains` so each domain binds to a
	// specific port. The conductor still accepts top-level `domain` and
	// folds it into the first mapping's `domains` for backwards compat.
	Domain        StringOrSlice                 `yaml:"domain,omitempty" json:"domain,omitempty"`
	Dockerfile    string                        `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	StorageMb     *int                          `yaml:"storageMb,omitempty" json:"storageMb,omitempty"`
	Requires      map[string]ServiceRequirement `yaml:"requires,omitempty" json:"requires,omitempty"`
	// SecretFiles is the round-tripped declaration block from a pulled
	// yaml AND the wire-side payload sent to prepare-cli-deployment.
	// Each entry is `{filename, mountPath, local, md5?}` on disk; the
	// actual file bytes live at `local`. Before marshalling for the
	// wire, `LoadSecretFileContents` reads each `local` path, base64-
	// encodes the bytes, and writes them into the entry's `Content`
	// field. The wire shape is `{filename, mountPath, content}`; the
	// `local` / `md5` fields stay on the yaml side only (json:"-").
	//
	// Conductor R2 wires the receive + process side end-to-end via a
	// new "Apply user secret files" orchestration step (update-only;
	// first-deploy users still need apps_secret-files_update because
	// the K8s namespace doesn't exist yet at orchestration slot 2.5).
	// Pre-fix (I10-K CLI half) this field was both absent from the
	// struct AND suppressed on the wire (`json:"-"`); the post-deploy
	// SaveConfig stripped user-authored entries silently.
	SecretFiles   []SecretFile                  `yaml:"secretFiles,omitempty" json:"secretFiles,omitempty"`
	// CustomSecretEnvVars holds the parsed contents of SecretEnv (sensitive,
	// Secret-backed). Sent in the prepare-cli-deployment body and lands in
	// the {osid}-user-secret-env-vars Secret on the cluster.
	CustomSecretEnvVars map[string]string       `yaml:"-" json:"customSecretEnvVars,omitempty"`
	// CustomEnvVars holds the parsed contents of Env (plain, ConfigMap-backed,
	// VCS-committed). Lands in the {osid}-user-env-vars ConfigMap.
	CustomEnvVars       map[string]string       `yaml:"-" json:"customEnvVars,omitempty"`

	// --- Legacy shorthand (normalized server-side into ServicePortMappings) ---
	Port          int   `yaml:"port,omitempty" json:"port,omitempty"`
	StandardHttps *bool `yaml:"standardHttps,omitempty" json:"standardHttps,omitempty"`

	// --- PulledApp pass-through (parsed-and-ignored by deploy) ---
	// A yaml produced by `runos apps pull` carries an `integration:` block
	// for VCS apps and an `overrides:` list when kubectl manifest
	// overrides are configured. `runos deploy` itself doesn't act on
	// either — the linked integration is resolved server-side at
	// deploy time, and overrides are managed via `apps sync`. They're
	// declared here only so KnownFields(true) yaml decoding (issue 50)
	// doesn't reject a freshly pulled yaml. Both are suppressed from
	// the wire body (`json:"-"`); the values stay client-side.
	Integration *PulledIntegration `yaml:"integration,omitempty" json:"-"`
	Overrides   []PulledOverride   `yaml:"overrides,omitempty" json:"-"`
}

// PulledIntegration mirrors apps.Integration as a yaml-only pass-through
// inside the deploy package (deploy can't import apps without a cycle).
// Used solely to let `runos deploy` parse a pulled yaml without
// rejecting the `integration:` block under KnownFields(true). The fields
// match apps.Integration so a pulled yaml round-trips byte-clean.
type PulledIntegration struct {
	ID         string `yaml:"id,omitempty"`
	RepoID     int64  `yaml:"repoId,omitempty"`
	RepoName   string `yaml:"repoName,omitempty"`
	BranchName string `yaml:"branchName,omitempty"`
}

// PulledOverride mirrors apps.Override for the same yaml-only
// pass-through reason as PulledIntegration. Kept local to deploy
// because the deploy verb never inspects the contents — overrides are
// managed through `runos apps sync` instead.
type PulledOverride struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name,omitempty"`
	Enabled bool   `yaml:"enabled"`
	Local   string `yaml:"local"`
	MD5     string `yaml:"md5,omitempty"`
}

// SecretFile is the on-disk declaration of one secret file mounted into
// the app container. Lives in the local yaml's `secretFiles:` list,
// alongside the actual file bytes referenced by `local`.
//
// Two shapes share the same struct:
//   - **yaml** (on disk): `filename`, `mountPath`, `local`, `md5?`.
//     `Content` stays empty; `Local` points at the bytes.
//   - **wire** (sent to prepare-cli-deployment): `filename`, `mountPath`,
//     `content` (base64). `Local` + `md5` are suppressed from JSON.
//     `LoadSecretFileContents` populates `Content` from `Local` right
//     before PrepareDeployment marshals.
//
// Conductor R2 ships an orchestration step "Apply user secret files"
// (update-only, slot 2.5) that consumes the wire shape and creates K8s
// Secrets via `createSecretFile`; the existing buildSecretVolumes step
// picks them up on the next rollout. Regression target: I10-K CLI half.
//
// Field shape mirrors apps.SecretFile on the yaml side; duplicated
// rather than imported to avoid an apps → deploy import cycle.
type SecretFile struct {
	Filename  string `yaml:"filename" json:"filename"`
	MountPath string `yaml:"mountPath" json:"mountPath"`
	Local     string `yaml:"local" json:"-"`
	MD5       string `yaml:"md5,omitempty" json:"-"`
	// Content holds the base64-encoded file bytes for the wire body.
	// Empty on disk (`yaml:"-"`) so it never round-trips through the
	// local yaml; populated by LoadSecretFileContents at deploy time.
	Content string `yaml:"-" json:"content,omitempty"`
}

// ServiceRequirement defines a dependent service (e.g., PostgreSQL, Valkey)
type ServiceRequirement struct {
	ID     string            `yaml:"id,omitempty" json:"id,omitempty"`
	Type   string            `yaml:"type" json:"type"`
	Class  string            `yaml:"class,omitempty" json:"class,omitempty"`
	Config map[string]any    `yaml:"config,omitempty" json:"config,omitempty"`
	Env    map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// LoadConfig reads and parses a runos.yaml config file
func LoadConfig(path string) (*DeployConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("runos.yaml not found at %s", path)
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// Issue 91: yaml.v3 silently truncates float literals to int when
	// the target field is int-typed (e.g. `cpuRequestMc: 0.5` decodes
	// to int(0)), and k8s treats limit=0 as UNLIMITED — the user
	// thinks they capped at 0.5mc/0.5MB; the pod actually has no
	// limits. Pre-decode the yaml as a generic map and refuse the
	// fractional case before the typed decode silently rounds it.
	if err := refuseFractionalResourceFields(data); err != nil {
		return nil, err
	}

	var config DeployConfig
	// KnownFields(true) makes the decoder fail with a typed error when
	// the yaml carries a key that isn't on DeployConfig. Pre-fix
	// (yaml.Unmarshal) silently dropped typos like `replica` (vs
	// `replicas`), `healtCheck`, `envVars`, so the user saw a deploy
	// exit 0 and a server that ignored half their config. The strict
	// decoder names the offending field in the error so the user can
	// fix the typo immediately. Issue 50.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&config); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("runos.yaml at %s is empty", path)
		}
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

// Validate checks that required fields are present.
//
// A port specification is required, supplied either via the legacy scalar
// `port` field or via the canonical `servicePortMappings` array. The two are
// mutually compatible: when both are set, the conductor prefers the canonical
// form. Each port must fall in 1-65535.
func (c *DeployConfig) Validate() error {
	if c.App == "" {
		return fmt.Errorf("app name is required in runos.yaml")
	}

	// Issue 88: HTTP probe paths must be a single line beginning with /.
	// yaml block-scalar (`|`) and folded-scalar (`>`) forms otherwise let
	// a user stuff newlines into the value, which k8s probe semantics
	// don't define. Same shape applies to the prometheus metricsPath.
	// Checked before the port branching because both port styles share
	// these optional fields.
	if err := validateHTTPPath("healthCheckPath", c.HealthCheckPath); err != nil {
		return err
	}
	if err := validateHTTPPath("metricsPath", c.MetricsPath); err != nil {
		return err
	}

	hasMappings := len(c.ServicePortMappings) > 0
	if !hasMappings {
		if c.Port <= 0 || c.Port > 65535 {
			return fmt.Errorf("valid port (1-65535) is required in runos.yaml (or set servicePortMappings)")
		}
		return nil
	}

	// Canonical mappings supplied, validate each entry.
	for i, m := range c.ServicePortMappings {
		if m.Port <= 0 || m.Port > 65535 {
			return fmt.Errorf("servicePortMappings[%d].port must be 1-65535", i)
		}
	}
	return nil
}

// resourceIntegerFields lists the top-level yaml keys whose value must
// be a non-negative integer literal (no float syntax). yaml.v3 silently
// coerces `0.5` to int(0) when the target struct field is int-typed,
// which on k8s means "no limit" rather than "half a millicore". The
// validator below refuses the float-literal case before the typed
// decode runs.
var resourceIntegerFields = []string{
	"cpuRequestMc",
	"cpuLimitMc",
	"memoryRequestMb",
	"memoryLimitMb",
	"replicas",
	"storageMb",
	"healthCheckPort",
	"metricsPort",
	"port",
}

// refuseFractionalResourceFields decodes the yaml into a generic
// map[string]any and inspects the resource-integer fields. If any are
// present with a non-integer literal (float), it refuses with a clear
// error naming the field. Issue 91.
func refuseFractionalResourceFields(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Will be caught by the strict decode downstream; don't double-
		// report parse errors here.
		return nil
	}
	for _, field := range resourceIntegerFields {
		v, ok := raw[field]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case int, int64, uint, uint64:
			// Already an integer literal, fine.
		case float32, float64:
			return fmt.Errorf("%s must be an integer, got fractional value %v (k8s resource limit of 0 means UNLIMITED; check that you wrote a whole number)", field, n)
		default:
			// String, bool, etc. — the typed decode will fail with a
			// better message than we can construct here.
		}
	}
	return nil
}

// validateHTTPPath refuses values that aren't a single-line HTTP path
// beginning with `/`. Empty values pass through (the field is optional).
// Pure helper so the regression test exercises each rejection mode
// without building a full DeployConfig.
func validateHTTPPath(field, value string) error {
	if value == "" {
		return nil
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%s contains control byte 0x%02x at position %d (must be a single-line HTTP path starting with /)", field, c, i)
		}
	}
	if value[0] != '/' {
		return fmt.Errorf("%s %q must begin with /", field, value)
	}
	for i := 0; i < len(value); i++ {
		if value[i] == ' ' || value[i] == '\t' {
			return fmt.Errorf("%s %q must not contain whitespace", field, value)
		}
	}
	return nil
}

// maxSecretFileBytes is the per-file size cap before base64 encoding.
// 100KiB is the conductor's hard limit on `apps_secret-files_update.add`
// entries; mirror it client-side so a too-large local file is caught
// here rather than via a server 400 after the marshal.
const maxSecretFileBytes = 100 * 1024

// LoadSecretFileContents reads each entry's `local` file path (resolved
// relative to configDir for relative paths), reads up to
// maxSecretFileBytes, base64-encodes the bytes, and writes the result
// to the entry's `Content` field. Called before PrepareDeployment
// marshals the config so the wire body carries `{filename, mountPath,
// content}` (the conductor's expected shape). Local + md5 stay
// suppressed from JSON.
//
// A nil receiver or empty SecretFiles slice is a no-op. Individual
// entries with an empty Local are skipped (no Content populated; the
// server will refuse the entry, but the rest of the deploy proceeds).
// Returns the first I/O error encountered; the caller bubbles it as
// "failed to load secret file" so the deploy aborts cleanly.
//
// Regression target: I10-K CLI half (the wire-side flip).
func (c *DeployConfig) LoadSecretFileContents(configDir string) error {
	if c == nil || len(c.SecretFiles) == 0 {
		return nil
	}
	for i := range c.SecretFiles {
		entry := &c.SecretFiles[i]
		if entry.Local == "" {
			// No file path to read; leave Content empty so the server
			// sees the entry as malformed (per validateSecretFilesShape)
			// and refuses with a clear message rather than us guessing
			// here.
			continue
		}
		localPath := entry.Local
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(configDir, localPath)
		}
		f, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("read secret file %q for entry %q: %w", entry.Local, entry.Filename, err)
		}
		// Cap at maxSecretFileBytes+1 so we can detect overflow with a
		// single read without buffering the whole file in memory twice.
		data, err := io.ReadAll(io.LimitReader(f, maxSecretFileBytes+1))
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("read secret file %q for entry %q: %w", entry.Local, entry.Filename, err)
		}
		if len(data) > maxSecretFileBytes {
			return fmt.Errorf("secret file %q exceeds the %d-byte cap (got at least %d bytes)", entry.Local, maxSecretFileBytes, len(data))
		}
		entry.Content = base64.StdEncoding.EncodeToString(data)
		// I10-Q: refresh the md5 sidecar so the local yaml mirrors the
		// just-pushed bytes. Without this, the yaml's `md5:` field keeps
		// whatever stale digest was written by the last `apps pull` (or
		// nothing on a first-deploy yaml authored by hand), and
		// `apps diff` reports spurious drift on the next read. Computed
		// from the raw bytes (not base64) so it matches the server-side
		// digest that conductor stores in the Secret annotation.
		digest := md5.Sum(data)
		entry.MD5 = hex.EncodeToString(digest[:])
	}
	return nil
}

// SaveConfig writes the config back to the file
func SaveConfig(path string, config *DeployConfig) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// ValidateAID checks that the config AID matches the session AID
func ValidateAID(configAID, sessionAID string) error {
	if configAID == "" {
		return nil // No AID in config, allow
	}
	if configAID == sessionAID {
		return nil // AIDs match, allow
	}
	return fmt.Errorf("config file AID (%s) does not match session AID (%s). Ensure you're logged into the correct account", configAID, sessionAID)
}

// ReconcileCID resolves a single cluster id from the two possible sources
// during a deploy: the flag-or-config value the user supplied (callerCID)
// and the cid: field embedded in the loaded yaml (yamlCID). Both empty
// returns ("", nil) so the caller can decide whether the resulting absence
// is acceptable (e.g. apps_diff allows empty when neither is set; the
// deploy verbs require non-empty post-reconcile). Mismatch when both are
// set is the cross-cluster-push guard — pre-fix, runos deploy silently
// used the caller's cid and ignored the yaml, so a stale --cid plus a
// directory-per-app yaml could push to the wrong cluster. apps_diff
// (cmd/apps.go:bindToYAML) already has the equivalent check; this lifts
// it into a shared helper so the deploy verbs can adopt it without
// duplicating the message. Regression target: I18-B.
func ReconcileCID(callerCID, yamlCID string) (string, error) {
	switch {
	case yamlCID == "":
		return callerCID, nil
	case callerCID == "":
		return yamlCID, nil
	case callerCID != yamlCID:
		return "", fmt.Errorf("cluster mismatch: yaml is for cluster %q but --cid (or default) is %q, refusing to deploy to a different cluster than expected", yamlCID, callerCID)
	default:
		return callerCID, nil
	}
}

// ResolvedEnvFiles holds the resolved absolute paths of the two env-var
// files an app deploy may pull from. Either path may be empty when the
// config has no `id` yet (first deploy of a new app), in which case the
// CLI deploy proceeds with no env vars from that source.
type ResolvedEnvFiles struct {
	Secret string
	Plain  string
}

// ResolveEnvFiles determines both env-file paths based on config state.
//
// Priority for each side:
//  1. Explicit `secretEnv` / `env` field in runos.yaml, used as-is.
//  2. Default filenames keyed off `cid` + `id`:
//     - secret: `.runos.{cid}.{id}.env` (gitignored by default)
//     - plain:  `runos.{cid}.{id}.config.env` (committed to VCS)
//
// Returns the resolved absolute paths and whether the config was modified
// (needs saving). The config is mutated to record the auto-derived
// filenames so subsequent deploys round-trip cleanly.
//
// The cluster-scoped legacy form (.runos.{cid}.env, no app id) used to be
// a third fallback for the secret side, but it's app-agnostic: two apps
// in the same cluster sharing one directory would silently pick up each
// other's env vars. Removed when multi-yaml support landed.
// WarnLegacyEnv surfaces the rename hint for any user still on that
// layout.
func ResolveEnvFiles(configDir string, config *DeployConfig, cid string) (ResolvedEnvFiles, bool) {
	var paths ResolvedEnvFiles
	modified := false

	// Secret side (sensitive, gitignored).
	if config.SecretEnv != "" {
		paths.Secret = filepath.Join(configDir, config.SecretEnv)
	} else if config.ID != "" {
		filename := fmt.Sprintf(".runos.%s.%s.env", cid, config.ID)
		config.SecretEnv = filename
		paths.Secret = filepath.Join(configDir, filename)
		modified = true
	}

	// Plain side (committed, no leading dot).
	if config.Env != "" {
		paths.Plain = filepath.Join(configDir, config.Env)
	} else if config.ID != "" {
		filename := fmt.Sprintf("runos.%s.%s.config.env", cid, config.ID)
		config.Env = filename
		paths.Plain = filepath.Join(configDir, filename)
		modified = true
	}

	return paths, modified
}

// LegacyEnvFilename returns the cluster-scoped, app-agnostic env filename
// (.runos.{CID}.env) that older CLI versions used as a fallback when the
// yaml didn't carry an explicit env path. Exposed so callers can detect
// stragglers and nudge users to migrate; never written to.
func LegacyEnvFilename(cid string) string {
	return fmt.Sprintf(".runos.%s.env", cid)
}

// ResolveArchiveRoot returns the absolute directory `runos deploy` should
// tarball, given the yaml's directory and the optional sourceDir field.
// sourceDir is interpreted relative to configDir so the value stays
// portable across machines (a yaml committed to git keeps working after
// clone). Empty sourceDir means configDir itself, the historic default.
//
// Rejects:
//   - absolute sourceDir (would lock the yaml to one user's filesystem).
//   - sourceDir resolving to a non-existent path or a non-directory.
//
// Does NOT reject `..` — escaping the yaml's directory is the whole
// point of the field for the directory-per-app shape (yaml in
// runos.<cid>.<id>/, source at the parent project root).
func ResolveArchiveRoot(configDir, sourceDir string) (string, error) {
	trimmed := strings.TrimSpace(sourceDir)
	if trimmed == "" {
		trimmed = "."
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("sourceDir must be relative (got absolute path %q); relative paths keep the yaml portable across machines", trimmed)
	}
	resolved := filepath.Clean(filepath.Join(configDir, trimmed))
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("sourceDir %q (resolved to %q) does not exist", trimmed, resolved)
		}
		return "", fmt.Errorf("stat sourceDir %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sourceDir %q (resolved to %q) is not a directory", trimmed, resolved)
	}
	return resolved, nil
}

// ResolveDockerfilePath returns the absolute path to the Dockerfile
// inside an already-resolved archive root, validating that the file
// exists before the upload starts. The yaml's `dockerfile:` field is
// relative to `sourceDir` (mirroring Docker's own convention: the
// Dockerfile path is relative to the build context). Empty defaults
// to "Dockerfile" at the archive root.
//
// Rejects:
//   - absolute dockerfile path (would lock the yaml to one machine).
//   - paths that escape the archive root (`../`, etc.) — the resolved
//     path must stay within archiveRoot or buildctl on the build
//     server can't find it in the uploaded tarball.
//   - dockerfile resolving to a non-existent path or a non-regular file.
//
// I27-Y pre-flight: pre-fix, a misconfigured `dockerfile:` field (typo,
// or wrong-relative path) silently uploaded the tarball, and the build
// server failed late with `failed to read dockerfile: open <name>: no
// such file or directory` after the user had already burned the
// archive-upload round-trip. Validating at this layer surfaces the
// mistake before any network call. Symmetric in shape with
// ResolveArchiveRoot.
func ResolveDockerfilePath(archiveRoot, dockerfile string) (string, error) {
	trimmed := strings.TrimSpace(dockerfile)
	if trimmed == "" {
		trimmed = "Dockerfile"
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("dockerfile must be relative to sourceDir (got absolute path %q); relative paths keep the yaml portable across machines", trimmed)
	}
	resolved := filepath.Clean(filepath.Join(archiveRoot, trimmed))
	cleanRoot := filepath.Clean(archiveRoot)
	rel, err := filepath.Rel(cleanRoot, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("dockerfile %q escapes the build context (resolved to %q, outside %q); set sourceDir so the Dockerfile lives inside it", trimmed, resolved, cleanRoot)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("dockerfile %q (resolved to %q) does not exist; check the path is relative to sourceDir (=%q)", trimmed, resolved, cleanRoot)
		}
		return "", fmt.Errorf("stat dockerfile %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("dockerfile %q (resolved to %q) is not a regular file", trimmed, resolved)
	}
	return resolved, nil
}

// nginxRootBaseImagePattern matches Dockerfile `FROM nginx:<tag>`
// lines that pull the upstream image (which binds port 80 and requires
// root). The unprivileged variants (`nginxinc/nginx-unprivileged`,
// `nginx/nginx-unprivileged`) are explicitly excluded — they're the
// recommended drop-in for RunOS clusters where containers can't run
// as root. Case-insensitive, allows comments after the FROM line,
// tolerates `--platform=` flags. I27-G regression target.
var nginxRootBaseImagePattern = regexp.MustCompile(`(?im)^\s*FROM(?:\s+--\S+)*\s+(nginx)(?::[^\s]+)?(?:\s|$)`)

// NginxDockerfileHint scans a Dockerfile and returns a one-line stderr
// hint when the base image is a bare `nginx:<tag>` (which can't bind
// port 80 as a non-root user — every RunOS cluster rejects root
// containers, so the pod CrashLoopBackOffs at startup). The hint names
// the `nginxinc/nginx-unprivileged` drop-in (binds 8080 as non-root).
// Empty return value when the Dockerfile is unreadable, uses a
// different base image, or already uses the unprivileged variant.
//
// Non-blocking: deploy proceeds either way. Some users patch the
// upstream image themselves (`USER`, `RUN sed -i ...port-80...`), so
// the false-positive cost of a refusal would be high. A stderr nag
// is the right tradeoff.
//
// I27-G regression target: docs (`applications.md`) already cover
// the constraint in the topic surface; this hint surfaces the same
// guidance at deploy time so users who didn't read the doc still get
// the breadcrumb before the cluster rejects the pod.
func NginxDockerfileHint(dockerfilePath string) string {
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return ""
	}
	matches := nginxRootBaseImagePattern.FindAllSubmatch(data, -1)
	for _, m := range matches {
		// m[0] is the full match, m[1] is the captured `nginx` token.
		// Confirm the captured token starts a base image that isn't
		// the unprivileged variant. (The regex only matches `nginx:...`
		// directly, not `nginxinc/...`, so any match is a root variant.)
		if len(m) >= 2 && string(m[1]) == "nginx" {
			return "Hint: this Dockerfile uses the upstream `nginx:` image. " +
				"RunOS clusters reject containers running as root, and `nginx:` binds port 80 (requires root). " +
				"Static sites crash with CrashLoopBackOff at startup. " +
				"Use `nginxinc/nginx-unprivileged:<tag>` instead (listens on port 8080 as non-root, drop-in for static content)."
		}
	}
	return ""
}

// WarnLegacyEnv prints a one-line stderr hint when configDir contains a
// pre-multi-yaml env file (.runos.{CID}.env, no app id) and the loaded
// yaml doesn't pin an explicit secret-env path. Non-blocking: deploy
// proceeds either way. The check is best-effort, stat errors are swallowed.
func WarnLegacyEnv(configDir string, config *DeployConfig, cid string) {
	if config.SecretEnv != "" {
		return
	}
	legacy := filepath.Join(configDir, LegacyEnvFilename(cid))
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	target := ".runos." + cid + ".<id>.env"
	if config.ID != "" {
		target = fmt.Sprintf(".runos.%s.%s.env", cid, config.ID)
	}
	fmt.Fprintf(os.Stderr,
		"Warning: %s is the pre-multi-yaml env layout and is no longer auto-loaded. "+
			"Rename it to %s, or set 'secretEnv: <path>' in runos.yaml.\n",
		LegacyEnvFilename(cid), target,
	)
}

// DefaultSecretEnvFilename returns the default sensitive env filename for a
// given cluster and app ID. Leading dot keeps it gitignored by default.
func DefaultSecretEnvFilename(cid, appID string) string {
	return fmt.Sprintf(".runos.%s.%s.env", cid, appID)
}

// DefaultEnvFilename returns the default plain env filename for a given
// cluster and app ID. No leading dot — this file IS committed to VCS.
func DefaultEnvFilename(cid, appID string) string {
	return fmt.Sprintf("runos.%s.%s.config.env", cid, appID)
}

// LocalDomainFqdns returns the deduplicated set of fqdns the local
// yaml declares, drawing from both the legacy top-level `domain:` slice
// AND each `servicePortMappings[].domains[].fqdn`. Used by the pre-
// deploy domain-removal gate (I2-4e) to compare against the server's
// per-app custom-domain list and surface any fqdn that the next deploy
// would silently remove.
//
// Returned slice is in declaration order with duplicates collapsed:
// a user who lists the same fqdn at top-level AND under a port mapping
// gets a single entry. Empty input → empty output.
func LocalDomainFqdns(c *DeployConfig) []string {
	if c == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, fqdn := range c.Domain {
		add(fqdn)
	}
	for _, mapping := range c.ServicePortMappings {
		for _, d := range mapping.Domains {
			add(d.Fqdn)
		}
	}
	return out
}

// HasLegacyFields reports whether the loaded config uses any of the
// top-level shorthand fields that conflict with servicePortMappings on
// the server side:
//   - port            (duplicates servicePortMappings[].port)
//   - standardHttps   (duplicates servicePortMappings[].standardHttps)
//
// Top-level `domain:` is intentionally NOT flagged here: it remains the
// documented form in the `domain` topic and the conductor folds it into
// the first mapping's `domains` cleanly. Calling it deprecated when
// every doc example uses it is a signal-quality regression for the LLM
// driving the deploy (and a UX papercut for humans).
//
// Used by the pre-deploy gate to surface a tailored "migrate via
// `runos apps pull --force`" message instead of the generic drift
// refusal, but only for the truly conflicting shorthands.
func HasLegacyFields(c *DeployConfig) bool {
	if c == nil {
		return false
	}
	if c.Port > 0 {
		return true
	}
	if c.StandardHttps != nil {
		return true
	}
	return false
}

// LoadEnvFile reads an env file at the given path and returns key-value
// pairs. Returns nil, nil if the path is empty or the file does not
// exist. Uses the lossless dotenv parser in internal/envfile so values
// with newlines, leading/trailing whitespace, or quote characters
// round-trip cleanly. Issue 73.
func LoadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", filepath.Base(path), err)
	}

	return envfile.Parse(data), nil
}

// SaveEnvFile writes env vars to the given path using the lossless
// dotenv format from internal/envfile.
func SaveEnvFile(path string, envVars map[string]string) error {
	if path == "" {
		return fmt.Errorf("env file path is required")
	}

	if err := os.WriteFile(path, envfile.Format(envVars), 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}

	return nil
}
