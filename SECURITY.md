# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in any RunOS product or service, including the CLI, API, web console, or infrastructure, please report it responsibly.

**Email:** security@runos.com

Please include:
- A description of the vulnerability
- Steps to reproduce
- Potential impact

We will acknowledge reports within 8 hours and aim to provide a fix or mitigation plan within 3 days for critical issues.

## Security Practices

- **No hardcoded secrets.** Credentials are loaded at runtime from user config (`~/.runos/config.json`) or environment variables.
- **Strict file permissions.** Config and cache files are created with `0600` (owner read/write only). Directories use `0700`.
- **Binary verification.** Self-updates verify SHA256 checksums against the release manifest before installing.
- **Short-lived tokens.** Authentication uses short-lived Firebase ID tokens refreshed from a locally stored refresh token. Tokens are never logged.
- **HTTPS only.** All API communication uses HTTPS.

## MCP Server Permissions

The CLI includes an MCP server for AI assistant integration with four permission levels:

| Level | Risk | Description |
|---|---|---|
| `read` | Low | Query-only access to clusters, services, and apps |
| `sensitive-read` | High | Returns credentials, connection strings, and secrets (visible to the AI model) |
| `write` | High | Create, update, and delete operations on live infrastructure |
| `sensitive-write` | Critical | Credential rotation and secret management |

Each level must be explicitly configured. Use the minimum level required for your workflow.
