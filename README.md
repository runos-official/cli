# RunOS CLI

[![CI](https://github.com/runos-official/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/runos-official/cli/actions/workflows/ci.yml)
[![Release](https://github.com/runos-official/cli/actions/workflows/release.yml/badge.svg)](https://github.com/runos-official/cli/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/runos-official/cli)](https://goreportcard.com/report/github.com/runos-official/cli)

Command-line interface for [RunOS](https://runos.com) -- a self-hosted cloud platform where you bring your own hardware to run cloud infrastructure across multiple providers.

This CLI communicates with the same REST API used by the RunOS web console, giving you full control over your clusters, services, and deployments from the terminal.

## Features

- **Cluster management** -- create, list, and manage Kubernetes clusters
- **Service provisioning** -- managed services (PostgreSQL, Valkey, MySQL, etc.)
- **Application deployment** -- deploy from local source (`runos deploy`) or from a linked GitHub/GitLab integration at a specific commit (VCS deploys)
- **Apps + services as IaC** -- `apps pull` / `apps diff` / `apps sync` and the matching `services` triplet round-trip cluster state to/from yaml on disk for git-versioned config
- **Job tracking** -- follow long-running operations with real-time progress and per-step build logs
- **Headless auth** -- personal access tokens for CI/CD via `RUNOS_API_KEY` (no interactive login required)
- **MCP integration** -- Model Context Protocol server for AI coding assistants (Claude Code, Cursor, Gemini CLI, OpenCode, OpenAI Codex)
- **Dynamic commands** -- most CLI commands auto-update from the API manifest, so new server-side features appear without a CLI upgrade

## Installation

### Install Script

**macOS / Linux:**

```bash
curl -fsSL https://get.beta.runos.com/cli.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://get.beta.runos.com/cli.ps1 | iex
```

### From Source

```bash
git clone https://github.com/runos-official/cli.git
cd cli
make local
```

Requires Go 1.25+. The binary is installed to `~/.local/bin/runos`.

## Quick Start

```bash
# Log in via browser (also picks up the default environment on first run)
runos login

# List your clusters
runos clusters list

# Deploy an app from the current directory
runos deploy
```

The first run auto-fetches the default environment from the RunOS CDN, so there's no manual environment-switch step. Use `runos config env <name>` only when you want to point the CLI at a different environment.

## Authentication

Two paths, depending on whether the CLI runs interactively or headless:

### Interactive (laptop)

```bash
runos login
```

Opens a browser to complete sign-in (Google, GitHub, or email/password, with optional 2FA). Tokens are stored under `~/.runos/` with `0600` permissions.

### Headless (CI / scripts)

Generate a personal access token in the console under Account → API Keys, or via `runos account api-keys add`. Then export the token plus your account ID:

```bash
export RUNOS_API_KEY=...
export RUNOS_ACCOUNT_ID=...
# Optional: pin a non-default environment
export RUNOS_API_URL=https://api.your-env.runos.com
```

When `RUNOS_API_KEY` is set, the CLI bypasses Firebase auth entirely. No `~/.runos/config.json` is required, and no interactive `runos login` is needed.

## Configuration

Configuration is stored in `~/.runos/config.json`.

### Environment Variables

| Variable | Description |
|---|---|
| `RUNOS_API_KEY` | Personal access token for headless auth |
| `RUNOS_ACCOUNT_ID` | Account ID (required alongside `RUNOS_API_KEY`) |
| `RUNOS_API_URL` | Override the API endpoint |
| `CONSOLE_URL` | Override the web console URL |
| `RUNOS_CLUSTER_ID` | Default cluster ID for cluster-scoped commands |

### Custom URLs

For local development or custom endpoints:

```bash
runos config set api-url http://localhost:3025
runos config set console-url http://localhost:5177
```

## Deployment

`runos deploy` dispatches on the app's deploy type:

- **CLI deploy** (`deployType: cli`): the CLI tarballs your local source (respecting `.dockerignore`), uploads it, and tracks the build/deploy job to completion.
- **VCS deploy** (`deployType: vcs`): the CLI sends `{sha, configPath}` only; the cluster pulls source from your linked GitHub/GitLab integration at the SHA, builds in-cluster, and rolls out. SHA defaults to `git rev-parse HEAD`; pass `--sha` to pin and `--allow-dirty` to waive the dirty-tree refusal. Use `--app <id> --sha <sha>` for CI mode (no local yaml on disk).

A minimal `runos.yaml`:

```yaml
app: my-app
port: 3000
requires:
    my-app-db:
        type: postgresql
        class: postgresql.c0.beff
        config:
            databaseName: myapp
            databaseUsername: myapp
        env:
            url: DATABASE_URL
```

### Environment variables

Two parallel files, both flow into the running pod via `envFrom`:

| File | Purpose | Permissions | VCS |
|------|---------|-------------|-----|
| `.runos.<cid>.<id>.env` | Sensitive credentials (Secret-backed) | 0600 | gitignored |
| `runos.<cid>.<id>.config.env` | Plain config (ConfigMap-backed; log level, feature flags, public URLs) | 0644 | committed |

App code reads them identically as `process.env.X`. A key may not appear in both files at once; the API refuses the deploy if it does.

## Apps and services as IaC

The CLI round-trips an app's full configuration (yaml, env vars, secret files, manifest overrides) and any service the app depends on between cluster and disk, so cluster state can live in version control.

```bash
# Pull cluster state to disk (writes runos.<cid>.<id>.yaml + env files + linked services)
runos apps pull <yaml>           # one app
runos apps pull --all            # every app in the cluster

# Show drift between local files and cluster
runos apps diff <yaml>           # exit 0 = in sync, exit 2 = drift, anything else = real failure

# Push local files to the cluster
runos apps sync <yaml>           # interactive; --dry-run to plan, --yes to skip prompt
```

The `runos services` triplet (`pull` / `diff` / `sync`) follows the same shape for managed services. The yaml is the source of truth for cluster id, so once committed, CI loops over yamls don't need a default cluster set or `--cid` per call.

For CI workflows, see `runos apps sync --dry-run` (drift detection) and `runos apps diff --redact-secrets` (secret-safe diff output).

## MCP Integration

The CLI includes a built-in [Model Context Protocol](https://modelcontextprotocol.io) server that exposes RunOS operations as tools for AI coding assistants.

Four permission levels control what operations are available:

| Level | Risk | Description |
|---|---|---|
| `read` | Low | Operations that change nothing (list clusters, apps, services) |
| `sensitive-read` | **High** | Read operations that return credentials and connection strings -- these will be visible to the AI model |
| `write` | **High** | Create, update, and delete operations that modify live infrastructure |
| `sensitive-write` | **Critical** | Credential rotation and secret management on live infrastructure |

> **Security note:** Choose the minimum permission level you need. The `sensitive-read` and above levels expose secrets to the AI assistant's context. Only use these in trusted environments.

> **What `read` does and does not promise.** The `read` server performs no mutation, and that is the whole of the promise. It is not free of secrets. On a CLI older than manifest 45.0.0, the `grafana`, `litellm`, `langfuse`, `vector` and `clickhouse` credentials commands sit on the `read` tier and return real passwords. Manifest 45.0.0 moves those five to `sensitive-read`. Run `runos manifest update` and check `runos cli version-check` before you treat the read server as secret-free. The `prometheus`, `traefik` and `netbird-server` credentials commands stay on `read` in every version, because they return only host, port and URL fields.

### Configure for your AI assistant

```bash
# Claude Code
runos mcp configure claude

# Cursor
runos mcp configure cursor

# Gemini CLI
runos mcp configure gemini

# OpenCode
runos mcp configure opencode

# OpenAI Codex
runos mcp configure codex
```

Every target writes into the current directory, so the RunOS tools are scoped to that
project. Re-running a target is safe: `cursor` brings the project back to the state it
should be in and reports what it changed, and the other four leave a configured project
alone.

### Cursor

`runos mcp configure cursor` writes three files:

| File | What it does |
|---|---|
| `.cursor/mcp.json` | Declares the four RunOS servers as stdio servers |
| `.cursor/hooks.json` | Registers the guard as a `beforeMCPExecution` hook, at `version: 1`, with `failClosed: true` |
| `.cursor/hooks/runos-guard.sh` | Answers `ask` for the three servers that carry risk, and `allow` for everything else |

The command converges on all three every run. Delete the guard, break the hook
registration, or clone a project whose committed `.cursor/mcp.json` points at another
machine's binary, and the next run repairs it and says what it repaired. A run that finds
everything in place changes nothing and says so.

**The guard decides on the MCP server name, never on a tool list.** Tools move between
servers when their risk changes, so a tool list goes stale where a server name does not.
The script parses nothing itself: it pipes Cursor's payload to `runos mcp cursor-guard`,
which reads it with a real JSON parser. That matters because `tool_input` is written by
the model and RunOS write tools take free-form string maps, so a text scan for
`mcp_server_name` cannot tell the real top-level key from a decoy nested inside
`tool_input`.

Three outcomes:

| The payload names | The guard answers | Why |
|---|---|---|
| `runos-sensitive-read`, `runos-write` or `runos-sensitive-write` | `ask` | These change or reveal something |
| `runos`, or any server the guard does not know | `allow` | The hook fires for every MCP server in the project and must not block another tool's |
| nothing the guard can read | `ask` | A guard that cannot read its own payload has to be loud, not silent |

**A broken guard blocks, it does not allow.** Cursor lets a hook that crashes, times out
or prints invalid JSON allow the action through. The guard is registered `failClosed`, so
those cases block instead. The cost is real: while the guard is broken, every MCP call in
that project is blocked, including another tool's servers. Re-run
`runos mcp configure cursor` to restore it.

**All four servers load.** Cursor has no per-server switch in `mcp.json`. Open Customize
in the sidebar and switch off `runos-sensitive-read`, `runos-write` and
`runos-sensitive-write`, or run `runos mcp configure cursor --read-only` to declare the
read server alone. `--read-only` also removes the other three if an earlier run declared
them.

**The guard is a Cursor editor feature.** A client that does not read `.cursor/hooks.json`
runs no guard at all. `cursor-agent` 2025.09.17 is such a client: its bundle contains no
`beforeMCPExecution`, no `hooks.json` and no `mcp_server_name`. There the approvals prompt
is the only brake, and `--read-only` is the only way to keep the write servers from
loading.

**Windows.** The guard is a bash script, so `runos mcp configure cursor` refuses to run on
Windows and names the reason. `--read-only` works there, because a project that declares
only the read server needs no guard.

The command asks for a typed confirmation before it writes, having first listed what each
server can do. `--yes` skips that. `--read-only` never asks, because it declares nothing
that can change anything.

An existing `.cursor/mcp.json` or `.cursor/hooks.json` is merged, not replaced: servers
and hooks another tool put there survive. A file that does not hold a JSON object is
named in the error and left on disk, and nothing else is written either. The one
exception is the JSON literal `null`, which is read as an empty object.

The guard is appended to `beforeMCPExecution` rather than replacing it. That is safe
because Cursor documents that "all matching hooks from every source run". Cursor does
not document how it combines two hooks that return different decisions, so if you
register a second `beforeMCPExecution` hook, test the pair before relying on either.

### How tools are exposed

Tools surface to MCP clients via two paths:

1. **Manifest-driven (the default).** Most tools come from the RunOS API CLI manifest at `~/.runos/manifest.json`. Each command entry maps directly to an HTTP endpoint, so adding a new API on the server side automatically produces a new MCP tool the next time `runos manifest update` runs. Categorisation (`read` / `sensitive-read` / `write` / `sensitive-write`) is set per command via the manifest's `mcp` field.

2. **Custom handlers.** A small set of tools need orchestration that a single API call can't express -- e.g. `deploy` (tar + upload + poll), and `apps_pull` / `apps_diff` / `apps_sync` / `apps_list_previous_uploads` (which combine API calls with local filesystem work). These live in [internal/mcp/](internal/mcp/) and run as `runos` subprocesses so behaviour stays in lockstep with the CLI. To add one: register a `Tool` descriptor in `buildTools()` (or a helper called from it) and dispatch by name in `handleToolsCall()`.

If a feature can be a single endpoint, prefer adding it to the manifest. Reach for a custom handler only when the tool needs to drive multiple endpoints, touch the local filesystem, or run a long subprocess.

## Development

### Prerequisites

- Go 1.25+
- A RunOS account with access to a cluster

### Building

```bash
# Install the tracked git hooks (run once, right after you clone)
make hooks

# Build and install locally
make local

# Build all platforms
make build

# Run tests
make test
```

### Git hooks

Run `make hooks` once per clone. It points `core.hooksPath` at the tracked
`.githooks/` directory. `.git/hooks` is not tracked, so a hook that is not
installed is a hook nobody has. The `pre-commit` hook runs the leak gate over
your staged diff and blocks a commit that would publish a credential or a new
internal identifier.

### Public repo: no credentials, no internal identifiers

This repo is public. Never commit credentials, tokens, API keys or private keys;
real account, cluster or app IDs; org or customer names; internal hostnames; or
pasted terminal output that carries a real address.

Use placeholders in examples and fixtures. For addresses, use the ranges that
exist for exactly that purpose: `192.0.2.0/24`, `198.51.100.0/24` and
`203.0.113.0/24` (RFC 5737), and `2001:db8::/32` (RFC 3849).

`scripts/leakcheck.py` enforces this. It runs in three places: the `pre-commit`
hook (staged diff only, fast), `make leakcheck` (on demand), and
`scripts/release.sh` (whole tree, and it cannot be skipped).

```bash
make leakcheck          # scan every tracked file
make leakcheck-staged   # scan only what is staged
make leakcheck-test     # test the checker itself
make leakcheck-update   # ratchet the baseline down after removing an identifier
```

It has two severities.

- **Credentials** hard fail, always. They can never be baselined.
- **Internal identifiers** are ratcheted. `scripts/leakcheck.baseline` records
  what this repo has already published, so existing work is not blocked. A NEW
  identifier fails the gate.

An internal identifier is a machine name or account id listed in
`scripts/leakcheck.config`, or any IP address literal outside the documentation,
loopback, link-local, unspecified, broadcast and well-known multicast ranges.
Addresses are allow-listed rather than deny-listed, because you cannot tell a
real address from an invented one by reading it. A project constant such as a
service CIDR is absorbed into the baseline once and never asked about again.

**Do not hand-add a line to `scripts/leakcheck.baseline` to get a commit
through.** A line in that file records a leak that already shipped, it is not a
licence to add another. Remove the identifier from the source, then run
`make leakcheck-update` so the baseline shrinks.

The pre-commit hook can be skipped in a genuine emergency with
`git commit --no-verify`, and it says so when it fires. Skipping does not get
the change released: the release gate runs the same checker over the whole tree.

### Project Structure

```
cli/
├── .githooks/              # Tracked git hooks (install with `make hooks`)
├── cmd/                    # Cobra command definitions
├── scripts/                # release.sh and the public-repo leak gate
├── internal/
│   ├── api/                # HTTP client for the RunOS API
│   ├── apps/               # apps pull / diff / sync (apps as IaC)
│   ├── auth/               # Firebase + PAT authentication
│   ├── cache/              # File-based TTL cache
│   ├── config/             # Configuration management
│   ├── deploy/             # Deployment pipeline (archive, upload)
│   ├── dynacmd/            # Dynamic command builder from API manifest
│   ├── git/                # Thin git wrapper for VCS-deploy SHA resolution
│   ├── jobs/               # Job polling and progress display
│   ├── manifest/           # CLI manifest loading and parsing
│   ├── mcp/                # MCP server implementation
│   ├── output/             # Response formatting (text, JSON)
│   ├── services/           # services pull / diff / sync (services as IaC)
│   └── update/             # CLI self-update mechanism
└── version/                # Version variable (set via ldflags)
```

### Releasing

1. Add a `## vX.Y.Z` section to `CHANGELOG.md` with release notes
2. Commit, tag (`git tag vX.Y.Z`), and push with `--tags`
3. CI builds all platforms, extracts the matching changelog section, and creates the GitHub release with those notes
4. If no changelog entry exists for the version, CI falls back to auto-generated notes from commits

### How Dynamic Commands Work

The CLI fetches a manifest from the API that defines available commands, their flags, and endpoint mappings. This means most commands are generated at runtime -- when the API adds new endpoints, the CLI picks them up automatically on the next `runos manifest update` (or when the cached manifest expires after 1 hour).

## License

The RunOS CLI is **source-available** under the [Elastic License 2.0](LICENSE):
the source is published for transparency and security review, not as open
source. Use is subject to the license terms. See [LICENSE](LICENSE) and
[NOTICE](NOTICE). Copyright 2026 RunOS.
