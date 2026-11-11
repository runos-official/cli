# Agents list fixtures

`dev-sanitized.json` records `runos agents list --json` from dev on 2026-11-09.
Every cluster, node, and host identifier was replaced before the fixture entered this repository.
The deployed API did not yet send the five derived agent fields.

`states.json` adds the five fields from the server contract for focused renderer tests.
The file contains no live identifiers or addresses.
