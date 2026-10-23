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
