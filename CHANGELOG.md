# Changelog

## v1.0.0-rc.6

Adds VCS deploys, splits env vars into Secret + ConfigMap, and brings live build-log streaming to `runos deploy --follow`. Install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.6`.

### New

- **VCS deploys**: `runos deploy` now dispatches on the app's `deployType`. CLI-deploy apps still tarball the local source and upload (unchanged). VCS-deploy apps send `{sha, configPath}` only; the conductor and cluster agent pull source from the linked GitHub/GitLab integration at the SHA, build in-cluster, push, and roll out. New flags: `--app <id>` for CI mode (no yaml on disk), `--sha` (defaults to `git rev-parse HEAD`), `--allow-dirty` (waives the dirty-tree refusal). The two modes never silently intermingle: passing `--sha` / `--allow-dirty` against a CLI-deploy app is a hard error, and passing `--app` against a CLI-deploy app is rejected after a server-side `deployType` lookup.
- **`configPath:` is the source of truth for a VCS yaml's repo location**: `runos deploy` auto-derives the repo-relative path of the local yaml (`git rev-parse --show-toplevel` + `filepath.Rel`) and sends it on every VCS deploy. Conductor persists it to the AppDocument, so subsequent CI deploys (`runos deploy --app <id> --sha <sha>` without a checkout) inherit the persisted value. Monorepos with apps under per-app subdirectories (`apps/billing/runos.dev.yaml`) work without manual config. Explicit `configPath:` field in the yaml is still honoured as the escape hatch for non-standard layouts.
- **Env vars split into Secret + ConfigMap**: alongside the existing `.runos.<cid>.<id>.env` (sensitive, gitignored, K8s Secret), `runos deploy` / `apps pull` / `apps sync` / `apps diff` now also handle `runos.<cid>.<id>.config.env` (plain, committed, K8s ConfigMap). Both flow into the pod via `envFrom` so app code reads them identically. A key may not appear in both files at once; conductor refuses the deploy with a 400 listing conflicts, and the CLI pre-flights the same check. Pull/sync/diff round-trip both sides independently.
- **`runos deploy --follow` streams per-step build logs**: the follow poller now fetches new work-item log entries on each tick (cursor-paginated via the new `/jobs/:id/workitems/:id/logs` endpoint) and indents them under their parent step. Previously only status transitions surfaced; long-running build steps now show progress in real time. GitHub Actions runs wrap the streamed output in a `::group::` block so it collapses by default and surface the public URL via `::notice::` after a successful deploy.
- **`requires.<alias>.env` collision detection**: when a local env file hand-authors a key that the platform injects at runtime from a linked service's credentials (e.g. `DATABASE_URL` written locally while `requires.db.env.url=DATABASE_URL` claims it), `runos deploy` and `apps sync` flag it as informational dead config. Conductor drops the colliding key from `customEnvVars` on every deploy so the runtime value always reflects the linked service; the message points at the cosmetic cleanup.
- **Pre-deploy code drift gate**: `runos deploy` refuses when newer CLI archives exist on the server (someone else deployed via console or CI between this directory's last `apps pull --code` / deploy and now). The check is fail-closed (API failure refuses the deploy) so a fail-open never lets through the very thing the gate exists to prevent. Pass `--force` to override.
- **`runos deploy --force`**: a single bypass flag for both pre-deploy gates (yaml drift and code drift). The diff is shown either way.

### Improvements

- **`apps pull` / `apps diff` correctly read `deployType`**: prior versions conflated provider name with deploy type and stored `github-arc` / `gitlab-runner` as `deployType`, producing perpetual drift on `apps diff` because the server emits `vcs`. Provider identity now flows via the `Integration` block alongside repo/branch metadata; `deployType` carries the canonical `cli` | `vcs` value with `integrationType` as a legacy fallback for older conductors.
- **Class-flap heads-up note on `apps diff`**: when the server stored `resourceRequirementClassId=custom` because cpu/memory/replicas overrides disagree with the named class's defaults (server's `resolveRRC` synthesis), the diff now surfaces a one-line note explaining the mechanic and how to round-trip cleanly. Previously this looked like fresh class drift on every sync.
- **MCP `deploy` tool gains VCS parameters**: `app`, `sha`, `allow_dirty`. Pre-validation stays in the CLI binary (one source of truth for the deployType branching rule) so the MCP shim only translates args. The tool description spells out which flags belong with which deploy type.
- **MCP JSON Schema for object/array fields**: the manifest-driven tool generator now emits `additionalProperties: {type: string}` for `object` fields and `items: {type: string}` for `array` fields. Strict-validating LLM clients (some Anthropic SDK tool wrappers) previously rejected the unannotated schemas. The `object` manifest type now maps to JSON Schema `object` instead of being collapsed to `string`.
- **MCP defensive JSON-string coercion**: when an MCP client ignores the declared field type and sends an `object` / `array` field as a JSON-encoded string (`envVars: '{"K":"v"}'`), the executor now decodes in-place so the wire body matches the manifest's declared type. Conductor previously rejected it as "not an object".
- **`apps pull` pulls both env-var sides independently**: secret env file is gitignored on first pull (created with mode 0600), plain config env file is created with mode 0644 ready to commit. Diff/sync round-trip each side against its corresponding K8s resource.
- **Pre-deploy drift gate validates IDs as defence-in-depth**: app ID and cluster ID from the local yaml are charset-validated via `apps.ValidateIdentifier` before being joined into URLs, so a tampered yaml can't smuggle path components into API requests.
- **Build stamp on local builds**: `make local` now stamps `dev-<utc-timestamp>` into the version string, so dev binaries are version-distinguishable in the wild. Stable releases continue to stamp the git tag.
- **Documentation**: the README and CLAUDE.md gain a deploy-verb section covering the deployType dispatch, the three cluster-id sources (flag → config → yaml), the SHA / dirty-tree gate, and the env-var Secret/ConfigMap split.

### Bug fixes

- **`apps diff` `--redact-secrets` covers the new secret env section**: values in the new `SecretEnv` `SectionDiff` are redacted in the unified-diff output by the same flag that already redacts `.env` content. The MCP `apps_diff` tool keeps passing `--redact-secrets` so secret values never reach the LLM context.

## v1.0.0-rc.5

Bug fix on top of v1.0.0-rc.4. Install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.5`.

### Bug fixes

- **`services pull` no longer projects class-derived cpu/memory/replica fields when a real resource class is stored**: now that the API manifest declares `replicas` / `cpuRequestMc` / `cpuLimitMc` / `memoryRequestMb` / `memoryLimitMb` as settable on `services/<type>/{id}/update` (so they can be overridden), the manifest-driven pull was projecting them into every yaml regardless of the stored class. With a real class set, the API encapsulates those values, so the yaml only needs the class. The CLI now mirrors the apps_pull preset-wins gate: real class → strip the five derived fields; `custom` (or empty / legacy) → keep them as the user-owned source of truth. Eliminates the false drift introduced by rc.4 on every service whose manifest exposes resource fields.

## v1.0.0-rc.4

CI/CD UX fixes on top of v1.0.0-rc.3. Install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.4`.

### Improvements

- **Yaml is the source of truth for cluster id**: `apps sync` / `apps diff` / `apps pull <yaml>` / `apps pull` (auto-detect) / `apps list-previous-uploads` and `services sync` / `services diff` / `services pull <yaml>` now read the cluster id from the yaml's `cid:` field. `--cid` (and `RUNOS_CLUSTER_ID`) become optional cross-check guards: passed values are still validated against the yaml, and any mismatch refuses the operation. CI loops over committed yamls no longer need a default cluster set, no `--cid` per call, no `RUNOS_CLUSTER_ID` env var. Commands that don't have a local yaml to source from (`apps pull --all`, `apps pull --app-id`, `services pull --type+--service-id`) still require an explicit cluster id.
- **CDN config schema**: `RemoteDomains` now reads the canonical `api` field, falling back to the legacy `conductor` field for older payloads. Driven by a new `APIURL()` resolver, used everywhere the CDN environment is consumed.

## v1.0.0-rc.3

Build + release-workflow fixes on top of v1.0.0-rc.1. Install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.3`. (rc.2 was an aborted attempt: the tag exists on GitHub but no release was published, and rc.3 supersedes it.)

### Bug fixes

- **Windows build**: `syscall.O_NOFOLLOW` (used in `apps_pull`'s symlink-refusal write path) doesn't exist on Windows, so the rc.1 release workflow failed at the `windows/amd64` step. Replaced with a build-tagged constant: protection is preserved on Unix; the flag is a no-op on Windows where the equivalent OS-level guarantee isn't available.
- **Release workflow under Immutable Releases**: the repo has Immutable Releases enabled, which disallows asset uploads after a release is published. The workflow now creates the release as a draft so asset uploads succeed, then publishes the draft in a follow-up step.

## v1.0.0-rc.1

Foundational release. Introduces IaC workflows for both apps and services, headless authentication for CI/CD, and a topic-driven documentation system for AI assistants. This is a release candidate for testing; install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.1`. The default install URL keeps serving the previous stable.

### New

- **Apps as IaC**: `runos apps pull` / `apps diff` / `apps sync` round-trip an app's full configuration (yaml, env vars, secret files, manifest overrides) between cluster and disk. Pull supports bulk mode (`--all`), single-app mode, code-archive download (`--code` / `--code-version`), drift gates, and section-level diff reports. Sync pushes deltas via dedicated endpoints (PATCH /apps, env-vars, secret-files, overrides, requires).
- **Services as IaC**: `runos services pull` / `services diff` / `services sync` for postgresql, valkey, mysql, and any future service type the manifest exposes. Schema is manifest-driven (zero per-type code in the CLI); a new service type or new settable field on the API side flows through automatically after `runos manifest update`.
- **Service yaml shape is desired-state only**: `runos.service.<cid>.<type>.<sid>.yaml` contains exactly the fields the user can set (`add.Input.Fields ∪ update.Input.Fields ∪ add.Input.Flags`). Audit, derived, and operational fields the show response carries are filtered out automatically. Filename is convention; renames are tolerated via header-based lookup.
- **Apps integration with services IaC**: `apps_pull` cascades into pulling linked service yamls; `apps_sync` refuses `requires:` entries without an `id:` (provisioning is the service-yaml's job); `runos deploy` keeps the `requires.<alias>.class` shorthand and now writes a service yaml on first-time provisioning.
- **PAT-based authentication**: `runos account api-keys add` / `list` / `show` / `update` / `revoke` for managing personal access tokens. The CLI consumes a PAT via `RUNOS_API_KEY` (plus `RUNOS_ACCOUNT_ID`, optional `RUNOS_API_URL`); when set, the CLI bypasses Firebase entirely. Headless CI runners need no `~/.runos/config.json` and no interactive `runos login`.
- **Topic-driven MCP documentation**: the MCP read server now requires the LLM to consume a minimum number of distinct topics (apps, services, api-keys, cicd, ...) before non-topic tools unlock, so AI assistants ground themselves in the workflow before executing it. Topic content is delivered to MCP clients separately from the CLI binary.
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
