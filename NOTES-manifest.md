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

## 4. `warnings` is a reserved response key now (objective 92 / story 211)

Files: every `src/util/cliManifest/` entry that declares an output field named `warnings`.
As of manifest 45.5.0 that is `apps/add`, `apps/update`, `services/umami/add`,
`services/umami/{id}/update`, `nodes/configure-gpu-shape`, `storage-groups/delete` and
`deploy`.

The CLI now treats a top-level `warnings` key on a SUCCESSFUL response as a conductor
advisory, not as data: it prints one `Warning: <entry>` line per entry on stderr and
suppresses the key from the plain-text table (`internal/dynacmd/warnings.go`). `--json` and
the MCP path are unaffected — the key is still in the body they return.

Two consequences for the manifest:

1. Declaring `warnings` on a command's output no longer buys a table row. It is harmless to
   leave the declaration in place (the CLI skips a declared-but-absent field), and it is
   still the honest schema for MCP and `--json`, so nothing needs changing today.
2. Do NOT introduce a top-level `warnings` field that means something OTHER than "advisory
   strings for the caller to read". The renderer is keyed on shape, not on the command, so a
   `warnings` array of strings anywhere will be rendered as advisories. A non-string array is
   left alone (printed as nothing, key kept), which is the only escape hatch.

The singular `warning` string is untouched and stays a per-command data field (the one-shot
token banner on `account/api-keys/add` and `account/notify-keys/add` reads it).
