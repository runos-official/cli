package desktopcontract

// The tables below are the contract. They list every key each command prints for RunOS Desktop.
// docs/desktop-contract.md says the same in prose, and a test fails when the two disagree.
// Required means the CLI always sends the key. A key Desktop needs is always Required.

// VPNStatus is `runos vpn status --json`, and also `vpn up`, `down`, `connect`, `disconnect` and
// `forget-key` under --json: they all print the daemon's status.
var VPNStatus = Contract{
	{"schemaVersion", Int, true, DesktopOptional},
	{"running", Bool, true, DesktopRequired},
	{"interface", String, false, DesktopOptional},
	{"address", String, false, DesktopOptional},
	{"deviceId", String, false, DesktopIgnored},
	{"accountId", String, false, DesktopOptional},
	{"session", Object, true, DesktopRequired},
	{"session.present", Bool, true, DesktopRequired},
	{"session.expiresAt", Time, false, DesktopOptional},
	{"session.loginRequired", Bool, true, DesktopRequired},
	{"dns", Object, true, DesktopOptional},
	{"dns.available", Bool, true, DesktopRequired},
	{"dns.mode", String, true, DesktopOptional},
	{"dns.error", String, true, DesktopOptional},
	{"dns.pending", Bool, false, DesktopIgnored},
	{"clusters", Objects, true, DesktopRequired},
	{"clusters[].cid", String, true, DesktopRequired},
	{"clusters[].name", String, true, DesktopRequired},
	{"clusters[].connected", Bool, true, DesktopRequired},
	{"clusters[].reachable", Bool, true, DesktopRequired},
	{"clusters[].reason", String, false, DesktopOptional},
	{"clusters[].endpoint", String, false, DesktopOptional},
	{"clusters[].allowedIps", Strings, true, DesktopIgnored},
	{"clusters[].resolver", String, false, DesktopOptional},
	{"clusters[].zones", Strings, true, DesktopOptional},
	{"clusters[].peeredWith", Strings, true, DesktopRequired},
	{"clusters[].peerUp", Bool, true, DesktopRequired},
	{"clusters[].lastHandshake", Time, false, DesktopOptional},
	{"clusters[].rxBytes", Int, true, DesktopOptional},
	{"clusters[].txBytes", Int, true, DesktopOptional},
	{"revision", String, false, DesktopIgnored},
	{"lastPollAt", Time, false, DesktopIgnored},
	{"lastPollError", String, false, DesktopOptional},
	{"lastApplyError", String, false, DesktopIgnored},
	{"version", String, true, DesktopOptional},
}

// CLIStatus is `runos status --json`. The command builds a map, so a key is present only when it
// has a value; only schemaVersion and authenticated are always there.
var CLIStatus = Contract{
	{"schemaVersion", Int, true, DesktopOptional},
	{"authenticated", Bool, true, DesktopRequired},
	{"accountId", String, false, DesktopOptional},
	{"companyName", String, false, DesktopOptional},
	{"website", String, false, DesktopIgnored},
	{"vpnAccountId", String, false, DesktopOptional},
	{"vpnAccountMismatch", Bool, false, DesktopOptional},
	{"vpnRunning", Bool, false, DesktopOptional},
	{"authError", String, false, DesktopOptional},
	{"authErrorKind", String, false, DesktopOptional},
	{"authErrorDetail", String, false, DesktopIgnored},
	{"sessionExpired", Bool, false, DesktopOptional},
	{"sessionExpiresAt", Time, false, DesktopIgnored},
	{"authMethod", String, false, DesktopIgnored},
	{"signedInAt", String, false, DesktopIgnored},
	{"environment", String, false, DesktopIgnored},
	{"apiUrl", String, false, DesktopIgnored},
	{"consoleUrl", String, false, DesktopIgnored},
	{"defaultClusterId", String, false, DesktopIgnored},
	{"defaultClusterName", String, false, DesktopIgnored},
	{"defaultClusterMissing", Bool, false, DesktopIgnored},
}

// Update is `runos update --json` and `runos update --check --json`: one document, two readers.
var Update = Contract{
	{"schemaVersion", Int, true, DesktopRequired},
	{"cli", Object, true, DesktopRequired},
	{"cli.updated", Bool, true, DesktopRequired},
	{"cli.updateAvailable", Bool, true, DesktopOptional},
	{"cli.currentVersion", String, false, DesktopOptional},
	{"cli.version", String, false, DesktopOptional},
	{"cli.message", String, false, DesktopOptional},
	{"desktop", Object, false, DesktopOptional},
	{"desktop.updated", Bool, true, DesktopRequired},
	{"desktop.updateAvailable", Bool, true, DesktopOptional},
	{"desktop.currentVersion", String, false, DesktopOptional},
	{"desktop.version", String, false, DesktopOptional},
	{"desktop.message", String, false, DesktopOptional},
}

// SignInEvent is one line of `runos login --json` and `runos vpn up --json`: a stream of objects,
// one per line. Desktop ignores an event name it does not know.
var SignInEvent = Contract{
	{"event", String, true, DesktopRequired},
	{"deviceId", String, false, DesktopOptional},
	{"url", String, false, DesktopOptional},
	{"browserOpened", Bool, false, DesktopOptional},
	{"reason", String, false, DesktopOptional},
	{"message", String, false, DesktopOptional},
}
