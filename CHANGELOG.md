# Changelog

## v1.0.0-rc.7

Bug-fix and hardening pass on top of v1.0.0-rc.6, driven by iterative test sessions against `apps pull` / `apps sync` / `apps diff` / `services sync` / `runos deploy` / MCP. Adds an empty-secret-env wipe gate, a `jobs_follow` MCP tool, an `--keep-env` flag on `apps pull`, and a regression-test methodology document. Install via `https://get.runos.com/cli.sh?release=v1.0.0-rc.7`.

### New

- **Empty-secret-env wipe gate**: `apps sync` now refuses to push when the local secret-env file is empty (or carries only platform-injected names like `DATABASE_URL`) AND the server has user-set secret keys. Catches the fresh-checkout footgun where a gitignored secret file isn't on disk and the LLM/CI silently wipes prod secrets. The refusal names the keys that would be deleted. Pass `--allow-empty-secret-env` (CLI) / `allow_empty_secret_env=true` (MCP) when the wipe is intentional. The `--yes` confirm-skip flag does NOT waive this gate, since `--yes` is "skip the prompt for the safe case" and not "I'm OK with destructive ops".
- **`apps pull --keep-env`**: opt-out from env file rewrites. `apps pull` writes both `.runos.<cid>.<id>.env` and `runos.<cid>.<id>.config.env` from server values by default; `--keep-env` preserves the local files instead. Useful when the user has dev-only env edits they want to keep while still refreshing yaml, secret-files, and overrides. Stdout shows the kept paths so the user knows nothing was rewritten and can run `apps diff` to see the divergence. Exposed on the MCP `apps_pull` tool as `keep_env`.
- **`jobs_follow` MCP tool**: long-poll streaming wrapper around `runos follow <jobId>`. Blocks until the job reaches a terminal state (success / failed / cancelled) or the 10-minute subprocess timeout fires, then returns the full streamed work-item log + final status as a single text payload. The canonical "wait until done" verb for orchestrations: cheaper than polling `jobs_show` in a loop and doesn't burn the LLM's prompt cache. Read-only.
- **TESTING.md**: testing philosophy, conventions, patterns, and a running list of regression tests by fix. The bar: a test earns its place if a future change that re-introduces a known bug would fail it. Pure-function regression tests on extracted helpers are the dominant pattern; we explicitly do NOT write cobra-level subprocess tests or end-to-end tests against a live conductor.

### Bug fixes

- **`runos deploy` env merge flipped to local-wins**: the pre-deploy sync used to refuse on any same-key-different-value conflict between local and server, blocking the dominant path (every version bump, every flag flip) until the user manually reconciled. The merge now copies the local file verbatim and adds only server-only keys; the deploy that follows pushes local up and replaces the server-side env in full. Same-key-different-value is silently the local value's win, which matches what the user expected when they edited the file.
- **`runos deploy` skips env file write when the merge is a no-op**: if the local file already has every server key, the merge skips the write entirely. Prevents the inverse footgun where a fresh checkout with no local env file would produce a strict no-op merge, the file would never materialise, and the subsequent deploy body would carry an empty `customEnvVars` map that conductor interpreted as "user wants empty ConfigMap" and wiped the server's env on every deploy.
- **`apps sync` override delete now removes the orphan local file**: the delete op carries `LocalLeaf` (the leaf filename inside `appDir/overrides/` that pull would have written for this override, derived via `OverrideFilenames` over the full server list so collision-disambiguated names match what pull wrote). Apply unlinks the local file alongside the server delete; previously deleting an override server-side left the local file orphaned and the next pull-then-sync would re-create it.
- **`apps diff` no longer flags local-only `sourceDir` as drift forever**: `sourceDir` is a CLI-only field (build-context resolution for `runos deploy`) that never reaches conductor and never appears in server-rendered yaml. The diff engine strips top-level `sourceDir:` lines from local bytes before comparison. Pulled yamls that include `sourceDir: ..` (the directory-per-app shape) no longer report drift on every diff.
- **`apps pull` preserves `resourceRequirementClassId: "custom"`**: prior versions rewrote the literal string `"custom"` as empty during pull, which fought with `apps show` (which surfaced `resourceRequirementClassId="custom"` from the server's `resolveRRC` synthesis). Pull now preserves the literal so `apps_show` and `apps_pull` return symmetric views; materialised cpu/memory fields are still emitted alongside, since `"custom"` by itself carries no values.
- **`apps sync` no longer renders preserve-sentinels as drift**: `clusterDomainId`, `resourceRequirementClassId`, and `replicas` are partial-update fields on the conductor's PATCH endpoint (omitted local value means "preserve server value", not "clear / set to zero"). The wire-body builder already omitted these correctly, but the dry-run plan was rendering alarming `replicas: 1 -> replicas: 0` lines for every yaml that omitted them, scaring users into refusing the (harmless) sync. Drift reporting now mirrors the wire body's omit logic.
- **`apps diff` exits 2 on drift**: structured exit code so CI can branch on "config drift detected" without parsing stdout. Exit 0 = in sync; exit 2 = drift; any other non-zero = real failure.
- **MCP `apps_diff` translates exit code 2 to success**: drift is the structured signal `apps_diff` returns, not a failure mode. The MCP wrapper now hands the drift report back to the LLM caller as a normal text payload instead of wrapping it in a red error block. The translation lives in a pure helper (`interpretAppsCommandResult`) tested directly with synthesized `*exec.ExitError`. CI users still see the non-zero exit at the CLI layer.
- **`services sync` class-only swap no longer flips class to "custom"**: pulling a service materialises the active class's cpu/memory baseline into the local yaml. Editing only the class line and syncing was sending the OLD baseline cpu/memory in the wire body; conductor's `resolveRRC` interpreted those as overrides against the NEW class baseline and flipped class to "custom". The PATCH body is now drift-only (`computeDriftPatch`): fields whose local value matches the server are omitted, so a class-only swap touches only the class field.
- **`apps pull` user-set secret env count no longer double-counts platform-injected keys**: `envVarCount` in the pull JSON summary used to sum plain + secret without filtering, which inflated the count and disagreed with what the `apps_env-vars` endpoint returned. Now `envVarCount` matches `apps_env-vars` keys exactly, and a separate `secretEnvVarCount` reflects user-set secret keys only (platform-injected keys claimed by `requires.<alias>.env` mappings are filtered out of the user-facing count).
- **MCP `domains_create` / `domains_update` `providerOptions` round-trips booleans**: the manifest-driven schema generator was emitting `additionalProperties: {type: string}` for `object` fields, which forced LLMs to send `proxy: "true"` (string) into a field that `domains_list` returned as `proxy: true` (bool). The conductor's `normalizeProviderOptions` was coercing back, but the schema mismatch was visibly confusing. The generator now drops the `additionalProperties` constraint specifically for `providerOptions` so booleans round-trip natively.

### Improvements

- **`apps sync` drops the "remove these dead-config keys" advisory**: previous versions emitted a "the platform injects these at runtime" note when the local secret-env file contained names claimed by `requires.<alias>.env` mappings (DATABASE_URL, REDIS_URL, etc.). The advice was wrong-headed: those keys are written to the local file by `apps pull` so it matches the K8s Secret, and the pre-deploy merge re-introduces them on every deploy. Removing them makes local LESS accurate, not more. The note and its formatter are gone; the underlying detection helper is retained as `FindServerInjectedEnvCollisions` and now used only to filter `runos deploy`'s "got merged back" warning so the user isn't pestered about keys that re-appear by design.
- **Documentation**: CLAUDE.md links to TESTING.md from the testing section. New tests added across `internal/apps/sync_test.go`, `internal/apps/pull_test.go`, `internal/apps/diff_test.go`, `internal/mcp/apps_test.go`, `cmd/deploy_test.go`, `internal/services/sync_test.go` covering each fix above as a regression test.

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
