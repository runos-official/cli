#!/usr/bin/env python3
"""Select service add contracts with node affinity from a served manifest."""

import json
import re
import sys


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: extract_create_affinity_manifest.py INPUT OUTPUT")
    with open(sys.argv[1], encoding="utf-8") as source:
        manifest = json.load(source)
    commands = []
    for command in manifest["commands"]:
        if not re.fullmatch(r"services/[^/]+/add", command["command"]):
            continue
        fields = command.get("input", {}).get("fields", [])
        if not any(field["name"] == "nodeAffinityTags" for field in fields):
            continue
        selected_fields = [
            {key: field[key] for key in ("name", "type", "required", "positional", "allowEmpty") if key in field}
            for field in fields
        ]
        selected_flags = [
            {key: flag[key] for key in ("name", "default") if key in flag}
            for flag in command.get("input", {}).get("flags", [])
        ]
        commands.append({
            "command": command["command"],
            "endpoint": command["endpoint"],
            "method": command["method"],
            "input": {"fields": selected_fields, "flags": selected_flags},
        })
    with open(sys.argv[2], "w", encoding="utf-8") as output:
        json.dump({"version": manifest["version"], "commands": commands}, output, indent=2, ensure_ascii=True)
        output.write("\n")


if __name__ == "__main__":
    main()
