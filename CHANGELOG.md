# Changelog

## v0.3.7

### Bug fixes
- **Fix MCP path parameter substitution** — AI models sending prefixed field names (e.g. `app_id` instead of `id`) now resolve correctly by deriving the expected prefix from the URL path entity
- **Fix boolean type mapping in MCP tool schema** — boolean manifest fields (like `enabled` on overrides) were incorrectly exposed as `string`, causing API rejections

### Improvements
- **MCP CLI version check** — the `cli_version-check` tool now auto-injects the current CLI version and OS, allowing AI assistants to check for updates without needing to know the version

## v0.3.6

### Bug fixes
- **Fix MCP path parameter substitution when AI prefixes field names** — resolves issue where literal `:id` appeared in API URLs instead of actual resource IDs
