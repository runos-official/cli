# What RunOS Desktop reads from the CLI

RunOS Desktop runs the `runos` binary and decodes what it prints with strict types. A key in the
wrong shape (most often `null` where Desktop reads a list) makes Desktop drop the whole answer and
show a bare menu with no error. This page is the contract. The tests in `internal/desktopcontract`,
`internal/vpn` and `cmd` (files named `desktop_contract_*_test.go`) encode the real Go output types
and fail when output leaves it. Run them alone with `make desktopcontract`.

Rules for every document below:

- No key is ever `null`. A list is `[]`, an unset text or flag is left out or empty, never `null`.
- A timestamp is RFC 3339, UTC, whole seconds, ending in `Z` (`2026-10-26T12:30:15Z`). A time that
  was never set is left out. It is never `0001-01-01T00:00:00Z`.
- `schemaVersion` is an integer.
- A new key must be added to the table in `internal/desktopcontract/tables.go` and to this page.
  A test fails until both say the same.

"Always sent" means the CLI sends the key in every reply. "Desktop" is how Desktop reads it:
`required` is a non-optional Swift property (a missing or null value fails the whole decode),
`optional` tolerates a missing key but not a wrong type, `ignored` is not decoded.

## Commands Desktop runs

| Command | Used for | Output Desktop decodes |
| --- | --- | --- |
| `runos --version` | compatibility, and the Restart VPN check | one line, the bare version |
| `runos status --json` | who is signed in | the status below |
| `runos vpn status --json` | the tunnel and its clusters | the VPN status below |
| `runos vpn up --json --no-browser`, `vpn up --non-interactive --json` | connect | sign-in events on stdout while it runs; exit code and stderr on failure |
| `runos vpn down --json`, `vpn connect <cid> --json`, `vpn disconnect <cid> --json` | tunnel changes | nothing; exit code and stderr on failure (stdout is the VPN status) |
| `runos login --json --no-browser` | sign in | sign-in events, one JSON object per line |
| `runos logout`, `runos account switch <aid> --json` | sign out, switch | nothing; exit code and stderr on failure |
| `runos update --check --json`, `runos update --json` | update | the update document below |
| `runos desktop relaunch --wait-pid <pid>` | restart after update | nothing |
| `runos user accounts --json` | account list | `{"accounts":[{aid, accountRole, isDefault, name?, companyName?}]}` |
| `runos nodes list --cid <cid> -j` | connection test | `{"nodes":[{name?, hostname?, vpnIp?}]}` |
| `sudo runos vpn install`, `sudo runos vpn restart` | run by Desktop as root | text only; stderr on failure |

`user accounts` and `nodes list` are generated from the API manifest and pass the server's reply
through, so the CLI cannot pin them. Desktop reads `accounts` and `nodes` as non-optional lists, so
the server must send `[]` and never `null`. Desktop ignores a failure of `user accounts` and shows an
error for `nodes list`.

## Version

`runos --version` prints the bare version and a newline: `1.22.2`, `1.23.0-rc.1` or
`dev-2026-10-26T12:00:00Z`. Desktop treats `dev` and `dev-...` as a development build, refuses a
version below its floor, and cannot identify anything that is not `MAJOR.MINOR.PATCH` with an
optional `-prerelease`. The VPN service reports the same string as `version` in `vpn status`
because it is started with the CLI's own `version.Version`. Desktop offers Restart VPN when the two
strings differ, so a restart (a new service process from the new binary) makes them equal.

## `runos status --json`

| Key | Type | Always sent | Desktop |
| --- | --- | --- | --- |
| `schemaVersion` | int | yes | optional |
| `authenticated` | bool | yes | required |
| `accountId` | string | no | optional |
| `companyName` | string | no | optional |
| `website` | string | no | ignored |
| `vpnAccountId` | string | no | optional |
| `vpnAccountMismatch` | bool | no | optional |
| `vpnRunning` | bool | no | optional |
| `authError` | string | no | optional |
| `authErrorKind` | string (`network` or `rejected`) | no | optional |
| `authErrorDetail` | string | no | ignored |
| `sessionExpired` | bool | no | optional |
| `sessionExpiresAt` | time | no | ignored |
| `authMethod` | string | no | ignored |
| `signedInAt` | string | no | ignored |
| `environment` | string | no | ignored |
| `apiUrl` | string | no | ignored |
| `consoleUrl` | string | no | ignored |
| `defaultClusterId` | string | no | ignored |
| `defaultClusterName` | string | no | ignored |
| `defaultClusterMissing` | bool | no | ignored |

## `runos vpn status --json`

The same document is printed by `vpn up`, `down`, `connect`, `disconnect` and `forget-key` with
`--json`. When the daemon answers without a status the CLI prints a status with `running` false, never
`null`.

| Key | Type | Always sent | Desktop |
| --- | --- | --- | --- |
| `schemaVersion` | int | yes | optional |
| `running` | bool | yes | required |
| `interface` | string | no | optional |
| `address` | string | no | optional |
| `deviceId` | string | no | ignored |
| `accountId` | string | no | optional |
| `session` | object | yes | required |
| `session.present` | bool | yes | required |
| `session.expiresAt` | time | no | optional |
| `session.loginRequired` | bool | yes | required |
| `dns` | object | yes | optional |
| `dns.available` | bool | yes | required |
| `dns.mode` | string | yes | optional |
| `dns.error` | string | yes | optional |
| `dns.pending` | bool | no | ignored |
| `clusters` | list of objects, `[]` when none | yes | required |
| `clusters[].cid` | string | yes | required |
| `clusters[].name` | string | yes | required |
| `clusters[].connected` | bool | yes | required |
| `clusters[].reachable` | bool | yes | required |
| `clusters[].reason` | string | no | optional |
| `clusters[].endpoint` | string | no | optional |
| `clusters[].allowedIps` | list of strings, `[]` when none | yes | ignored |
| `clusters[].resolver` | string | no | optional |
| `clusters[].zones` | list of strings, `[]` when none | yes | optional |
| `clusters[].peeredWith` | list of strings, `[]` when none | yes | required |
| `clusters[].peerUp` | bool | yes | required |
| `clusters[].lastHandshake` | time | no | optional |
| `clusters[].rxBytes` | int | yes | optional |
| `clusters[].txBytes` | int | yes | optional |
| `revision` | string | no | ignored |
| `lastPollAt` | time | no | ignored |
| `lastPollError` | string | no | optional |
| `lastApplyError` | string | no | ignored |
| `version` | string | yes | optional |

An older service may omit `schemaVersion` (before 1), `dns` (before 2) or a cluster's lists. The CLI
reads those replies and prints the table above anyway: a missing list becomes `[]`.

## `runos update --check --json` and `runos update --json`

| Key | Type | Always sent | Desktop |
| --- | --- | --- | --- |
| `schemaVersion` | int | yes | required |
| `cli` | object | yes | required |
| `cli.updated` | bool | yes | required |
| `cli.updateAvailable` | bool | yes | optional |
| `cli.currentVersion` | string | no | optional |
| `cli.version` | string | no | optional |
| `cli.message` | string | no | optional |
| `desktop` | object | no | optional |
| `desktop.updated` | bool | yes | required |
| `desktop.updateAvailable` | bool | yes | optional |
| `desktop.currentVersion` | string | no | optional |
| `desktop.version` | string | no | optional |
| `desktop.message` | string | no | optional |

`desktop.*` is always sent when `desktop` is present. `desktop` is absent when Desktop is not installed.

## Sign-in events (`runos login --json`, `runos vpn up --json`)

One JSON object per line on stdout, flushed as it happens. Desktop ignores an `event` it does not
know. Known events: `device_code` (`deviceId`, `url`), `browser_opened` (`browserOpened`), `pending`,
`authorized`, `notice` (`reason`, `message`), `error` (`reason`, `message`).

| Key | Type | Always sent | Desktop |
| --- | --- | --- | --- |
| `event` | string | yes | required |
| `deviceId` | string | no | optional |
| `url` | string | no | optional |
| `browserOpened` | bool | no | optional |
| `reason` | string | no | optional |
| `message` | string | no | optional |

## When a command fails

- Exit code is 1. No command here sets another code.
- stdout holds only sign-in events (for `login` and `vpn up`), or nothing.
- stderr holds `Error: ` and the message. Desktop shows it. It has no colour codes or control
  characters, no leading or trailing blank space, and is at most 300 characters or lines of at most
  120. `vpn up` adds a second paragraph that starts `Stuck? 'runos vpn diagnose'`; Desktop skips it
  and shows the last line before it.
- A missing VPN service reads `the RunOS VPN service is not running (<socket path>). Run 'sudo runos
  vpn install' first.` Desktop recognises it by the socket path `/var/run/runos-vpn.sock` or the words
  `vpn install` anywhere in the text. Two other CLI errors also contain those words (the socket
  permission error, and the older-service error that offers `vpn install` as a fallback), so Desktop
  can take them for a missing service. See "Known gap" below.

## Known gap

Desktop's check for a missing service matches on text that two other errors share. Desktop should
match `service is not running (` as well as the path, or the CLI should offer a machine-readable
marker. This repo pins the wording Desktop relies on; it does not change Desktop.
