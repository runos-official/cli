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
