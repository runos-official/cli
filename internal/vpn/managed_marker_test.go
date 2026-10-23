package vpn

import (
	"strings"
	"testing"
)

/*
The sweep deletes only files and rules that are exactly ours.

Teardown and the start-up sweep remove everything this matches. The macOS check was "the marker text
appears anywhere in the file", so `# managed by runos-vpn-dev`, or a user's own file that quotes
the marker in a note, was deleted. The Windows check used PowerShell -eq, which ignores case.
*/
func TestOnlyAFileWhoseFirstLineIsTheMarkerIsOurs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"written by the daemon", resolverMarker + "\nnameserver 192.0.2.53\n", true},
		{"marker only", resolverMarker + "\n", true},
		{"a longer marker on the first line", resolverMarker + "-dev\nnameserver 192.0.2.53\n", false},
		{"text after the marker", resolverMarker + " (copy)\nnameserver 192.0.2.53\n", false},
		{"marker quoted on a later line", "nameserver 192.0.2.53\n" + resolverMarker + "\n", false},
		{"marker inside a comment", "# see: " + resolverMarker + "\nnameserver 192.0.2.53\n", false},
		{"no marker", "nameserver 192.0.2.53\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isManagedResolverFile([]byte(tc.body)); got != tc.want {
				t.Errorf("isManagedResolverFile(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestTheNrptTestIsCaseSensitive(t *testing.T) {
	clause := nrptManagedClause()
	if !strings.Contains(clause, "-ceq") || strings.Contains(clause, " -eq ") {
		t.Errorf("clause %q must use -ceq: -eq ignores case and matches another tool's rule", clause)
	}
	if !strings.Contains(clause, "'"+nrptComment+"'") {
		t.Errorf("clause %q must name the daemon's comment", clause)
	}
}

/*
The Windows teardown must fail when a rule is left behind.

THE GAP. The script ended in `$r.Count`, and with -Command the exit code follows the last statement,
which always succeeds. A Remove-DnsClientNrptRule that failed with a non-terminating error gave exit 0
where the plain removal gave exit 1, so Teardown returned nil and the sweep logged success while a
stale rule stayed. The script now stops on any error and reports how many rules it removed AND how
many are still present afterwards; Teardown fails when any remain.
*/
func TestTheNrptTeardownScriptStopsOnErrorAndCountsWhatRemains(t *testing.T) {
	script := nrptTeardownScript()
	if !strings.HasPrefix(script, "$ErrorActionPreference = 'Stop';") {
		t.Errorf("script must begin by stopping on the first error: %q", script)
	}
	if strings.Count(script, nrptManagedClause()) != 2 {
		t.Errorf("script must find our rules before AND after the removal: %q", script)
	}
	for _, want := range []string{"Remove-DnsClientNrptRule", `"$($r.Count) $left"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script %q must contain %q", script, want)
		}
	}
}

func TestTheNrptTeardownOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		out       string
		wantFlush bool
		wantErr   bool
	}{
		{"nothing to remove", "0 0\r\n", false, false},
		{"removed all", "3 0\n", true, false},
		{"output has other lines first", "warning\r\n2 0\r\n", true, false},
		{"a rule is left behind", "1 1\n", true, true},
		{"nothing removed and one remains", "0 2\n", false, true},
		{"unreadable output", "oops", true, true},
		{"empty output", "", true, true},
		{"negative numbers", "-1 0", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flush, err := nrptTeardownOutcome(tc.out)
			if flush != tc.wantFlush || (err != nil) != tc.wantErr {
				t.Errorf("nrptTeardownOutcome(%q) = flush %v, err %v; want flush %v, err %v",
					tc.out, flush, err, tc.wantFlush, tc.wantErr)
			}
		})
	}
}
