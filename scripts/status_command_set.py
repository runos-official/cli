#!/usr/bin/env python3
"""Derive the set of manifest commands that return a service status read.

Story 217 / objective 93 criterion 1: "every status read" has to be a
BOUNDED, PROVABLE set, not a phrase and not a hand-written list, because
the instance nobody listed is the next defect. This script produces that
set from a live manifest fetch so a later reader can re-derive it instead
of trusting a pasted list.

The rule, stated once here and applied mechanically:

  * every command whose path ends in the segment `status`, plus
  * the cluster deployment-readiness command
    (`clusters/is-app-deployment-ready`), which the objective plan header
    names explicitly because it folds two services into one verdict and
    reported ready during the incident.

For each command the set records its declared output type and its declared
output field names, which is what the rendering tests iterate.

Usage:

    # from a file already fetched
    python3 scripts/status_command_set.py --manifest manifest.json \
        --out testdata/status/status-commands.json

    # or fetch it (unfiltered route; needs an API key with any account)
    RUNOS_API_URL=https://api.example.com RUNOS_API_KEY=... \
        python3 scripts/status_command_set.py --fetch \
        --out testdata/status/status-commands.json

The output is stable: commands are sorted by path, so a re-run against an
unchanged manifest produces a byte-identical file.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.request

# The cluster-level deployment-readiness reader. Its path does not end in
# `status`, so the path rule alone would miss it; the objective plan header
# puts it in scope by name.
READINESS_COMMAND = "clusters/is-app-deployment-ready"

MANIFEST_ENDPOINT = "/cli/manifest"


def fetch_manifest(base_url: str, api_key: str) -> dict:
    req = urllib.request.Request(
        base_url.rstrip("/") + MANIFEST_ENDPOINT,
        headers={"Authorization": "Bearer " + api_key},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def is_status_read(command: str) -> bool:
    return command.rstrip("/").split("/")[-1] == "status" or command == READINESS_COMMAND


def field_names(output: dict) -> list[str]:
    """Manifest output fields are a mix of bare strings and richer objects
    ({"name": ...}); the CLI formatter only ever uses the names."""
    names = []
    for f in output.get("fields") or []:
        if isinstance(f, str):
            names.append(f)
        elif isinstance(f, dict) and f.get("name"):
            names.append(f["name"])
    return names


def build_set(manifest: dict, source: str) -> dict:
    entries = []
    for cmd in manifest.get("commands") or []:
        name = cmd.get("command") or ""
        if not is_status_read(name):
            continue
        output = cmd.get("output") or {}
        entries.append(
            {
                "command": name,
                "endpoint": cmd.get("endpoint"),
                # An absent output.type is meaningful: criterion 3 covers
                # entries whose type is array OR unset, so record null
                # rather than defaulting it to anything.
                "output_type": output.get("type"),
                "output_fields": field_names(output),
            }
        )
    entries.sort(key=lambda e: e["command"])
    return {
        "generated_by": "scripts/status_command_set.py",
        "rule": (
            "every manifest command whose path ends in the segment 'status', "
            "plus the cluster deployment-readiness command "
            + READINESS_COMMAND
        ),
        "manifest_version": manifest.get("version"),
        "source": source,
        "count": len(entries),
        "commands": entries,
    }


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--manifest", help="path to a manifest.json already fetched")
    ap.add_argument(
        "--fetch",
        action="store_true",
        help="fetch the manifest from $RUNOS_API_URL using $RUNOS_API_KEY",
    )
    ap.add_argument(
        "--source",
        default="",
        help="provenance label recorded in the output (environment, date)",
    )
    ap.add_argument("--out", help="write here instead of stdout")
    args = ap.parse_args(argv)

    if args.fetch:
        base = os.environ.get("RUNOS_API_URL")
        key = os.environ.get("RUNOS_API_KEY")
        if not base or not key:
            print("--fetch needs RUNOS_API_URL and RUNOS_API_KEY", file=sys.stderr)
            return 2
        manifest = fetch_manifest(base, key)
        source = args.source or "live manifest fetch"
    elif args.manifest:
        with open(args.manifest, encoding="utf-8") as fh:
            manifest = json.load(fh)
        source = args.source or args.manifest
    else:
        print("give --manifest <file> or --fetch", file=sys.stderr)
        return 2

    doc = build_set(manifest, source)
    text = json.dumps(doc, indent=2, sort_keys=False) + "\n"
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(text)
        print(f"{doc['count']} status reads -> {args.out}", file=sys.stderr)
    else:
        sys.stdout.write(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
