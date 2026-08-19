# Manifest text changes the CLI fixes make due (2026-08-17)

The CLI is fixed; these are conductor manifest TEXT changes for the manifest agent.
Nothing here blocks the CLI work.

## 1. `requires` no longer lacks a flag (review 2 item 19)

Files: `src/util/cliManifest/` entries for `apps/add`, `apps/update`, `deploy`.
Field: `requires`, description tail.

Current text ends with:

> This field has no `--requires` flag; pass via `-f body.yaml` (object-typed body field).

That is now false: object-typed fields have had a repeatable flag since goal 19 A9, and this
fix makes `--requires` accept a JSON object. Replace that sentence with:

> Pass one JSON object: `--requires '{"db":{"id":"abc12","type":"postgresql"}}'`, or put the
> whole body in a YAML file and pass `-f body.yaml`. `key=value` cannot express it, because
> the values are objects.

## 2. `providerOptions` shape (review 2 item 19)

Files: `src/util/cliManifest/` entries for `domains/add`, `domains/update`.
Field: `providerOptions`.

The CLI now refuses `--provider-options key=value` and names the JSON form, because the
values are booleans and numbers as well as strings. Worth stating in the description:

> Pass one JSON object, e.g. `--provider-options '{"proxied":true}'`.

Better still: declare `valueType` on both fields. The CLI reads `valueType` first and the
hardcoded carve-out in `internal/dynacmd/object_flag.go` and
`internal/mcp/server.go:projectObjectValue` retires the moment it appears.

## 3. `requires` valueType / valueFields

Same fields. The MCP schema for `requires` is still a hardcoded fallback in the CLI
(`projectObjectValue`, I26-N). Declaring `valueType: "object"` plus `valueFields`
(`id`, `type`, `config`, `env`) removes it.
