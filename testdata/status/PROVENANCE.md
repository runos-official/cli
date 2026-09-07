# Where the status-read set and the enriched payloads came from

Objective 93, story 217. Two things live here, and they have different
provenance, so they are recorded separately.

## `status-commands.json` — the SET, measured

Criterion 1 of the story requires "every status read" to be a bounded,
provable set rather than a phrase, because the instance nobody listed is the
next defect. So the file is generated, never hand-written:

    python3 scripts/status_command_set.py \
        --manifest <manifest.json> \
        --source "..." \
        --out testdata/status/status-commands.json

- Source: the unfiltered `/cli/manifest` route of the RunOS **dev**
  environment conductor, fetched 2026-09-07. The unfiltered route is used on
  purpose: the account-scoped route carries only the modules one account has
  switched on, which would silently shrink the set.
- Manifest version at that fetch: **45.4.0**.
- Rule applied: every command whose path ends in the segment `status`, plus
  `clusters/is-app-deployment-ready`, the cluster deployment-readiness reader
  the objective plan header puts in scope by name.
- Result: **26** commands, and **every one of them declares
  `output.type: "object"`**. There is no array-typed and no type-unset status
  read in this manifest. That is the measured answer to the story's criterion
  3, not an omission — `TestStatusCommandSet_OutputTypeCensus` pins it, so a
  future manifest that adds one fails the census and forces the sweep to be
  extended rather than quietly skipped.

Re-derive it after a manifest change by re-running the generator; the output
is sorted by command, so an unchanged manifest reproduces the file byte for
byte.

## `fixtures/*.json` — the PAYLOADS, plausible and clearly labelled

These are **not** recordings of a live enriched response, because at the time
this story was implemented the conductor stories at objective positions 0 and
1 had not published their contract yet: neither carried an implementation
summary, and a handbook search for the backing-service attribution fields
returned nothing. So the fixtures use the three shapes the story's acceptance
criteria name, covering what the plan header fixes the contract must carry
(type, identifier, operator-facing NAME, state, reason; own fault separated
from backend fault; a word for a check that could not answer):

| file | shape |
|------|-------|
| `enriched-dependencies.json` | a top-level `dependencies` array of objects, each with type, identifier, name, state and message |
| `enriched-backend-object.json` | a nested `backend` object with type, identifier, name, state and reason |
| `enriched-scalars.json` | top-level scalars for a reason and for a check that could not answer, beside a `replicas` object |

The identifiers and names in them are invented.

**The fix under test does not depend on these names.** The renderer defect
this story fixes was a gate that discarded every key a summary did not
consume, so it loses nothing whatever conductor ends up calling the fields.
If the published contract differs, updating these three files is a rename and
the tests keep their meaning.

Each fixture is stored in Go's canonical `json.MarshalIndent(v, "", "  ")`
form. That is deliberate: it lets criterion 5 assert that `--json` output is
**byte-identical** to the response body rather than merely equivalent.
