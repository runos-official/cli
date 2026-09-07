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

## 5. The advanced-config mapper flattens every field to `string` (objective 92 / story 212)

File: `src/util/cliManifest/advancedConfigFields.ts`.
Commands affected: every `set-advanced-configs`, `set-router-config`,
`set-lmcache-engine-config` and `set-lmcache-server-config` entry.

The mapper declares `type: 'string'` for every advanced-config field, with no branch on the
catalog's own type. The catalog already carries the answer and the manifest throws it away.
Measured on manifest 45.5.0 against `/service-info/<type>/advanced-config-schema`:

| catalog | fields | `inputType: number` | `toggle` | `valueShape: json` | declared `string` in the manifest |
|---|---|---|---|---|---|
| `vllm-router` | 25 | 19 | 2 | 0 | 25 |
| `lmcache-server` | 34 | 17 | 5 | 2 | 34 |

Three costs, in the order they bite an operator:

1. `--max-concurrent-requests abc` is accepted by the CLI, because a `string` field registers
   a string flag that takes anything. Declared `integer` it would be refused at the flag, with
   no round trip. (What conductor then does with the bad value is conductor's business; the
   point is that the CLI cannot refuse it even though the catalog knows the type.)
2. `--disable-retries` needs an explicit value instead of being a switch.
3. A json-shaped field gets ONE opaque string flag instead of the repeatable
   `--flag key=value` map form the CLI has had since goal 19 A9. Two fields ship in this
   state today, both on `lmcache-server`: `l2_adapters` and `runtime_plugin_config`. The
   vLLM ENGINE's `extra-env` on `set-advanced-configs` is the same case, and objective 92's
   story 205 adds the router's `extra_env` to it.

THE CHANGE. Make the mapper type-aware, deriving the manifest type from what the catalog
already declares:

> `valueShape: 'json'`  ->  `type: 'object'`
> `type: 'integer'`     ->  `type: 'integer'`
> everything else       ->  `type: 'string'` (unchanged)

`type: 'object'` alone is what the CLI needs for the map flag. For a map whose VALUES are
not strings the CLI reads `valueType` first (`internal/manifest/types.go`), so declare
`valueType` only where the values genuinely are not strings — `extra_env`'s are strings, so
it needs `object` and nothing more.

SCOPE AND COST, agreed with the Conductor implementer on objective 92. This is a manifest
contract change on existing commands, so it is a MAJOR manifest bump and it needs its own
story or change request. It is deliberately NOT part of objective 92, and nothing in that
objective waits on it: `extra_env` is REACHABLE today as a JSON string
(`--extra-env '{"HF_HUB_OFFLINE":"1"}'` reaches conductor's `parseRouterExtraEnv` and
works), so this is typing and ergonomics, not unreachability. When it is done it must cover
the engine's kebab-case `extra-env` and the router's snake_case `extra_env` in one change,
or the two surfaces disagree about the same concept.

No CLI carve-out was added for any of this, and the CLI needs no change: its flag-registration
type switch already has `integer`, `object`, `array` and `boolean` arms beside `string`
(`internal/dynacmd/builder.go`), and the object flag already reads `ValueType` to decide whether
to refuse `key=value` (`internal/dynacmd/object_flag.go`). Each of those arms starts working the
moment the manifest declares the type.
