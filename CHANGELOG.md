# Changelog

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
