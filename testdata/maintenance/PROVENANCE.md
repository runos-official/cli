# Where the account maintenance fixtures came from

Objective 108, story 302. The RunOS CLI renders the account maintenance
read endpoint. Every fixture here is a RESPONSE this client must render.

## Provenance

The fixtures are HAND-BUILT from the published response contract, not
recorded from a running system. The contract is section 3.6 of the
conductor story that serves the endpoint, which fixes the envelope keys,
the cluster header fields, the node row fields, the row detail fields,
the snake_case wire form of every enum, and the two age fields in
seconds.

Hand-built is deliberate for five of the six files: a recorded response
carries whatever states the test account happened to be in, and these
fixtures have to carry states a live account cannot be put into on
demand, such as a node whose facts are too old and a status word a later
release invents.

No file here holds a real identifier. Every account id, cluster id, node
id, name and hostname is a role name or a short placeholder. No file
holds an address. A kernel and a package version are upstream Debian
strings, which name no RunOS system.

## The files

- `full.json` — one account, two clusters, five nodes. Covers the five
  node states a verifier reads: current, waiting updates with a pending
  reboot, a node RunOS cannot reach, a node whose facts are too old, and
  a node whose automatic update timers were enabled outside RunOS. The
  second cluster is the degraded header: it did not answer, so its
  Kubernetes fields are null and its facts came from nowhere, while its
  node rows still arrive.
- `empty-account.json` — an account with no cluster.
- `cluster-without-nodes.json` — a cluster that reports no node.
- `invented-status.json` — every server word replaced by a value this
  CLI build has never seen, plus two fields the contract does not carry
  (`quarantineReleasesAt`, `detail.quarantineEvidence`). The renderer
  must print all of them verbatim. This file is what proves the client
  holds no word list, no mapping and no default word.
- `cluster-nodes-null.json` — two clusters, and the first sends `nodes`
  as null. Added for rework finding 405. The shape hook must NOT require
  a node array in every element: when it did, one deviant cluster
  dropped the whole account to the generic renderer and the healthy
  cluster lost its node table silently. Its test also builds a second
  variant in memory with the `nodes` KEY removed, so both the null and
  the absent case are covered.
- `clusters-list.json` and `clusters-list-empty.json` — NOT this view.
  They are the `clusters/list` response, which also answers
  `{"clusters":[...]}`. They exist so the fall-through test proves the
  shape hook leaves another command's output alone, including the empty
  case, which is the shape a weaker hook would capture by accident.
