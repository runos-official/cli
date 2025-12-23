# RunOS CLI (Clister)

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

The primary backend for this CLI is **Conductor**. When implementing API integrations, use the Conductor MCP server to discover available endpoints and their specifications.

### Conductor MCP Server

An MCP server is available for querying Conductor API documentation. Use it to:
- List available API endpoints by category
- Get detailed endpoint specifications (params, body schema, auth requirements)
- Test API calls during development

**Available MCP Tools:**

| Tool | Description |
|------|-------------|
| `list_api_endpoints` | List all APIs, optionally filter by category (services, apps, jobs, tags, service-info, cli-deployment) |
| `describe_api` | Get details about a specific endpoint (e.g., `describe_api(endpoint="GET /services/:type/:id")`) |
| `call_api` | Execute any API endpoint with method, path, aid, cid, and optional body |
| `health_check` | Check Conductor server health |
| `firestore_get` | Get a Firestore document |
| `firestore_query` | Query Firestore collections |
| `get_template` | Fetch K8s/Helm/script templates |
| `list_templates` | List available templates |
| `upload_cli_deployment` | Upload app tarball for deployment |
| `url_check` | Check if a URL returns 200 |

**Prerequisites:** Conductor must be running locally (`pnpm dev` in the conductor directory) on port 3026.

## Tech Stack

- **Language**: Go
- **CLI Framework**: Cobra
- **Config Location**: `~/runos/`
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

### Project Structure (target)
```
cli/
├── cmd/                 # Cobra commands (one file per command/subcommand)
├── internal/
│   ├── api/             # REST API client
│   ├── auth/            # Firebase authentication
│   ├── config/          # Configuration management
│   ├── output/          # Output formatting (plain text, JSON)
│   └── progress/        # Progress bar for job polling
├── pkg/                 # Reusable packages (if any)
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

Store auth tokens securely in `~/runos/`. Consider implementing API key fallback for CI/CD scenarios.

## Guidelines for Claude

### API Discovery
When implementing or modifying API integrations:
1. **Always use `list_api_endpoints`** first to discover available endpoints (filter by category: services, apps, jobs, tags, service-info, cli-deployment)
2. **Use `describe_api`** to get detailed endpoint specifications before writing integration code (params, body schema, query params, auth requirements, async behavior)
3. Never guess API contracts - always query the Conductor MCP server for accurate, up-to-date information

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
