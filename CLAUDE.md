# RunOS CLI

Command-line interface for interacting with RunOS clusters. RunOS is a self-hosted cloud platform (similar to AWS) where users bring their own hardware, allowing cloud infrastructure across multiple providers.

## Project Overview

This CLI enables users to:
- Create and manage Kubernetes clusters
- Provision services (e.g., PostgreSQL instances)
- View cluster stats and health
- Generate node installation commands
- Manage database users and credentials
- Poll long-running operations via job IDs

The CLI communicates with the same REST API used by the RunOS web console.

## Backend: Conductor

The primary backend for this CLI is **Conductor**. The CLI manifest serves as the source of truth for API documentation.

### API Documentation via Manifest

The CLI manifest (`~/.runos/manifest.json`) serves two purposes:
1. Define available CLI commands for users
2. Provide API endpoint specifications for development

**To get the latest API definitions:**
```bash
runos manifest update
```

**Then read the manifest:**
```bash
cat ~/.runos/manifest.json
```

The manifest contains all endpoint definitions including:
- Endpoint paths (e.g., `/:aid/:cid/services/:type`)
- HTTP methods (GET, POST, PUT, DELETE)
- Input fields (name, type, required, enum values, defaults)
- Output schema (response fields)

## Tech Stack

- **Language**: Go
- **CLI Framework**: Cobra
- **Config Location**: `~/.runos/`
- **Authentication**: Firebase Auth (Google SSO, GitHub SSO, password auth, 2FA)

## Code Conventions

### Go Idioms
- Follow standard Go conventions (Effective Go, Go Code Review Comments)
- Use `PascalCase` for exported identifiers, `camelCase` for unexported
- Use `snake_case` for file names
- Keep packages focused and small
- Prefer composition over inheritance
- Handle errors explicitly, don't ignore them
- Use meaningful variable names; avoid single letters except in short scopes
- Initialisms stay all-caps in exported names: `URL`, `ID`, `MD5`, `HTTP`, `API`, `JSON`, `CID`, `AID` (not `Url`, `Id`, `Md5`, etc.). Lowercase initialisms in unexported short locals (`cid`, `aid`, `url`) are fine.
- Detect specific error conditions with typed sentinels (`var ErrXxx = errors.New(...)`) and `errors.Is` / `errors.As`, not `strings.Contains(err.Error(), ...)`
- Use stdlib `sort.Slice` / `sort.Strings`; never hand-roll bubble or insertion sorts
- Doc comment every exported type, function, and const block. Lead with the symbol name
- Network timeouts, body-size caps, and similar magic numbers live as named constants near the top of the package, not inline at call sites

### Project Structure
```
cli/
├── cmd/                    # Cobra commands (one file per command/subcommand)
├── internal/
│   ├── api/                # REST API client
│   ├── auth/               # Firebase authentication
│   ├── cache/              # File-based TTL cache
│   ├── config/             # Configuration management
│   ├── deploy/             # Deployment pipeline (archive, upload)
│   ├── dynacmd/            # Dynamic command builder from API manifest
│   ├── jobs/               # Job polling and progress display
│   ├── manifest/           # CLI manifest loading and parsing
│   ├── mcp/                # MCP server implementation
│   ├── output/             # Output formatting (plain text, JSON)
│   └── update/             # CLI self-update mechanism
├── version/                # Version constant (set via ldflags)
├── scripts/                # Install/uninstall scripts
├── main.go
├── go.mod
└── go.sum
```

### Command Naming
- Use kebab-case for multi-word commands: `create-cluster`, `list-nodes`
- Group related commands under parent commands: `runos postgres create`, `runos postgres list`

## Deploy verb: dispatch on app.deployType

`runos deploy` is a single verb that handles both deploy types. The dispatch is in [cmd/deploy.go](cmd/deploy.go); the VCS branch lives in [cmd/deploy_vcs.go](cmd/deploy_vcs.go).

| App type | What `runos deploy` does | Endpoint |
|---|---|---|
| `deployType: cli` | Tarballs the local source dir, uploads, build server runs BuildKit + push + rollout | `POST /:aid/:cid/prepare-cli-deployment` |
| `deployType: vcs` | Sends `{sha, configPath}` only; conductor + cluster agent pull source from the linked GitHub/GitLab integration at the SHA | `POST /:aid/:cid/apps/:id/deploy` |

VCS dispatch logic in `runDeploy`:

1. `--app <id>` flag → CI mode, no yaml load. Cid must come from `--cid` or config default. Server-side deployType check rejects passing `--app` against a CLI-deploy app.
2. Otherwise: load yaml, fall back to yaml's `cid:` field if neither flag nor config default. Resolve deployType from `deployConfig.DeployType` first, then from a server lookup if absent.
3. CLI-deploy guard: passing `--sha` / `--allow-dirty` against a CLI-deploy app is a hard error so the two modes never silently intermingle.

### Three cluster-id sources, priority order

Set in `cmd/deploy.go`. **Yaml-as-third-source matters**: a checked-in `runos.yaml` that already carries `cid:` should not require a per-machine flag/config to deploy from a fresh clone.

1. `--cid` flag.
2. CLI config default (`runos config set cid <id>`).
3. The yaml's `cid:` field (loaded from disk).

Hard error fires only after step 3, so a yaml-bearing deploy works out of the box. The `--app` CI-mode branch errors at step 2 since no yaml is loaded.

### SHA resolution and the dirty-tree gate (VCS only)

In [cmd/deploy_vcs.go](cmd/deploy_vcs.go):

- `--sha` flag wins.
- Otherwise: `git rev-parse HEAD` if inside a git checkout. Error if not.
- `git status --porcelain` after resolution: if dirty, **refuse** unless `--allow-dirty`. The build runs against the committed source on the git host; uncommitted edits would silently not be in the build.

### configPath: yaml is the source of truth for its own repo location

VCS apps can live in monorepo subdirectories (e.g. `apps/billing/runos.dev.yaml`). The cluster agent reads the committed yaml at a `configPath` stored on the AppDocument. The CLI auto-populates this every laptop deploy:

[cmd/deploy_vcs.go:resolveVcsConfigPath](cmd/deploy_vcs.go) — three sources:

1. Explicit `configPath:` field in the local yaml (escape hatch for non-standard layouts).
2. Auto-derived from the yaml's filesystem path relative to `git rev-parse --show-toplevel`. The common case: user doesn't set anything.
3. Empty (CI mode without a yaml on disk, or yaml lives outside the repo) — conductor falls back to whatever the AppDocument has stored.

The CLI sends `configPath` in the `POST /apps/:id/deploy` body; conductor's `prepareVcsDeployment` persists it to the AppDocument before queueing the orchestration. Subsequent CI deploys (`runos deploy --app <id> --sha <sha>` without a yaml) inherit the persisted value automatically — yaml-as-source-of-truth invariant.

The deploy output prints the resolved configPath (or `<not sent>`) so a missed auto-derive is visible immediately instead of cascading into a confusing "yaml not found" error from the cluster agent.

### Env vars on VCS apps

Env vars are split into two parallel sources, each backed by a different K8s resource. Both flow into the pod via `envFrom`, so app code reads them identically:

| Source | Local file | K8s resource | VCS | Holds |
|--------|-----------|--------------|-----|-------|
| Secret | `.runos.<cid>.<id>.env` (0600, gitignored) | `<osid>-user-env-vars` Secret | no | credentials, tokens |
| ConfigMap | `runos.<cid>.<id>.config.env` (0644, committed) | `<osid>-user-env-config` ConfigMap | yes | log level, feature flags, public URLs |

The Secret keeps its legacy K8s resource name (`<osid>-user-env-vars`) on purpose: env vars set via the console pre-split read/write that exact name, so leaving it in place preserves existing data without a migration step. The new ConfigMap takes a distinct slot.

A key may NOT appear in both files at once. The conductor hard-fails the deploy with a 400 listing the conflicting keys; `runos deploy` and `runos apps sync` also pre-flight the check on the client.

Both sources are managed independently of `runos deploy --sha`. Set them via:

- `runos apps sync <yaml>` — pushes both local files (replace-all per source: additions, edits, deletions).
- Console UI / `POST /apps/:id/secret-env-vars` (sensitive) / `POST /apps/:id/env-vars` (plain) / `POST /apps/:id/secrets` (atomic combined).

`runos deploy --sha <sha>` does NOT touch either source (intentionally — `.env` isn't committed; `.config.env` is, but env state is conceptually orthogonal to image deploys). The conductor's `deploy.vcs` orchestration reads both the existing Secret and the existing ConfigMap on its apply step and round-trips them, so env vars survive every VCS deploy. CI deploys (no laptop, no env files on the runner) just rebuild what's already on the server.

## Postgres adopt-user and discrete-field requires.env

`runos services postgresql adopt-user --id <svc> --cid <cid> --username <u> --database <db> (--rotate-password | --password <specific>)` brings an existing Postgres role under RunOS management. Sibling to `create-database`, distinct verb: where create-database is greenfield, adopt-user takes over a role that already exists (restored, cloned, or migrated). Idempotent. Always reassigns ownership of objects in the target database to the role (tables + sequences + the `public` schema; extensions stay owned by `postgres`). Always ends with a RunOS-managed password (one of `--rotate-password` / `--password` is required; passing both is rejected). Returns credentials once, like create-database; the role then appears in `services postgresql users` tagged owner and credentials are retrievable via `services postgresql user-credentials`.

The verb is fully manifest-driven (no static cobra code in this repo, no MCP shim). After the conductor side ships, `runos manifest update` picks up the entry and `runos services postgresql adopt-user --help` renders.

The companion piece is the discrete-field `requires.env` injection: an app's `requires.<alias>.env` map can route individual Postgres credential fields to arbitrary env-var names, not just the legacy single `url: DATABASE_URL` shape. Supported field keys: `url`, `host`, `port`, `user`, `password`, `database`. Note `user`, not `username`; the adopt-user verb's `--username` flag is unrelated to the requires.env key.

```yaml
requires:
  growthco-db:
    id: myosid
    type: postgresql
    config:
      databaseName: growthco_sor
      databaseUsername: growthco_sor
    env:
      host:     POSTGRES_SERVER
      port:     POSTGRES_PORT
      database: POSTGRES_DB
      user:     POSTGRES_USER
      password: POSTGRES_PASSWORD
      url:      DATABASE_URL
```

`requires.env` is a `map[string]string` in [internal/deploy/config.go](internal/deploy/config.go) `ServiceRequirement.Env` and the consumers in [internal/apps/requires_env.go](internal/apps/requires_env.go) (`FilterPlatformInjectedEnv`, `FindServerInjectedEnvCollisions`) iterate field-key-agnostically, so the discrete-field shape works through the CLI without any code change. The collision-detection / drift-gate filter already pulls every right-hand-side env name into the platform-injected set, so a deploy against an adopted app doesn't trip the I3-E false-positive drift gate for any of the six fields. The regression-test pin lives in [internal/apps/requires_env_test.go](internal/apps/requires_env_test.go).

Linking the adopted instance to the app is the existing `requires:` link-to-existing mechanism (service id + config). `requires:` does NOT auto-invoke adoption on deploy — adoption is an explicit operator pre-step (it mutates object ownership and the password, which must not happen implicitly on every push).

## MCP integration

The CLI also runs as an MCP server (`runos mcp`). Tools fall into two buckets:

### Manifest-driven (the default path)

The MCP server iterates `~/.runos/manifest.json` and generates one tool per command. Tool name = command path with `/` → `_` and `{id}` placeholders stripped (`apps/{id}/show` → `apps_show`). Input fields and output hints come straight from the manifest. **Adding a field to a manifest command exposes it on the corresponding MCP tool automatically** — no shim work needed.

### Static / handcrafted (escape hatch)

A short list lives in [internal/mcp/server.go](internal/mcp/server.go) (`isStaticAppsTool`) and [internal/mcp/apps.go](internal/mcp/apps.go) (`buildAppsCommandArgs`):

- `apps_pull`, `apps_diff`, `apps_sync`, `apps_list_previous_uploads` — all dispatch to runos subprocesses with bespoke arg translation because they use the local filesystem (yaml/dockerfile/.env) which the manifest can't represent.
- `deploy` — also handcrafted (in `server.go:566`, not in the static-apps list). Has a custom description covering the deployType branching and dirty-tree gate; new fields go through both the schema declaration and `buildDeployArgs`.

When extending: prefer adding to the manifest if the field maps cleanly to an HTTP body field. Drop into the handcrafted path only when the tool needs local-filesystem awareness or branch-specific guidance the manifest can't carry.

## Output Formatting

- **Default**: Plain text, human-readable
- **JSON flag**: Support `--json` or `-j` flag for JSON output (for scripting)
- Keep plain text output concise and scannable

## API Interaction

- Base URL is configurable (stored in config file)
- Long-running operations return a job ID
- Use progress bars when polling job status
- Handle API errors gracefully with clear messages

## Job-following convention

Every command that produces a server-side job follows the same shape:

- **Default = fire-and-forget.** The command issues the API call, prints the returned `jobId` (or a `Follow rollout: runos follow <id>` hint), and exits 0 the moment the conductor accepts the request. No waiting, no streaming, no auto-detection of TTY/CI.
- **Opt-in via `--follow` (`-f` where the short flag is free).** When set, the command blocks until the job reaches a terminal status, streams progress to stdout via `jobs.FollowJobWithService`, and exits non-zero if the job's terminal status is `failed` (carrying the conductor's `job.Error` verbatim).
- **No auto-follow based on TTY, CI env vars, or any other implicit signal.** The user passes `--follow` when they want to wait. Period. Mixing implicit and explicit follow caused real footguns (V12 silent-success, V10 surprise-blocking) and the inconsistency between deploy / apps_sync / services_sync / dynacmd flag defaults made the surface harder to reason about.
- **Multi-step commands** (e.g. `apps_sync` issues yaml-patch + env + secret-files + overrides as separate jobs): when `--follow` is set, wait per step. A failure aborts the rest of the plan and exits non-zero. Without `--follow`, all steps still run; the trailing `runos follow <id>` hint covers each emitted job so the user can manually wait.

Commands that emit jobs and so MUST have `--follow`:
- `runos deploy` (CLI + VCS deploy paths)
- `runos apps sync`
- `runos services sync`
- Manifest-driven dynacmd commands whose response shape declares `jobId` (auto-wired in `internal/dynacmd/builder.go`; default already opt-in)

The reusable primitive is `internal/jobs/follow.go:FollowJobWithService(ctx, svc, jobID) error`. Don't add a silent variant: if a caller wants to wait without streaming, they want `--follow=false` plus a manual `runos follow` afterward.

`runos follow <jobId>` is the always-block verb for users who prefer to dispatch then watch.

## Authentication Flow

Firebase Auth supports multiple sign-in methods:
- Google SSO
- GitHub SSO
- Email/password
- 2FA (if enabled)

Store auth tokens securely in `~/.runos/`. Consider implementing API key fallback for CI/CD scenarios.

## Development Guidelines

### Do
- Write idiomatic Go code
- Use Cobra patterns for commands (RunE for error handling, flags, subcommands)
- Keep commands thin; business logic goes in `internal/` packages
- Return structured errors that can be formatted appropriately
- Use context.Context for cancellation and timeouts
- Add brief comments for non-obvious logic only

### Don't
- Don't add unnecessary abstractions
- Don't create interfaces until you have multiple implementations
- Don't add features beyond what's explicitly requested
- Don't add verbose logging unless specifically needed
- Don't hardcode API URLs or credentials

### Security
- Never log or display credentials unless explicitly outputting them
- Secrets can be displayed in plain text when requested at the CLI surface (no confirmation needed). The MCP surface is different: tool output flows into LLM context and may be persisted, so any path that emits env values, secret-file content, or unified diffs of either MUST redact (use `apps.RedactEnvUnifiedDiff`, or pass `--redact-secrets` when shelling to the CLI from an MCP tool).
- Store tokens with appropriate file permissions (0600)
- Validate user input before sending to API
- **Server-supplied identifiers are untrusted on the client.** App IDs, cluster IDs, archive IDs, and similar values returned by conductor must be charset-validated (`apps.ValidateIdentifier` or equivalent) before being joined into `filepath.Join`, used in shell arguments, or otherwise interpreted by the local filesystem.
- HTTP client baseline: every `*http.Client` sets a `Timeout`; every response `defer resp.Body.Close()`; response reads wrap in `io.LimitReader`; server-supplied download URLs validate scheme (no protocol downgrade) and disable redirects (`CheckRedirect: http.ErrUseLastResponse`); pre-check `Content-Length` against the size cap before streaming.
- Tar/zip extraction must reject zip-slip (entries that escape the destination after `filepath.Join`), reject symlinks pointing outside the destination, cap total decompressed size, and open writes with `O_NOFOLLOW` when overwriting on top of existing trees.

### Error Handling
- Use descriptive error messages
- Include context about what operation failed
- Exit with appropriate codes (0 = success, 1 = error)
- Don't panic; return errors up the call stack

### Testing
- See [TESTING.md](TESTING.md) for the testing philosophy, conventions, and the running list of regression tests by fix.
- We test pure functions and extracted helpers. We do **not** write cobra-level subprocess tests. No e2e against a live conductor. Extract a pure helper and test that instead.
- When you fix a bug, add a regression test for the pure logic that prevents it. Append the entry to TESTING.md's "regression tests by fix" table.
- Use `t.Helper()` in test helpers so failures point at the calling test
- Use `t.Cleanup(fn)` over `defer fn()`; helpers can register their own cleanup
- Use `t.TempDir()` for filesystem fixtures, `t.Chdir(dir)` (Go 1.24+) for cwd changes (avoid `defer os.Chdir(prev)`)
- Table-driven tests with `t.Run(tt.name, ...)` for any function with more than two cases
- Tests for security-sensitive validators (charset checks, URL validation, redaction, zip-slip protection) are mandatory, not optional

### Build & Install
After making code changes, always run `make local` to build and install the CLI to `~/.local/bin/runos`.

### Releasing
1. Add a `## vX.Y.Z` section to `CHANGELOG.md` with release notes
2. Commit, tag (`git tag vX.Y.Z`), and push with `--tags`
3. CI builds all platforms, extracts the matching section from `CHANGELOG.md`, and creates the GitHub release with those notes
4. If no changelog entry exists for the version, CI falls back to auto-generated notes from commits
