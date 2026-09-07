#!/usr/bin/env python3
"""Bound the vLLM manifest field set for story 212 (objective 92).

Two subcommands, so both the snapshots and the derived set are reproducible
from a raw conductor fetch rather than trimmed or typed by hand:

  snapshot  trim a raw /cli/manifest response to the vLLM command entries
  diff      derive the new-field set from a pre and a post snapshot

Usage:
  TOK=<pat>
  curl -sH "Authorization: Bearer $TOK" https://api.dev.runos.com/cli/manifest > raw.json
  python3 scripts/vllm_field_diff.py snapshot --manifest raw.json \
      --environment dev --schema vllm-router=router.json \
      --out testdata/vllm/manifest-pre-45.5.0.json
  python3 scripts/vllm_field_diff.py diff \
      --pre  testdata/vllm/manifest-pre-45.5.0.json \
      --post testdata/vllm/manifest-post-<version>.json \
      --out  testdata/vllm/new-fields.json

A snapshot keeps every command whose name starts with `services/vllm/`, plus
the generic `service-info/{type}/advanced-config-schema` entry, because the
router and LMCache catalogs are discovered through it. Nothing else from the
manifest is kept: the other ~670 commands are noise for this story and would
make the artefact date for reasons unrelated to it. `--schema TYPE=FILE` folds
a `/service-info/<type>/advanced-config-schema` response into the same file, so
one snapshot describes one build rather than leaving the catalog in a sibling
whose provenance a reader has to match up by hand.
"""

import argparse
import json
import sys

VLLM_PREFIX = "services/vllm/"
SCHEMA_COMMAND = "service-info/{type}/advanced-config-schema"

# Class A is a field on the SERVICE RECORD: it is created or read by the
# service's own add / update / show / list command. Class B is an
# advanced-config CATALOG key, carried by one of the set-*-config commands.
# The split is derived from the command a field appears on, never from a
# hand-written list of field names, so whichever shape conductor ships is
# recorded rather than assumed.
RECORD_COMMANDS = {
    "services/vllm/add",
    "services/vllm/{id}/update",
    "services/vllm/{id}/show",
    "services/vllm/list",
}


def field_class(command):
    if command in RECORD_COMMANDS:
        return "A"
    if command.endswith("-config"):
        return "B"
    return "other"


def load(path):
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)


def write(path, payload):
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2, sort_keys=False)
        handle.write("\n")


def cmd_snapshot(args):
    raw = load(args.manifest)
    kept = [
        c for c in raw.get("commands", [])
        if c.get("command", "").startswith(VLLM_PREFIX)
        or c.get("command") == SCHEMA_COMMAND
    ]
    kept.sort(key=lambda c: c["command"])
    schemas = {}
    for pair in args.schema or []:
        service_type, _, path = pair.partition("=")
        if not path:
            raise SystemExit("--schema wants TYPE=FILE, got %r" % pair)
        schemas[service_type] = load(path)

    write(args.out, {
        "manifestVersion": raw.get("version"),
        # The version string is NOT a safe identifier. Two different manifests
        # both call themselves 45.5.0: objective 91 bumped dev to it, and
        # objective 92's story 205 bumped its own branch to it independently.
        # The conductor commit is the identifier that discriminates.
        "conductorCommit": args.commit,
        "environment": args.environment,
        "apiUrl": args.api_url,
        "fetchedAt": args.fetched_at,
        "commandCountInFullManifest": len(raw.get("commands", [])),
        "commands": kept,
        "advancedConfigSchemas": schemas,
    })
    print("wrote %s: %d vLLM entries and %d catalog schemas from manifest %s"
          % (args.out, len(kept), len(schemas), raw.get("version")))


def input_fields(command):
    return (command.get("input") or {}).get("fields", []) or []


def output_fields(command):
    """Output fields are declared as BARE STRINGS, not objects.

    Reading them as objects is the mistake that hides them: an earlier scan of
    this manifest reported "no command declares a warnings output field" while
    seven did, because it looked for {"name": ...}. Keep this function as the
    single place that knows the shape.
    """
    return (command.get("output") or {}).get("fields", []) or []


def by_name(snapshot):
    return {c["command"]: c for c in snapshot.get("commands", [])}


def describe(field, command):
    described = {
        "name": field.get("name"),
        "type": field.get("type"),
        "class": field_class(command),
    }
    for optional in ("valueType", "valueFields", "required", "positional", "enum",
                     "allowEmpty", "format", "default"):
        if field.get(optional) is not None:
            described[optional] = field[optional]
    return described


def cmd_diff(args):
    pre, post = load(args.pre), load(args.post)
    pre_cmds, post_cmds = by_name(pre), by_name(post)

    commands = []
    for name in sorted(post_cmds):
        post_cmd = post_cmds[name]
        pre_cmd = pre_cmds.get(name)
        pre_in = {f.get("name") for f in input_fields(pre_cmd or {})}
        pre_out = set(output_fields(pre_cmd or {}))

        new_in = [describe(f, name) for f in input_fields(post_cmd)
                  if f.get("name") not in pre_in]
        new_out = [{"name": f, "class": field_class(name)}
                   for f in output_fields(post_cmd) if f not in pre_out]
        if not new_in and not new_out:
            continue
        commands.append({
            "command": name,
            "newInManifest": pre_cmd is None,
            "newInputFields": new_in,
            "newOutputFields": new_out,
        })

    # Criterion 6: a field an operator can WRITE but never READ BACK cannot be
    # round-tripped by the IaC pull/diff/sync path, so it is drift the operator
    # cannot clear. Computed over the POST manifest as a whole, not only over
    # the new fields, because an existing write-only field is just as unclearable.
    written = set()
    for name in ("services/vllm/add", "services/vllm/{id}/update"):
        for f in input_fields(post_cmds.get(name, {})):
            if not f.get("positional"):
                written.add(f["name"])
    readable = set(output_fields(post_cmds.get("services/vllm/{id}/show", {})))
    write_only = sorted(written - readable)

    new_names = sorted({f["name"] for c in commands for f in c["newInputFields"]}
                       | {f["name"] for c in commands for f in c["newOutputFields"]})
    write(args.out, {
        "generatedBy": "scripts/vllm_field_diff.py",
        "pre": {
            "manifestVersion": pre.get("manifestVersion"),
            "conductorCommit": pre.get("conductorCommit"),
            "environment": pre.get("environment"),
        },
        "post": {
            "manifestVersion": post.get("manifestVersion"),
            "conductorCommit": post.get("conductorCommit"),
            "environment": post.get("environment"),
        },
        "counts": {
            "commandsWithNewFields": len(commands),
            "distinctNewFieldNames": len(new_names),
            "writeOnlyFields": len(write_only),
        },
        "commands": commands,
        "writeOnlyFields": write_only,
    })
    print("wrote %s: %d commands, %d distinct new field names, %d write-only"
          % (args.out, len(commands), len(new_names), len(write_only)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="subcommand", required=True)

    snap = sub.add_parser("snapshot")
    snap.add_argument("--manifest", required=True)
    snap.add_argument("--out", required=True)
    snap.add_argument("--environment", required=True)
    snap.add_argument("--api-url", required=True)
    snap.add_argument("--commit", required=True,
                      help="conductor commit the manifest was built from")
    snap.add_argument("--fetched-at", required=True)
    snap.add_argument("--schema", action="append", metavar="TYPE=FILE",
                      help="fold in a /service-info/TYPE/advanced-config-schema response")
    snap.set_defaults(func=cmd_snapshot)

    diff = sub.add_parser("diff")
    diff.add_argument("--pre", required=True)
    diff.add_argument("--post", required=True)
    diff.add_argument("--out", required=True)
    diff.set_defaults(func=cmd_diff)

    args = parser.parse_args()
    args.func(args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
