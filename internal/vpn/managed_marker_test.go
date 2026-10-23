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
	for _, want := range []string{"Remove-DnsClientNrptRule", `"$removed $left"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script %q must contain %q", script, want)
		}
	}
}

/*
One failed removal must not abort the rest.

With $ErrorActionPreference = 'Stop' a throw ends the pipeline, so the first rule that could not be
removed left every later rule in place. Each removal now has its own try/catch: a failure is
reported on a line of its own, the loop goes on, and the count of what is STILL present decides
whether Teardown fails.
*/
func TestTheNrptTeardownScriptRemovesEachRuleInItsOwnTry(t *testing.T) {
	script := nrptTeardownScript()
	loop := script[strings.Index(script, "foreach"):strings.Index(script, "$left =")]
	for _, want := range []string{"try {", "Remove-DnsClientNrptRule -Force", "$removed++", "} catch {", "failed:"} {
		if !strings.Contains(loop, want) {
			t.Errorf("the per-rule loop %q must contain %q", loop, want)
		}
	}
	if strings.Index(loop, "try {") > strings.Index(loop, "Remove-DnsClientNrptRule") {
		t.Errorf("the removal must sit inside the try: %q", loop)
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
		{"one failed and one removed", "failed: access denied\r\n1 1\r\n", true, true},
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

// The reason a removal failed is kept in the error, so the log says why a rule remains.
func TestTheNrptTeardownErrorCarriesTheFailureText(t *testing.T) {
	_, err := nrptTeardownOutcome("failed: access denied\r\n0 1\r\n")
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v, want the failure text in it", err)
	}
}
