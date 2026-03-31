# RunOS CLI

[![CI](https://github.com/runos-official/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/runos-official/cli/actions/workflows/ci.yml)
[![Release](https://github.com/runos-official/cli/actions/workflows/release.yml/badge.svg)](https://github.com/runos-official/cli/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/runos-official/cli)](https://goreportcard.com/report/github.com/runos-official/cli)

Command-line interface for [RunOS](https://runos.com) -- a self-hosted cloud platform where you bring your own hardware to run cloud infrastructure across multiple providers.

This CLI communicates with the same REST API used by the RunOS web console, giving you full control over your clusters, services, and deployments from the terminal.

## Features

- **Cluster management** -- create, list, and manage Kubernetes clusters
- **Service provisioning** -- deploy managed services (PostgreSQL, Valkey, MinIO, etc.)
- **Application deployment** -- deploy apps from local source with `runos deploy`
- **Job tracking** -- follow long-running operations with real-time progress
- **MCP integration** -- Model Context Protocol server for AI coding assistants (Claude Code, Roo Code, Gemini CLI, OpenCode)
- **Dynamic commands** -- CLI commands auto-update from the API manifest, so new features appear without upgrading

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
# Set the environment
runos config env beta

# Log in via browser
runos login

# List your clusters
runos clusters list

# Deploy an app
runos deploy
```

## Configuration

Configuration is stored in `~/.runos/config.json`.

### Environment Variables

| Variable | Description |
|---|---|
| `CONDUCTOR_API_URL` | Override the API endpoint |
| `CONSOLE_URL` | Override the web console URL |
| `RUNOS_CLUSTER_ID` | Set the default cluster ID |

### Custom URLs

For local development or custom API endpoints:

```bash
runos config set conductor-url http://localhost:3025
runos config set console-url http://localhost:5177
```

## Deployment

The `runos deploy` command deploys applications from a `runos.yaml` configuration file in your project root:

```yaml
app: my-app
port: 8080
requirements:
  cpu: "0.25"
  memory: 256Mi
  replicas: 1
services:
  - type: postgres
```

The CLI creates a tarball of your source code (respecting `.dockerignore`), uploads it to your cluster, and tracks the build/deploy job to completion.

Environment variables can be defined in `.runos.<cluster-id>.env` files.

## MCP Integration

The CLI includes a built-in [Model Context Protocol](https://modelcontextprotocol.io) server that exposes RunOS operations as tools for AI coding assistants.

Four permission levels control what operations are available:

| Level | Risk | Description |
|---|---|---|
| `read` | Low | Query-only operations (list clusters, apps, services) |
| `sensitive-read` | **High** | Read operations that return credentials and connection strings -- these will be visible to the AI model |
| `write` | **High** | Create, update, and delete operations that modify live infrastructure |
| `sensitive-write` | **Critical** | Credential rotation and secret management on live infrastructure |

> **Security note:** Choose the minimum permission level you need. The `sensitive-read` and above levels expose secrets to the AI assistant's context. Only use these in trusted environments.

### Configure for your AI assistant

```bash
# Claude Code
runos mcp configure claude

# Roo Code
runos mcp configure roo

# Gemini CLI
runos mcp configure gemini

# OpenCode
runos mcp configure opencode
```

## Development

### Prerequisites

- Go 1.25+
- A RunOS account with access to a cluster

### Building

```bash
# Build and install locally
make local

# Build all platforms
make build

# Run tests
make test
```

### Project Structure

```
cli/
├── cmd/                    # Cobra command definitions
├── internal/
│   ├── api/                # HTTP client for the Conductor API
│   ├── auth/               # Firebase authentication
│   ├── cache/              # File-based TTL cache
│   ├── config/             # Configuration management
│   ├── deploy/             # Deployment pipeline (archive, upload)
│   ├── dynacmd/            # Dynamic command builder from API manifest
│   ├── jobs/               # Job polling and progress display
│   ├── manifest/           # CLI manifest loading and parsing
│   ├── mcp/                # MCP server implementation
│   ├── output/             # Response formatting (text, JSON)
│   └── update/             # CLI self-update mechanism
└── version/                # Version variable (set via ldflags)
```

### How Dynamic Commands Work

The CLI fetches a manifest from the API that defines available commands, their flags, and endpoint mappings. This means most commands are generated at runtime -- when the API adds new endpoints, the CLI picks them up automatically on the next `runos manifest update` (or when the cached manifest expires after 1 hour).
