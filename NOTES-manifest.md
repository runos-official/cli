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

The mapper declares `type: 'string'` for every advanced-config field, with no branch on what
the catalog says the value is. The catalog already carries the answer and the manifest throws
it away.

MEASURED on conductor `9f626657` (the post-change build this story bounds), from the
`advancedConfigSchemas` block of `testdata/vllm/manifest-post-45.6.0.json`. The commit is the
label, not the manifest version: `45.5.0` names two different manifests, which is why
`testdata/vllm/PROVENANCE.md` keys off the commit.

| catalog | fields | `inputType: number` | `inputType: toggle` | `valueShape: json` | declared `string` in the manifest |
|---|---|---|---|---|---|
| `vllm-router` | 43 | 36 | 2 | 1 | 43 |
| `lmcache-server` | 34 | 17 | 5 | 2 | 34 |

Three costs, in the order they bite an operator:

1. `--max-concurrent-requests abc` is accepted by the CLI, because a `string` field registers
   a string flag that takes anything. Declared `integer` it would be refused at the flag, with
   no round trip. (What conductor then does with the bad value is conductor's business; the
   point is that the CLI cannot refuse it even though the catalog knows the type.)
2. `--disable-retries` needs an explicit value instead of being a switch. Seven keys are
   affected: `disable_retries` and `disable_circuit_breaker` on `vllm-router`, and
   `l1_use_lazy`, `disable_observability`, `disable_metrics`, `disable_logging` and
   `enable_tracing` on `lmcache-server`.
3. A json-shaped field gets ONE opaque string flag instead of the repeatable
   `--flag key=value` map form the CLI has had since goal 19 A9. THREE fields are in this
   state on the post-change build: `extra_env` on `vllm-router`, and `l2_adapters` and
   `runtime_plugin_config` on `lmcache-server`. The vLLM ENGINE's kebab-case `extra-env` on
   `set-advanced-configs` is the same case and shipped that way before objective 92.

THIS IS A CHANGE DUE, NOT A CHANGE TO APPLY ON ITS OWN. Retyping these fields is only correct
if the REQUEST BODY CONTRACT and the UNSET MECHANISM move in the same change. Neither of those
is in scope for this story, and neither is designed here. Both blockers are measured below,
against the checked-in snapshot and the CLI as it stands. Do the whole thing or do not start:
a mapper-only change breaks every field it retypes.

BLOCKER 1, THE REQUEST BODY. The `set-*-config` family takes a string-to-string record. The
CLI serialises each field by its MANIFEST type (the `collectInput` type switch in
`internal/dynacmd/executor.go`), so retyping changes the value ON THE WIRE, not just the flag.
Measured against a recording stub, one field declared each way:

    type: 'string'    --max-concurrent-requests 128   ->  {"max_concurrent_requests":"128"}
    type: 'integer'   --max-concurrent-requests 128   ->  {"max_concurrent_requests":128}
    type: 'boolean'   --disable-retries true          ->  {"disable_retries":true}

So the endpoint has to start accepting the typed value in the same change, or every retyped
field sends a JSON number or boolean where the handler expects a string.

BLOCKER 2, THE UNSET PATH. Every one of the 77 catalog fields on this build declares
`unsetBy: 'empty-string'`, and that is how an operator clears a value. The CLI has no concept
of `unsetBy` — nothing in this repo reads it — so the empty string simply travels as the
value, and a typed flag refuses it before it ever reaches the wire. Measured on the flag the
real builder registers:

    type: 'string'    --cache-threshold ""   ->  accepted, sends {"cache_threshold":""}
    type: 'integer'   --cache-threshold ""   ->  refused: strconv.ParseInt: parsing "": invalid syntax
    type: 'boolean'   --disable-retries ""   ->  refused: strconv.ParseBool: parsing "": invalid syntax

So retyping removes the ONLY way to unset a field, for 53 of the 77 (the 46 integral numbers
and the 7 toggles that the mapping below retypes `integer` and `boolean`); the 21 that stay
`string` and the 3 that become `object` keep it. A replacement unset path has to land in the
same change, and it is a two-repository design: conductor names the mechanism, and the CLI
most likely needs a flag for it. Nothing here designs it, and it must not be improvised by
whoever picks up the mapper.

THE TYPE MAPPING A COMPLETE CHANGE WOULD USE, recorded so it does not have to be re-derived.
It is not a licence to apply it alone; read the two blockers above first:

> `valueShape: 'json'`                       ->  `type: 'object'`
> `inputType: 'toggle'`                      ->  `type: 'boolean'`
> `inputType: 'number'`, integral `step`     ->  `type: 'integer'`
> `inputType: 'number'`, fractional `step`   ->  `type: 'string'` (unchanged)
> everything else                            ->  `type: 'string'` (unchanged)

THE NUMBER ARM IS CONDITIONAL, AND MUST STAY THAT WAY. `inputType: 'number'` is a UI
numeric-input hint, and it covers FRACTIONAL values; the catalog says which through `step`.
The CLI's `integer` type registers a `ParseInt` flag and there is no float manifest type on
either side, so retyping a fractional field `integer` REGRESSES it from reachable to
unreachable: measured against the checked-in snapshot, `cache_threshold` declared `integer`
gives a pflag `int` that refuses `--cache-threshold 0.3` with
`strconv.ParseInt: parsing "0.3": invalid syntax`, while the `string` it has today accepts
that value and sends it. Four of the seven have `min: 0, max: 1`, so an integer flag would
leave an operator nothing but 0 and 1 for a field whose own suggested default is 0.3 or 0.8.

The seven fractional keys on this build, all `step: 0.01`, named the way cost 2 names the
toggles so the carve-out is visible rather than inferred:

> `vllm-router`: `cache_threshold` (default 0.3), `balance_rel_threshold` (1.5),
> `retry_backoff_multiplier` (1.5), `retry_jitter_factor` (0.2)
> `lmcache-server`: `eviction_trigger_watermark` (0.8), `eviction_ratio` (0.2), `l1_size_gb`

THE RESIDUAL, STATED. Those seven stay `string` and stay unvalidated at the flag. Closing
that needs a float type on BOTH sides — a manifest type conductor emits and a matching arm in
the CLI's registration switch — which is a separate and smaller change due than this one. Do
not fold it in by widening the integer arm.

Those are the property names the PUBLISHED schema carries, and they are the ones checkable
against the snapshot beside this file: the full key union of every catalog field there is
`category, delivery, inputType, isAdvanced, key, longDescription, max, min, openOptions,
options, requiresRestart, shortDescription, step, suggestedDefault, title, unsetBy,
valueShape`. There is no `type` property on a published field. The Conductor implementer
states that the catalog SOURCE carries its own `type: 'integer'` and `type: 'json'` at the
mapper's input; if so those are the same two signals under different names, and either
spelling produces the mapping above. Branch on whichever the mapper actually receives.

WHAT EACH JSON-SHAPED KEY NEEDS BESIDE `object`. The CLI reads `valueType` to decide whether
`key=value` can express the map (`internal/dynacmd/object_flag.go`): `valueType: 'string'`,
or no `valueType` at all, accepts `key=value`; any other `valueType` refuses it and names the
shape with a copyable JSON example.

- `extra_env` (both surfaces): values are environment-variable values, so they ARE strings.
  `type: 'object'` alone is enough; no `valueType` is needed.
- `l2_adapters` and `runtime_plugin_config`: free-form JSON whose values are NOT plain
  strings, so each needs a non-string `valueType` (`object` unless conductor knows better) so
  the CLI refuses `key=value` and names the JSON form instead of inventing a map of strings.
  This story did not determine their exact value shape: the catalog publishes only
  `valueShape: json`, and neither key was exercised against a live service. Conductor should
  state the shape rather than inherit this guess.

SCOPE AND COST, agreed with the Conductor implementer on objective 92. This is a manifest
contract change on existing commands, so it is a MAJOR manifest bump and it needs its own
story or change request. It is deliberately NOT part of objective 92, and nothing in that
objective waits on it: `extra_env` is REACHABLE today as a JSON string
(`--extra-env '{"HF_HUB_OFFLINE":"1"}'` reaches conductor's `parseRouterExtraEnv` and
works), so this is typing and ergonomics, not unreachability. When it is done it must cover
the engine's kebab-case `extra-env` and the router's snake_case `extra_env` in one change,
or the two surfaces disagree about the same concept.

WHAT IS STILL UNFIXED WHEN STORY 212 LANDS, said plainly so it is not lost. The command line
still accepts a nonsense value for every field the catalog calls numeric or boolean:
`--max-concurrent-requests abc` and `--disable-retries maybe` are taken, sent, and refused
later at the service, so the operator learns at the far end of a round trip instead of at the
flag. The seven fractional keys stay unvalidated even after the change above, because there is
no float type on either side. This story deliberately fixes none of that: it bounds and proves
REACHABILITY, and better validation is the larger cross-repository change recorded here.

NO CLI CARVE-OUT WAS ADDED, and the CLI's FLAG side needs no change for the mapping above: the
flag-registration type switch already has `integer`, `object`, `array` and `boolean` arms
beside `string` (`internal/dynacmd/builder.go`), and the object flag already reads `ValueType`
to decide whether to refuse `key=value` (`internal/dynacmd/object_flag.go`). Each of those arms
starts working the moment the manifest declares the type. That is the whole of the earlier
claim that "the CLI needs no change", and it was too broad: the flag needs none, the BODY the
CLI sends changes shape (blocker 1), and a replacement for the empty-string unset (blocker 2)
would need CLI work that does not exist yet.

THIS RECORD IS CHECKED, not just written. Six tests in
`internal/dynacmd/vllm_fields_test.go` hold it to the checked-in snapshot and to the CLI's
real behaviour:

- `TestNotesSection5MatchesTheSnapshot` derives the census and the toggle and json-shaped key
  names from the snapshot and compares them here, so regenerating the artefact against a later
  build fails a test rather than silently dating this record.
- `TestNotesSection5PrescribesPropertiesTheCatalogCarries` parses the mapping above and
  refuses a LEFT-hand property no field in the snapshot carries, so an arm that would match
  nothing cannot be written here unnoticed.
- `TestNotesSection5PrescriptionAcceptsEveryFieldsOwnDefault` applies the mapping to every
  catalog field and asserts the flag it produces accepts that field's own `suggestedDefault`,
  and its `step` where that is fractional. This is the RIGHT-hand check: an arm that fires and
  produces a type the field's own values cannot pass through is what turned seven reachable
  fields unreachable in the first draft of this section.
- `TestNotesSection5NamesEveryFractionalKey` keeps the seven names above in step with the
  snapshot.
- `TestRetypingWouldSendANonStringBody` executes a field declared each way against a recording
  stub, so blocker 1's table is measured rather than asserted.
- `TestRetypingWouldRemoveTheEmptyStringUnsetPath` reads `unsetBy` from every catalog field and
  proves the typed flag refuses the empty value that field's own catalog entry depends on, so
  blocker 2 cannot quietly stop being true.

The mapping is duplicated as `prescribedManifestType` in that file, deliberately: prose cannot
be executed, and the point of running it is to check what the mapping PRODUCES rather than how
it is spelled. Change both together.

## 6. Eight vLLM fields are write-only, so IaC cannot round-trip them (objective 92 / story 212)

Files: `src/util/cliManifest/` entry for `services/vllm/{id}/show`.
Change due: return these on the show response, the way story 206 just did for `image`.

A field that `services/vllm/add` or `services/vllm/{id}/update` accepts but
`services/vllm/{id}/show` does not return cannot be pulled: `internal/services/pull.go` uses the
add/update input fields as its ALLOW-LIST but the show response as its SOURCE, so a field show
omits never reaches the yaml. `runos services diff` then reports drift the operator cannot clear.

Measured on conductor `9f626657` by `scripts/vllm_field_diff.py`, eight remain:

> `configSetId`, `configType`, `modelSourceBucket`, `modelSourceIntegrationId`,
> `modelSourceMinioServiceId`, `modelSourcePath`, `replicas`, `storageGroupId`

`image` was a ninth until story 206 added it to the show output, which is the precedent and the
exact shape of the fix.

ONE OF THE EIGHT IS ON OBJECTIVE 92'S OWN SUBJECT. The objective's Settled 6 makes the EFFECTIVE
REPLICA COUNT the thing that brings a router into existence, and `replicas` is the write-only one.
So the single input that decides whether a router exists is the one an operator cannot read back.
Show returns `directReplicas` and `routerActive`, which are runtime state, not the desired-state
value an operator wrote. `internal/services/pull.go` even carries a comment (lines 18-26) keeping
`replicas` OFF the class-coupled strip list because dropping it broke diff projection and
surprised users following the services topic, "which DOES show `replicas: 1`" — reasoning that
assumes show returns it. On vLLM it does not.

The CLI deliberately synthesizes NO read path and NO local cache for any of these. Inventing a
value the server never returned would make "pinned" and "running" indistinguishable, which is the
same reason story 206 gives for an absent pin reading as absent rather than as the published
image. Regression target: `TestVLLMWriteOnlyFieldsAreRecordedNotSynthesized`, which fails if this
list drifts from the manifest in EITHER direction, so the record cannot outlive the gap.
