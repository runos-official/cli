package vpn

import (
	"bytes"
	"strconv"
	"strings"
)

// resolverMarker is the first line of every resolver file the macOS daemon writes. Kept out of the
// darwin build tag so the Linux and Windows test runs cover the matching rule too.
const resolverMarker = "# managed by runos-vpn"

// nrptComment is the comment on every NRPT rule the Windows daemon writes.
const nrptComment = "runos-vpn"

/*
isManagedResolverFile reports whether a resolver file was written by this daemon.

The first line must BE the marker, nothing before it and nothing after it on that line. The check
used to be "the marker appears anywhere", so a file that mentions the marker in a longer comment
(`# managed by runos-vpn-dev`, or a note in someone's own file) was treated as ours, and the
start-up sweep deleted it. Teardown and the sweep delete every file this accepts.
*/
func isManagedResolverFile(data []byte) bool {
	first, _, _ := bytes.Cut(data, []byte("\n"))
	return string(first) == resolverMarker
}

// nrptManagedClause is the PowerShell test for "this rule is ours". -ceq, not -eq: -eq ignores
// case, so a rule commented RUNOS-VPN (somebody else's) matched and was removed.
func nrptManagedClause() string {
	return "$_.Comment -ceq '" + nrptComment + "'"
}

// nrptTeardownScript removes every NRPT rule the daemon owns and prints how many it removed, in one
// PowerShell start. The count lets Teardown flush the DNS cache only when something was removed.
func nrptTeardownScript() string {
	return "$r = @(Get-DnsClientNrptRule | Where-Object { " + nrptManagedClause() + " }); " +
		"$r | Remove-DnsClientNrptRule -Force; $r.Count"
}

// removedRuleCount reads the last line of the teardown script's output. -1 means the output could
// not be read, which callers treat as "something may have been removed".
func removedRuleCount(out string) int {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	count, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1]))
	if err != nil || count < 0 {
		return -1
	}
	return count
}
