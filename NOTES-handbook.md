# Handbook / MCP-topic updates due from the CLI review-2 fixes (2026-08-17)

Behaviour an agent-facing document describes and this change moved. No conductor API
contract changed.

## 1. The read server's topic gate counts differently (review 2 item 18)

Old: any topic key a SEARCH returned counted, so one `mcp_topics_search` that matched two
topics opened the whole read server without reading a word.

New: `mcp_bootstrap` counts as one document, and each `mcp_topics_show` counts as one.
`mcp_topics_search` counts as nothing: it returns keys, titles and content LENGTHS. The gate
is 2, so the path is bootstrap, then one `mcp_topics_show`.

The MCP server's own `instructions` and the gate refusal text now say this. Any handbook
article or MCP topic that tells an agent "search two topics to open the gate" is wrong.

## 2. A failed bootstrap no longer locks a server (review 2 item 2)

`manifest_update` and `mcp_bootstrap` are never behind a gate. After a bootstrap attempt
FAILS (expired sign-in, conductor unreachable), the bootstrap gate and the topic gate both
downgrade from a refusal to a warning prepended to every result, so the server stays usable
and the caller is told the instructions were never read.

## 3. Destructive MCP tools state the confirm rule in their description

Every destructive tool's description now ends with: "DESTRUCTIVE: this cannot be undone. The
call is refused unless you pass confirm=true ...". 84 commands in manifest 41.0.0.

Two commands became destructive with this fix and now demand `--yes` / `confirm=true`:
`clusters/etcd-remove-member` and `services/prometheus/{id}/custom-rules-delete`.

## 4. `--follow` progress goes to stdout in text mode

`runos <cmd> --follow` writes its progress lines to stdout, and to stderr only with `--json`.
A document that says progress is always on stderr is out of date.
