package apps

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

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
	// secretEnv / env path fields are CLI-side bookkeeping; preserve
	// the local yaml's authored paths so a user who renamed the file
	// (e.g. `secretEnv: .secret.env`) doesn't fight permanent drift
	// against the canonical default (I3-B).
	MergeUserEnvPaths(serverState, localApp)
	alignOmittedPortDefaults(serverState, localApp)

	yamlDir := filepath.Dir(yamlPath)
	yamlDiff, err := ComputeYAMLDiff(yamlPath, serverState)
	if err != nil {
		return nil, fmt.Errorf("yaml diff: %w", err)
	}
	// I5-F: drop round-trip-only fields (sourceDir, dockerfile) from
	// the yaml diff so pure local-tooling state doesn't trip the
	// exit-2 contract. The push paths already ignore these fields;
	// the diff path must match.
	yamlDiff = FilterRoundTripFromYAMLDiff(yamlDiff)

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
	// I5-F: strip platform-injected names (values in
	// requires.<alias>.env) from BOTH local and server before
	// comparison. The iter-3 R1 fix dropped them from the server
	// side only (FilterPlatformInjectedEnv), which closed the
	// "local missing → drift" case but left the asymmetric
	// "local has, server-filtered doesn't → drift" gap. The
	// conductor's push-side `stripOrphanKeysFromCustom` (iter-4 R2)
	// removes the same names from anything the CLI sends, so any
	// drift involving only these names is unactionable. Now both
	// sides are filtered for the comparison.
	platformInjected := requiresOwnedNames(serverState)
	hasSecretContent := len(secretEnvVars) > 0 || fileExists(secretPath)
	if hasSecretContent {
		secretEnvDiff, err = ComputeEnvDiffFiltered(secretPath, secretEnvVars, platformInjected)
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
