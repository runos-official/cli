# Where the vLLM manifest snapshots came from

Story 212 (objective 92) bounds "every new vLLM router and LMCache manifest field" as a
checked-in artefact rather than as prose. These three files are that artefact. Regenerate them
with `scripts/vllm_field_diff.py` rather than editing them by hand.

## Do not key anything off the manifest version string

`45.5.0` is claimed by TWO different manifests. Objective 91 bumped `dev` to 45.5.0, and
objective 92's story 205 independently bumped its own branch to 45.5.0. The conductor COMMIT is
the identifier that discriminates, which is why every snapshot records `conductorCommit`.

The cheap runtime discriminator is the input-field count of
`services/vllm/{id}/set-router-config`: **26 means pre-205, 44 means post-205.**

## manifest-pre-45.5.0.json

- Source: live fetch, `GET https://api.dev.runos.com/cli/manifest`, 2026-09-07, rjwrn dev PAT.
- Conductor: `0fd5d0f2` on `origin/dev` (merge of objective 91), deployed to runos-dev as
  deployment 70, resolved version 1.29.0-rc.17.
- Verified pre-change on BOTH dependencies before it was used as a baseline: `set-router-config`
  declares 26 input fields, and the `vllm-router` catalog schema returns 25 fields of which none
  declares a `delivery` kind — which is exactly the before-baseline story 205's own summary
  records. No vLLM entry carries `routerImage`, `lmcacheServerImage` or `lmcacheServerGeneration`.
- The two `/service-info/<type>/advanced-config-schema` responses were fetched from the same
  build and are folded in under `advancedConfigSchemas`.

## manifest-post-45.6.0.json

- Source: **NOT a live fetch.** No deployed build carried stories 205 and 206 while this story
  ran. Foreman deploys an objective's integration branch only after every story of the objective
  lands, and story 207 was still open.
- Conductor: `9f626657` ("merge story 206"), head of
  `origin/fm/obj-92-give-the-vllm-router-and-the-lmcache-server-the-operator-sur`. It contains
  story 205 at `b94cea97` and story 206 at `9eb862bd`. It does NOT contain story 207, so
  `servedModelNameHubShape` and the `warnings` output field are absent here by construction.
- `MANIFEST_VERSION` declared at that commit: `45.6.0`.
- Produced by the Conductor implementer running `getManifest()` and
  `getAdvancedConfigSchema()` — the same functions the route handlers call — in a detached
  worktree at that commit, then publishing the output as foreman artefacts 18 to 23.
- HOW IT REACHED THIS REPO WITHOUT A TRANSCRIPTION STEP. Each blob carries a SHA-256 that
  Conductor computed on its own source file. Each was extracted byte-exactly from the recorded
  tool result and its digest verified before use; all six matched on the first attempt:

  | Artefact | Contents | SHA-256 |
  |---|---|---|
  | 20 | manifest part A, 13 commands | `1e2f0fdd2931aef58e3d8f67e772c2581bdbd5815c68c12e7fc2e038b737d03a` |
  | 21 | manifest part B, set-advanced-configs | `e63bedf8a294c0ac657092c79bd271eabc748caef421284dc57bbab465f68369` |
  | 22 | manifest part C, set-router-config + set-lmcache-engine-config | `c7cfe05cc26b1e599eb7a06f9a4275f2a7f42c4ff2cdc36c63ba3e2c66f21934` |
  | 23 | manifest part D, 4 tail commands | `be37a611fcb990e7207a3891c5203b0445a183f91a7bcac3537aa0fd523e672f` |
  | 19 | vllm-router advanced-config-schema, 43 fields | `c8f7763f6ebea7d76b54d4b28ae526d78afe26d8a5aa15e86d91ef574561ee70` |
  | 18 | lmcache-server advanced-config-schema, 34 fields | `f0349a0500defcef6f736be51751eb863e1368fc581646d3f578f061bb2ed66f` |

  Reassembly used Conductor's own script and its assertion that the four parts total 20 commands.

## new-fields.json

Derived, never hand-written:

```
python3 scripts/vllm_field_diff.py diff \
    --pre  testdata/vllm/manifest-pre-45.5.0.json \
    --post testdata/vllm/manifest-post-45.6.0.json \
    --out  testdata/vllm/new-fields.json
```

Every test that quantifies over "every new field" reads this file, so a later field addition is a
REGENERATION, not a test rewrite.

`class` is derived from the command a field appears on, not from a list of field names: `A` for a
field on the service record (add / update / show / list), `B` for an advanced-config catalog key
(a `set-*-config` command).

## What this snapshot pair is not

It is a snapshot, so it dates. When a build carrying objective 92 is deployed, re-run the two
`snapshot` invocations against the live endpoints and re-run `diff`. The tests read the artefact,
so nothing but these three files should need to change.
