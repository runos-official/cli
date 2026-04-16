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

## Output Formatting

- **Default**: Plain text, human-readable
- **JSON flag**: Support `--json` or `-j` flag for JSON output (for scripting)
- Keep plain text output concise and scannable

## API Interaction

- Base URL is configurable (stored in config file)
- Long-running operations return a job ID
- Use progress bars when polling job status
- Handle API errors gracefully with clear messages

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
- Secrets can be displayed in plain text when requested (no confirmation needed)
- Store tokens with appropriate file permissions (0600)
- Validate user input before sending to API

### Error Handling
- Use descriptive error messages
- Include context about what operation failed
- Exit with appropriate codes (0 = success, 1 = error)
- Don't panic; return errors up the call stack

### Build & Install
After making code changes, always run `make local` to build and install the CLI to `~/.local/bin/runos`.

### Releasing
1. Add a `## vX.Y.Z` section to `CHANGELOG.md` with release notes
2. Commit, tag (`git tag vX.Y.Z`), and push with `--tags`
3. CI builds all platforms, extracts the matching section from `CHANGELOG.md`, and creates the GitHub release with those notes
4. If no changelog entry exists for the version, CI falls back to auto-generated notes from commits
