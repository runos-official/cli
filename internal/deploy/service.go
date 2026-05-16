// Package deploy handles application deployment including archive creation and upload.
package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Service handles deployment-related API calls
type Service struct {
	baseURL    string
	httpClient *http.Client
	token      string
	cid        string
	aid        string
}

// APIError is returned by the deploy service's GET methods when the
// conductor responds with a non-2xx status. Callers that need to
// branch on the status code (e.g. suppressing a 404 warning when the
// AppDocument hasn't been minted yet on first deploy) errors.As to
// reach the typed value. The Error() string format is identical to
// the historic plain-error shape so callers that only print get the
// same message.
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error renders the same one-liner the historic GET methods emitted.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, string(e.Body))
}

// PrepareResponse is the response from the prepare-cli-deployment endpoint.
//
// CliUploadID is the same identifier that GET /:aid/:cid/apps/:id/cli-archives
// uses to key archives, and is what `prepare-cli-pull` accepts. The CLI
// records it in the per-app sidecar (.runos-source-version) right after a
// successful upload, so future deploys can detect upstream archives that
// have appeared since this one. JobID is the orchestrating deploy job (used
// by --follow polling); the two ids are conceptually different even when
// they happen to share a value.
type PrepareResponse struct {
	JobID       string               `json:"jobId"`
	OSID        string               `json:"osid"`
	AppID       string               `json:"appId"`
	UploadURL   string               `json:"uploadUrl"`
	Token       string               `json:"token"`
	ExpiresAt   string               `json:"expiresAt"`
	CliUploadID string               `json:"cliUploadId,omitempty"`
	Services    []ProvisionedService `json:"services,omitempty"`
}

// ProvisionedService represents a service that will be provisioned for an app dependency
type ProvisionedService struct {
	Alias string `json:"alias"`
	ID    string `json:"id"`
	Type  string `json:"type"`
	IsNew bool   `json:"isNew"`
}

// NewService creates a new deployment service
func NewService(baseURL, token, cid, aid string) *Service {
	return &Service{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		token: token,
		cid:   cid,
		aid:   aid,
	}
}

// CID returns the cluster ID this Service is bound to.
func (s *Service) CID() string { return s.cid }

// AID returns the account ID this Service is bound to.
func (s *Service) AID() string { return s.aid }

// PrepareDeployment calls the prepare-cli-deployment endpoint to get an upload token
func (s *Service) PrepareDeployment(config *DeployConfig) (*PrepareResponse, error) {
	// Endpoint: /:aid/:cid/prepare-cli-deployment (camelCase alias still works)
	reqURL := fmt.Sprintf("%s/%s/%s/prepare-cli-deployment", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))

	jsonBody, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result PrepareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// UploadTarball uploads the gzipped tarball to the cluster agent
func (s *Service) UploadTarball(uploadURL string, uploadToken string, data io.Reader) error {
	// If uploadURL is relative, prepend the base URL
	fullURL := uploadURL
	if len(uploadURL) > 0 && uploadURL[0] == '/' {
		fullURL = s.baseURL + uploadURL
	}

	req, err := http.NewRequest(http.MethodPost, fullURL, data)
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}

	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+uploadToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to read upload response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// NetworkAccess represents a network access entry for an app
type NetworkAccess struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// Domain mirrors the `/:aid/domains` list-entry shape (manifest
// `domains/list`). Only the fields the domain-removal gate consumes
// are typed; anything else is left in the raw response unparsed.
type Domain struct {
	ID               string `json:"id"`
	Fqdn             string `json:"fqdn"`
	TargetIngressURL string `json:"targetIngressUrl"`
}

// GetAccountDomains fetches the account-wide custom-domain list. The
// endpoint isn't cluster- or app-scoped; callers filter by
// targetIngressUrl when they need a per-app view.
func (s *Service) GetAccountDomains() ([]Domain, error) {
	reqURL := fmt.Sprintf("%s/%s/domains", s.baseURL, url.PathEscape(s.aid))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	result, err := parseAccountDomainsResponse(body)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// parseAccountDomainsResponse handles both the legacy bare-array
// (`[{...}, ...]`) and the iter-27 envelope (`{domains: [...]}`) shapes
// of the `/:aid/domains` response. Pre-fix the deploy domain-removal
// gate hard-coded the bare-array branch and every deploy printed
// "Warning: domain-removal gate skipped (fetch failed: ...)" because
// conductor migrated to the envelope shape, silently disabling the
// confirmation. Pure helper so the regression test exercises both
// shapes plus the malformed-input case without spinning up a server.
// Issue 70.
func parseAccountDomainsResponse(body []byte) ([]Domain, error) {
	var direct []Domain
	if err := json.Unmarshal(body, &direct); err == nil {
		return direct, nil
	}
	var envelope struct {
		Domains []Domain `json:"domains"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return envelope.Domains, nil
}

// GetAppCustomDomains returns the deduplicated set of user-supplied
// custom-domain fqdns currently attached to the named app. Sources
// from the account-wide /:aid/domains endpoint and filters by
// targetIngressUrl matching the app's K8s service name `app-<id>`.
//
// Used by the pre-deploy domain-removal gate (I2-4e) to identify
// which server-side fqdns the next deploy would remove if the local
// yaml's `domain:` / `servicePortMappings[].domains` no longer
// references them. Empty list when the app has no custom domains
// attached.
//
// Regression history (TEST_LOG.md):
//   - I2-4e (round 3): originally sourced from /:aid/:cid/apps/:id/
//     network-access. Worked initially.
//   - I2-4e'' (round 5): tightened filter to drop IN_CLUSTER and
//     *.svc.cluster.local entries, since the original filter let K8s
//     internal service DNS through.
//   - I2-4e''' (round 6, this commit): switched source endpoint to
//     /:aid/domains. The network-access endpoint never includes
//     user custom domains (only RUNOS_PUBLIC_<port> + IN_CLUSTER_<port>),
//     so round-5's tightened filter went from "too many entries" to
//     "zero entries even on real removals." domains/list IS the
//     authoritative custom-domain register.
func (s *Service) GetAppCustomDomains(appID string) ([]string, error) {
	if appID == "" {
		return nil, nil
	}
	domains, err := s.GetAccountDomains()
	if err != nil {
		return nil, err
	}
	osid := "app-" + appID
	seen := make(map[string]struct{})
	var out []string
	for _, d := range domains {
		if d.Fqdn == "" {
			continue
		}
		if !targetIngressMatchesOSID(d.TargetIngressURL, osid) {
			continue
		}
		if _, ok := seen[d.Fqdn]; ok {
			continue
		}
		seen[d.Fqdn] = struct{}{}
		out = append(out, d.Fqdn)
	}
	return out, nil
}

// targetIngressMatchesOSID reports whether a domain's
// targetIngressUrl references the K8s service named `osid` (which is
// the conductor's `app-<id>` convention). Matches when osid appears
// as a delimited token, so `app-appid2` matches in
// `http://app-appid2.app-appid2.svc.cluster.local:3000` AND
// `https://app-appid2-3000.mycluster2.aid.dev.runos.xyz` but does NOT match
// in `app-hmx9oa-3000...` (different app whose id starts with the
// same prefix).
//
// Token boundaries are characters K8s / URL syntax allows next to a
// hostname segment: `.`, `-`, `:`, `/`, or end-of-string. Conductor's
// targetIngressUrl format is opaque to the CLI by design, so the
// match is structural rather than format-specific.
func targetIngressMatchesOSID(target, osid string) bool {
	if target == "" || osid == "" {
		return false
	}
	idx := 0
	for {
		hit := strings.Index(target[idx:], osid)
		if hit < 0 {
			return false
		}
		hit += idx
		end := hit + len(osid)
		// Right boundary: osid must end at a non-alphanumeric.
		if end < len(target) {
			c := target[end]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				idx = end
				continue
			}
		}
		// Left boundary: avoid `xapp-appid2` matching `app-appid2`.
		if hit > 0 {
			c := target[hit-1]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				idx = end
				continue
			}
		}
		return true
	}
}

// hostFromLink extracts the host portion of a URL (strips scheme and
// any path / query / port). Returns the input unchanged if URL parsing
// fails — the caller treats the result as an opaque fqdn either way.
func hostFromLink(link string) string {
	u, err := url.Parse(link)
	if err != nil || u == nil {
		return link
	}
	host := u.Hostname()
	if host == "" {
		return link
	}
	return host
}

// GetNetworkAccess fetches network access URLs for an application
func (s *Service) GetNetworkAccess(appID string) ([]NetworkAccess, error) {
	// Endpoint: /:aid/:cid/apps/:id/networkAccess
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/networkAccess", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result []NetworkAccess
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result, nil
}

// AppInfo represents an application from the apps list
type AppInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Port       int    `json:"port"`
	DeployType string `json:"deployType"`
}

// AppShow is the subset of GET /apps/:id the deploy command branches on. The
// real response is much larger; we decode only what runos deploy needs to
// pick between the cli- and vcs-deploy paths.
type AppShow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DeployType string `json:"deployType"`
	// Resource fields. These come back populated even when the local
	// yaml omits them, because the conductor's resolveRRC synthesises a
	// default class on first deploy. The post-deploy stamp-back uses
	// these to fill in absent local fields so the manifest is self-
	// describing without the user having to run apps_pull manually.
	ResourceRequirementClassID string `json:"resourceRequirementClassId,omitempty"`
	CPURequestMc               int    `json:"cpuRequestMc,omitempty"`
	CPULimitMc                 int    `json:"cpuLimitMc,omitempty"`
	MemoryRequestMb            int    `json:"memoryRequestMb,omitempty"`
	MemoryLimitMb              int    `json:"memoryLimitMb,omitempty"`
}

// VCSDeployResponse is what POST /apps/:id/deploy returns (202 + jobId).
type VCSDeployResponse struct {
	JobID string `json:"jobId"`
}

// GetApp fetches the app details for branching on deployType. Returns the
// raw HTTP error so callers can distinguish 404 from other failures.
func (s *Service) GetApp(appID string) (*AppShow, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: body}
	}

	var result AppShow
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// DeployVCS triggers a VCS deploy at the given commit sha. The conductor
// pulls source from the app's linked GitHub/GitLab integration, reconciles
// runos.yaml + runos.service.*.yaml from the committed tree, builds (when
// not already in Harbor), patches the deployment, and watches rollout.
//
// configPath is the repo-relative location of the runos.yaml the cluster
// agent should read on this deploy. Empty string means "use whatever the
// AppDocument has stored" (CI mode without a yaml on disk). Non-empty
// values are persisted to the AppDocument so subsequent deploys inherit
// them — the yaml is the source of truth for its own repo location.
func (s *Service) DeployVCS(appID, sha, configPath string) (*VCSDeployResponse, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/deploy", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	bodyMap := map[string]string{"sha": sha}
	if configPath != "" {
		bodyMap["configPath"] = configPath
	}
	jsonBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// I27-Z: return a typed *APIError so callers' --json error path
	// (cmd/apps_pull.go:emitJSONError) can flatten the conductor body
	// into the outer envelope instead of double-encoding it as a
	// quoted string inside `{error: "...API error (400): {\"error\":..."}`.
	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: body}
	}

	var result VCSDeployResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// FindAppByName searches for an app by name in the cluster
func (s *Service) FindAppByName(appName string) (*AppInfo, error) {
	// Endpoint: /:aid/:cid/apps
	reqURL := fmt.Sprintf("%s/%s/%s/apps", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	// I26-O / I26-U: conductor 16.0.0 wrapped /apps in an `{apps: [...]}`
	// envelope. Accept both the new envelope and the legacy bare array
	// during the rollout window.
	body = unwrapArrayEnvelopeDeploy(body)
	var apps []AppInfo
	if err := json.Unmarshal(body, &apps); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	for _, app := range apps {
		if app.Name == appName {
			return &app, nil
		}
	}

	return nil, nil // Not found
}

// unwrapArrayEnvelopeDeploy mirrors apps.unmarshalListResponse / the
// output-package and dynacmd-package unwrap helpers. Returns the inner
// array bytes when `data` is a single-key object whose value is an
// array; otherwise returns `data` unchanged. Duplicated locally so the
// internal/deploy package doesn't import internal/apps just for this.
func unwrapArrayEnvelopeDeploy(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return data
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return data
	}
	if len(probe) != 1 {
		return data
	}
	for _, inner := range probe {
		innerTrim := bytes.TrimSpace(inner)
		if len(innerTrim) > 0 && innerTrim[0] == '[' {
			return inner
		}
	}
	return data
}

// AppDependency represents a dependency of an app
type AppDependency struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// fetchEnvVarMap is the shared implementation for GET /env-vars and
// /secret-env-vars: an authenticated GET that decodes the response into a
// flat string map.
//
// I26-O: conductor 16.0.0 wrapped the env-vars responses in an
// `{envVars: {...}}` envelope. The CLI accepts both shapes so the
// `runos deploy` pre-sync env merge survives the transition window
// where older clusters still emit the bare map. Mirrors
// `apps.parseEnvVarsResponse`. Duplicating the parser here (rather
// than importing the apps package) keeps the internal/deploy ↔
// internal/apps dependency boundary one-way.
func (s *Service) fetchEnvVarMap(reqURL string) (map[string]string, error) {
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: body}
	}

	return parseEnvVarsResponse(body)
}

// parseEnvVarsResponse mirrors apps.parseEnvVarsResponse: accepts the
// new `{envVars: {...}}` envelope (conductor 16.0.0+) and the legacy
// bare `{KEY: value, ...}` map. See the apps-package copy for the full
// rationale and detection rule. Regression target: I26-O.
func parseEnvVarsResponse(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var envelope struct {
		EnvVars map[string]string `json:"envVars"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil {
		var probe map[string]json.RawMessage
		if perr := json.Unmarshal(data, &probe); perr == nil {
			if _, hasEnvelope := probe["envVars"]; hasEnvelope && len(probe) == 1 {
				if envelope.EnvVars == nil {
					return map[string]string{}, nil
				}
				return envelope.EnvVars, nil
			}
		}
	}
	var bare map[string]string
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, fmt.Errorf("failed to parse env-vars response: %w", err)
	}
	return bare, nil
}

// GetAppSecretEnvVars fetches the sensitive (Secret-backed) env vars for an
// application. Endpoint: /:aid/:cid/apps/:id/secret-env-vars
func (s *Service) GetAppSecretEnvVars(appID string) (map[string]string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/secret-env-vars", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	return s.fetchEnvVarMap(reqURL)
}

// GetAppEnvVars fetches the plain (ConfigMap-backed) env vars for an
// application. Endpoint: /:aid/:cid/apps/:id/env-vars
func (s *Service) GetAppEnvVars(appID string) (map[string]string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/env-vars", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	return s.fetchEnvVarMap(reqURL)
}

// GetAppDependencies fetches dependencies for an application
func (s *Service) GetAppDependencies(appID string) ([]AppDependency, error) {
	// Endpoint: /:aid/:cid/apps/:id/dependencies
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/dependencies", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: body}
	}

	// I27-AG: conductor 17.7.0's envelope-everywhere migration wrapped
	// `apps/:id/dependencies` in `{dependencies: [...]}` (sibling of the
	// I27-AA / I27-T list-endpoint sweep). Pre-fix the decoder rejected
	// every response with `cannot unmarshal object into Go value of type
	// []deploy.AppDependency`, taking the laptop-deploy dependency-check
	// path offline. Reuse the existing shape-keyed unwrapper so both the
	// legacy bare-array shape and the new envelope round-trip cleanly
	// during the migration window.
	body = unwrapArrayEnvelopeDeploy(body)

	var deps []AppDependency
	if err := json.Unmarshal(body, &deps); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return deps, nil
}
