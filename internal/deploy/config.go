package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

	var config DeployConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
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

// HasLegacyFields reports whether the loaded config uses any of the
// deprecated top-level fields that have been superseded by
// servicePortMappings:
//   - port            (use servicePortMappings[].port)
//   - standardHttps   (use servicePortMappings[].standardHttps)
//   - domain          (use servicePortMappings[].domains[].fqdn)
//
// Used by the pre-deploy gate to surface a tailored "migrate via
// `runos apps pull --force`" message instead of the generic drift
// refusal — the LLM driving the deploy then knows to recommend the
// migration path rather than `--force`.
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
	if len(c.Domain) > 0 {
		return true
	}
	return false
}

// LoadEnvFile reads an env file at the given path and returns key-value pairs.
// Returns nil, nil if the path is empty or the file does not exist.
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

	envVars := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			value = strings.Trim(value, `"'`)
			envVars[key] = value
		}
	}

	return envVars, nil
}

// SaveEnvFile writes env vars to the given path.
func SaveEnvFile(path string, envVars map[string]string) error {
	if path == "" {
		return fmt.Errorf("env file path is required")
	}

	var lines []string
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, envVars[k]))
	}

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}

	return nil
}
