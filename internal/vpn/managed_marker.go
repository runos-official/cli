package vpn

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// resolverDir is where macOS reads per-domain resolvers. Untagged so the diagnose report can read it.
const resolverDir = "/etc/resolver"

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

// nrptTeardownScript removes every NRPT rule the daemon owns, in one PowerShell start, and prints
// "removed remaining" as its last line. It stops on an error outside the loop, so that gives a
// non-zero exit. Each removal has its own try/catch: a rule that cannot be removed is reported on a
// line of its own ("failed: ...") and the loop goes on to the rest. The count of what is still
// present afterwards, not the exit code, decides whether the sweep worked.
func nrptTeardownScript() string {
	clause := nrptManagedClause()
	return "$ErrorActionPreference = 'Stop'; " +
		"$r = @(Get-DnsClientNrptRule | Where-Object { " + clause + " }); " +
		"$removed = 0; " +
		"foreach ($x in $r) { try { $x | Remove-DnsClientNrptRule -Force; $removed++ } " +
		"catch { Write-Output ('failed: ' + $_.Exception.Message) } }; " +
		"$left = @(Get-DnsClientNrptRule | Where-Object { " + clause + " }).Count; " +
		`"$removed $left"`
}

// nrptTeardownOutcome reads the script's last line. flush says whether the DNS cache needs a flush:
// when a rule was removed or the output cannot be read. err is set when a rule remains or the output
// cannot be read, because the sweep must not report a clean machine it could not confirm.
func nrptTeardownOutcome(out string) (flush bool, err error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) != 2 {
		return true, fmt.Errorf("could not read the result of the NRPT teardown: %q", out)
	}
	removed, errRemoved := strconv.Atoi(fields[0])
	left, errLeft := strconv.Atoi(fields[1])
	if errRemoved != nil || errLeft != nil || removed < 0 || left < 0 {
		return true, fmt.Errorf("could not read the result of the NRPT teardown: %q", out)
	}
	if left > 0 {
		return removed > 0, fmt.Errorf("%d NRPT rule(s) of the VPN are still present after the removal: %s",
			left, strings.Join(strings.Fields(strings.Join(lines[:len(lines)-1], " ")), " "))
	}
	return removed > 0, nil
}

// parseResolverNameserver reads the nameserver a resolver file names.
func parseResolverNameserver(data []byte) (netip.Addr, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "nameserver" {
			if addr, err := netip.ParseAddr(fields[1]); err == nil {
				return addr, true
			}
		}
	}
	return netip.Addr{}, false
}
