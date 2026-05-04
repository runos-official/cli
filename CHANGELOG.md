# Changelog

## v1.0.0-rc.1

Foundational release. Introduces IaC workflows for both apps and services, headless authentication for CI/CD, and a topic-driven documentation system for AI assistants. This is a release candidate for testing; install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.1`. The default install URL keeps serving the previous stable.

### New

- **Apps as IaC**: `runos apps pull` / `apps diff` / `apps sync` round-trip an app's full configuration (yaml, env vars, secret files, manifest overrides) between cluster and disk. Pull supports bulk mode (`--all`), single-app mode, code-archive download (`--code` / `--code-version`), drift gates, and section-level diff reports. Sync pushes deltas via dedicated endpoints (PATCH /apps, env-vars, secret-files, overrides, requires).
- **Services as IaC**: `runos services pull` / `services diff` / `services sync` for postgresql, valkey, mysql, and any future service type the manifest exposes. Schema is manifest-driven (zero per-type code in the CLI); a new service type or new settable field on the API side flows through automatically after `runos manifest update`.
- **Service yaml shape is desired-state only**: `runos.service.<cid>.<type>.<sid>.yaml` contains exactly the fields the user can set (`add.Input.Fields ∪ update.Input.Fields ∪ add.Input.Flags`). Audit, derived, and operational fields the show response carries are filtered out automatically. Filename is convention; renames are tolerated via header-based lookup.
- **Apps integration with services IaC**: `apps_pull` cascades into pulling linked service yamls; `apps_sync` refuses `requires:` entries without an `id:` (provisioning is the service-yaml's job); `runos deploy` keeps the `requires.<alias>.class` shorthand and now writes a service yaml on first-time provisioning.
- **PAT-based authentication**: `runos account api-keys add` / `list` / `show` / `update` / `revoke` for managing personal access tokens. The CLI consumes a PAT via `RUNOS_API_KEY` (plus `RUNOS_ACCOUNT_ID`, optional `RUNOS_API_URL`); when set, the CLI bypasses Firebase entirely. Headless CI runners need no `~/.runos/config.json` and no interactive `runos login`.
- **Topic-driven MCP documentation**: four topic files (apps, services, api-keys, cicd) under `gitignored/mcp-topics/`. The MCP read server requires the LLM to consume a minimum number of distinct topics before non-topic tools unlock, so AI assistants ground themselves in the workflow before executing it.
- **Service delete guard (409)**: `DELETE /services/<type>/<id>` returns a structured 409 listing dependent apps when any reference the service. The CLI renders this as a multi-line refusal naming each dependent and its alias, with reconcile guidance.
- **`runos follow <jobId>`**: blocking job-status follower with line-oriented output (one line per state change, no escape codes, no spinners, no repaints). Suitable for CI logs and AI consumers; exit code gates downstream pipeline steps.

### Improvements

- **Pre-deploy drift gate**: `runos deploy` refuses when local diverges from server state (yaml or source archive). Pass `--force` to override; the diff is shown either way.
- **Manifest schema tolerance**: `output.fields` now accepts both legacy bare-string entries and the new rich-object shape (`{name, type, description, enum}`). New API-side field metadata flows through without a CLI release.
- **Tarball walker** unconditionally excludes RunOS-managed manifests (`runos*.yaml`, `runos.*/`, `overrides/`, dot-prefixed files) from every deploy archive, regardless of `.dockerignore`. Cross-cluster config can't bleed into a source upload.
- **`apps_pull` writes a default `.dockerignore`** on first pull (skipped when present) covering the same exclusions so external Docker builders honour the same rules.
- **Multi-cluster project shape**: directory-per-app (`runos.<cid>.<id>/` subdirs with `sourceDir: ".."`) is the recommended shape for repos deploying to multiple clusters; `apps_pull` defaults to it and stamps `sourceDir` automatically.

### Renames (breaking since v0.3.9)

- **Env var**: `CONDUCTOR_API_URL` → `RUNOS_API_URL`. Update CI secrets and shell rc files.
- **Config key**: `runos config set conductor-url ...` → `runos config set api-url ...`. Existing on-disk config files keep working unchanged (the internal JSON field is still `conductor_url`).
- **Status output label**: `Conductor:` → `API:` in `runos status`.
- **Topic prose / README**: every reference to the internal codename "conductor" replaced with "RunOS API" or equivalent. Internal Go field names and the `RemoteDomains.Conductor` CDN-config schema kept as-is.

### Internal

- `internal/apps/`: new package backing the apps IaC commands.
- `internal/services/`: new package backing the services IaC commands.
- `internal/auth/resolve.go`: `ResolveToken(cfg)` is the single auth-token entry point; used by every API-touching subcommand.
- `internal/dynacmd/executor.go`: new `ExecuteWithInput` programmatic entry; `*APIError` typed error so callers can branch on status; shared `dispatch` between Execute paths.
- `internal/manifest/types.go`: `Output.Fields` typed as `[]OutputField` with custom Unmarshal/Marshal for forward-compat.
- `internal/jobs/`: `DisplayFollow` (terminal-clear + spinner repaint) replaced by `EmitFollowDeltas(io.Writer, ...)`.
- New direct dep: `github.com/pmezard/go-difflib` (unified-diff output).
- `golang.org/x/term` promoted from indirect to direct.

### Release-workflow

- `vX.Y.Z-<suffix>` tags are now auto-marked as GitHub prereleases (so `releases/latest/...` keeps serving the previous stable). Stable tags (`vX.Y.Z` with no suffix) become the latest. See [.github/workflows/release.yml](.github/workflows/release.yml).

## v0.3.9

### Improvements
- **Fix changelog extraction in release workflow** — release notes were not being extracted due to a broken awk/sed command
- **Add CI test for changelog extraction** — verifies all changelog entries are extractable on every push/PR
- **Document release process** in README and CLAUDE.md

## v0.3.8

### Improvements
- **Changelog-driven release notes** — GitHub releases now pull notes from CHANGELOG.md, falling back to auto-generated notes if no entry exists

## v0.3.7

### Bug fixes
- **Fix MCP path parameter substitution** — AI models sending prefixed field names (e.g. `app_id` instead of `id`) now resolve correctly by deriving the expected prefix from the URL path entity
- **Fix boolean type mapping in MCP tool schema** — boolean manifest fields (like `enabled` on overrides) were incorrectly exposed as `string`, causing API rejections

### Improvements
- **MCP CLI version check** — the `cli_version-check` tool now auto-injects the current CLI version and OS, allowing AI assistants to check for updates without needing to know the version

## v0.3.6

### Bug fixes
- **Fix MCP path parameter substitution when AI prefixes field names** — resolves issue where literal `:id` appeared in API URLs instead of actual resource IDs
