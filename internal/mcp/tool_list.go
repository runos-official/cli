package mcp

import (
	"strings"

	"github.com/runos-official/cli/internal/dynacmd"
)

// contains checks if a string slice contains a specific item
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// cidDescription is the one sentence every cluster-scoped tool carries.
func cidDescription(hasDefault bool) string {
	if hasDefault {
		return "Cluster id. Omit to use the configured default cluster."
	}
	return "Cluster id."
}

func (s *Server) buildTools() []Tool {
	var tools []Tool

	seen := make(map[string]struct{})
	for _, cmd := range s.manifest.Commands {
		toolName := manifestToolName(cmd.Command)
		if _, exists := seen[toolName]; exists || shadowsReservedTool(cmd.Command, toolName) {
			continue
		}
		seen[toolName] = struct{}{}

		// Static shims own their names and category even when a manifest
		// entry exists for the corresponding CLI command.
		if !commandInTier(cmd, s.category) || staticToolGroup(toolName) != nil ||
			isManifestUpdateTool(toolName) || toolName == toolsEnableToolName {
			continue
		}

		// Managed-service tools for a type this account does not run are
		// left out. runos_tools_enable adds one back mid-session.
		if !s.toolsets.permits(cmd.Command) {
			continue
		}

		// Skip deploy - it has a built-in handler
		if toolName == "deploy" {
			// Add deploy tool with custom description and cid parameter
			tools = append(tools, Tool{
				Name: "deploy",
				// Builds and rolls out to the cluster, so not read-only.
				Annotations: &ToolAnnotations{},
				Description: `Deploy an application. The CLI dispatches on the app's deployType:

  - CLI deploy (deployType: cli): tarballs the local source and uploads to /prepare-cli-deployment. The classic flow.
  - VCS deploy (deployType: vcs): no tarball. Conductor pulls source from the linked GitHub/GitLab integration at a specific commit sha, builds in-cluster, pushes, patches, waits for rollout. Trigger by passing sha (and optionally app to skip the local yaml load).

WHEN TO USE WHICH SHAPE:
  - CLI-deploy app, from a checkout: pass yaml_file (or rely on the single-runos*.yaml auto-detect). Do NOT pass sha/app/allow_dirty (rejected for CLI-deploy apps).
  - VCS-deploy app, from a checkout: pass yaml_file. sha defaults to git rev-parse HEAD; pass sha to deploy a specific commit. allow_dirty waives the dirty-tree refusal.
  - VCS-deploy app, no checkout (CI mode, or fast "deploy by id" from a laptop): pass app=<5-char id> and sha=<commit>. Skips the yaml load entirely.

DIRTY-TREE GATE (VCS only): when sha is auto-resolved from HEAD and the working tree has uncommitted changes, deploy refuses. Pass allow_dirty=true to proceed. The build uses the COMMITTED source at HEAD, NOT the dirty edits. Recommend committing first unless the user has explicitly accepted the gap.

BEFORE CALLING (CLI-deploy only): check for large files (>1MB) or build artifacts that should be in .dockerignore to avoid slow uploads. Requires a Dockerfile that runs as non-root.

MULTI-YAML PROJECTS: a project directory can hold multiple runos.<cid>.<id>.yaml files (e.g. staging + prod sharing one source tree). When more than one runos*.yaml exists, you MUST pass yaml_file explicitly so the right manifest is deployed. Monorepos with VCS apps under per-app subdirs use configPath (set on apps_add) so the cluster agent reads the per-app yaml from the committed tree.

Pre-deploy drift gate (CLI-deploy only): when the yaml has id/cid/aid set, deploy first compares local against the running app on the server. If anything has changed on the server that isn't reflected locally, the deploy refuses with the diff in the error so the user can decide whether to overwrite. Pass force=true to bypass the gate and overwrite server state with the local version. Fresh yamls (no id) skip the gate entirely.

LEGACY YAML MIGRATION (CLI-deploy only): when the deploy refusal output contains "deprecated field names" or "deprecated fields (port:/domain:/standardHttps:)", the user's yaml uses the legacy schema (top-level port:/standardHttps:/domain: instead of servicePortMappings:[{port,standardHttps,domains:[{fqdn,enableCloudflareProxy}]}]). DO NOT recommend force=true in this case, forcing keeps the user on the legacy shape and the same drift returns on every deploy. Instead, recommend they run "runos apps pull <yaml-path> --force" (which rewrites the local file from the canonical server state), then re-call this deploy tool. Migration is one-time per yaml.

VCS deploys with an unchanged sha are near-instant: the orchestration short-circuits the build when the image is already in Harbor and only runs manifest reconcile + rollout watch. Use this to apply config drift after editing the yaml without a rebuild: edit yaml, commit, push, deploy with the new sha.

DOCKER BUILD ARGS (both deployTypes): pass one or more KEY=VALUE entries via the build_arg array to set Docker build args on the image build (most acutely Next.js bundles baking NEXT_PUBLIC_* values). Build args also accept a declarative source in runos.yaml under "buildArgs:" (committed, per-environment). Precedence: --build-arg > runos.yaml buildArgs. Conductor merges and forwards to BuildKit server-side; the CLI does NOT merge. Duplicate keys within build_arg are rejected with a clear error before any API call. Keys must match the Docker ARG name regex (^[A-Za-z_][A-Za-z0-9_]*$). Undeclared ARGs in the Dockerfile are ignored (no-op, safe). Values are plaintext; do not put secrets here.`,
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]Property{
						"cid": {
							Type:        "string",
							Description: "Cluster id to deploy to. Required when the CLI has no default cluster.",
						},
						"yaml_file": {
							Type:        "string",
							Description: "Path to the runos*.yaml manifest to deploy. Optional when only one runos*.yaml is present in the project directory. REQUIRED when the directory holds multiple manifests (multi-cluster projects), otherwise the wrong app may be deployed. The CLI subprocess loads this via the --config flag. For VCS apps, the local yaml is consulted only for the app id; the committed yaml at <sha> is the source of truth for the build.",
						},
						"app": {
							Type:        "string",
							Description: "VCS-deploy only. App ID (5-char identifier) to deploy when running without a runos.yaml on disk (CI mode, or fast laptop deploys by id). Cannot be combined with yaml_file. Rejected for CLI-deploy apps.",
						},
						"sha": {
							Type:        "string",
							Description: "VCS-deploy only. Commit SHA (7-40 hex chars) to deploy. Defaults to `git rev-parse HEAD` when omitted and a checkout is present. Rejected for CLI-deploy apps. The cluster agent reads the committed runos.yaml at this sha for the build.",
						},
						"allow_dirty": {
							Type:        "boolean",
							Description: "VCS-deploy only. Waive the dirty-tree refusal when sha is auto-resolved from HEAD. The build uses the committed source at HEAD, NOT uncommitted edits. Recommend committing first unless the user has explicitly accepted the gap.",
							Default:     false,
						},
						"force": {
							Type:        "boolean",
							Description: "CLI-deploy only. Bypass the pre-deploy drift gate and overwrite server state with the local version. Only set this after the user has reviewed the drift and explicitly chosen to proceed. DO NOT set this when the gate output mentions deprecated/legacy fields, recommend `apps_pull <yaml> force=true` to migrate first.",
							Default:     false,
						},
						"build_arg": {
							Type:        "array",
							Description: "Docker build args as KEY=VALUE strings (repeatable, applies to both deployTypes). Merged server-side with runos.yaml `buildArgs:`; --build-arg wins. Duplicate keys rejected. Keys must match the Docker ARG name regex (^[A-Za-z_][A-Za-z0-9_]*$). Values are plaintext, do not pass secrets here.",
							Items:       &Property{Type: "string"},
						},
					},
				},
			})
			continue
		}

		tool := Tool{
			Name:        toolName,
			Description: cmd.Description,
			InputSchema: InputSchema{
				Type:       "object",
				Properties: make(map[string]Property),
			},
			Annotations: annotationsFor(cmd),
		}

		// Add cid parameter if endpoint requires it. It is REQUIRED when
		// the CLI has no default cluster to fall back on: saying optional
		// there made the call fail at dispatch with "cluster ID required"
		// for a value the schema said could be left out (B13).
		if strings.Contains(cmd.Endpoint, ":cid") {
			tool.InputSchema.Properties["cid"] = Property{
				Type: "string",
				// One short sentence. Repeated on every cluster-scoped tool (259
				// of 315 on the read server); the 240-character version it
				// replaces was 26% of the whole server. Whether cid may be
				// omitted is stated STRUCTURALLY by `required` below, and
				// "use clusters_list" lives in the bootstrap.
				Description: cidDescription(s.defaultClusterID != ""),
			}
			if s.defaultClusterID == "" {
				tool.InputSchema.Required = requireOnce(tool.InputSchema.Required, "cid")
			}
		}

		// Destructive verbs take an explicit confirm, mirroring the
		// CLI's --yes. Pre-fix the CLI refused a delete that no human
		// could confirm while MCP dispatched the same delete on the
		// first ask, so the safer surface was the one a person drives
		// and the unguarded one was the LLM's (A14).
		if rule, _, ok := dynacmd.ConditionalReadOnly(cmd); ok && dynacmd.IsDestructiveCommand(cmd) {
			// FCR 1909: read-only unless one field holds a listed value.
			// confirm stays in the schema for that case, but not required.
			tool.InputSchema.Properties[confirmArgName] = Property{
				Type:        "boolean",
				Description: conditionalConfirmText(rule),
			}
			tool.Description = withConditionalConfirmNotice(cmd.Description, rule)
		} else if dynacmd.IsDestructiveCommand(cmd) {
			tool.InputSchema.Properties[confirmArgName] = Property{
				Type:        "boolean",
				Description: "Must be true. This verb is destructive and cannot be undone: set confirm=true only after the user has agreed to this exact target. The CLI asks a human the same question through --yes.",
			}
			tool.InputSchema.Required = requireOnce(tool.InputSchema.Required, confirmArgName)
			// The description is what an LLM reads while it decides what to
			// call. Saying it only in the property description cost a
			// round trip on every destructive verb (review 2 item 6).
			tool.Description = withConfirmNotice(cmd.Description)
		}

		if cmd.Input != nil {
			for _, field := range cmd.Input.Fields {
				prop := Property{
					Type:        s.mapType(field.Type),
					Description: field.Description,
				}
				if len(field.Enum) > 0 {
					prop.Enum = field.Enum
				}
				if field.Default != nil {
					prop.Default = field.Default
				}
				// JSON Schema requires `additionalProperties` (for object) and
				// `items` (for array) so clients know what to put inside the
				// container. The manifest now carries optional `valueType` +
				// `valueFields` for object fields (parallel to `itemType` +
				// `itemFields` for arrays); when present, projectObjectValue
				// emits the richer schema. Otherwise we fall back to a
				// pre-existing carve-out table:
				//   - providerOptions (domains_create / domains_update):
				//     drop the additionalProperties constraint entirely
				//     so booleans round-trip natively.
				//   - requires (apps/add / apps/update / deploy): map of
				//     alias → {id, type, config, env}; emit the structured
				//     value schema so MCP clients don't have to stringify.
				//     Regression target: I26-N.
				//   - everything else: default to string values for
				//     back-compat with existing map[string]string fields
				//     whose manifest entries pre-date valueType. LLM libs
				//     that strict-validate the schema (some versions of
				//     the Anthropic SDK tool wrapper) accept it.
				if prop.Type == "object" {
					prop.AdditionalProperties = s.projectObjectValue(field)
				}
				if prop.Type == "array" {
					prop.Items = s.projectArrayItems(field)
				}
				tool.InputSchema.Properties[field.Name] = prop

				if field.Required {
					tool.InputSchema.Required = requireOnce(tool.InputSchema.Required, field.Name)
				}
			}

			for _, flag := range cmd.Input.Flags {
				tool.InputSchema.Properties[flag.Name] = Property{
					Type:        "boolean",
					Description: flag.Description,
					Default:     flag.Default,
				}
			}
		}

		tools = append(tools, tool)
	}

	// Append static apps_* tools (pull/diff/sync/list-previous-uploads)
	// for the categories they belong to. These aren't manifest-driven.
	tools = append(tools, staticAppsTools(s.category)...)
	// Append static services_* tools (pull/diff/sync). Same rationale:
	// orchestrating local yaml files, not pure API operations.
	tools = append(tools, staticServicesTools(s.category)...)
	// Append jobs_follow — long-poll streaming until terminal state,
	// shape the manifest dispatcher doesn't model.
	tools = append(tools, staticJobsTools(s.category)...)
	// Append manifest_update: every category can be the one holding a
	// command list that predates the last conductor deploy (B2).
	tools = append(tools, manifestUpdateTool())
	// Append `run` — blocks streaming container output and exits with
	// the container's real exit code; a shape the manifest's one-call
	// dispatcher can't model. Only surfaces under sensitive_write
	// since it mutates cluster state.
	tools = append(tools, staticRunTools(s.category)...)
	// Only worth listing when something is actually hidden: an unscoped
	// server has nothing to add, and the tool would be dead weight.
	if s.toolsets.Scoped() && len(s.toolsets.Hidden()) > 0 {
		tools = append(tools, Tool{
			Name:        toolsEnableToolName,
			Description: s.toolsets.enableToolDescription(),
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"types": {
						Type:        "array",
						Description: "Service types to load, e.g. [\"kafka\",\"vllm\"].",
						Items:       &Property{Type: "string"},
					},
				},
				Required: []string{"types"},
			},
			Annotations: &ToolAnnotations{ReadOnlyHint: true},
		})
	}

	return tools
}
