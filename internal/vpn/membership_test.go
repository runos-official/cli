package vpn

import "testing"

// docWith builds a device document holding one cluster in the state a test needs.
func docWith(cid string, reachable bool, reason string) *Document {
	doc := &Document{}
	c := DocumentCluster{CID: cid, Name: "a cluster"}
	c.Server.Reachable = reachable
	c.Server.Reason = reason
	doc.Clusters = append(doc.Clusters, c)
	return doc
}

/*
The operator's report, in one sentence: "i am connecting to nothing it seems yet it says i can
connect."

Connecting to a cluster with no VPN server is not a connection. It writes the device into that
cluster's connected set, nothing routes, and `vpn status` then reads "connected but unreachable"
for ever. That is a dead state a person cannot fix by waiting, and the only way out is a
disconnect they have to think of themselves. It should never have been reachable: the daemon
already holds the document that says the server is missing.
*/
func TestConnectIsRefusedWhenTheClusterHasNoVpnServer(t *testing.T) {
	doc := docWith("cl3", false, "no VPN server installed")

	refusal := connectRefusal(doc, "cl3")

	if refusal == "" {
		t.Fatal("connecting to a cluster with no VPN server must be refused, not accepted")
	}
	if !contains(refusal, "no VPN server installed") {
		t.Errorf("the refusal must carry the server's own reason, got %q", refusal)
	}
	if !contains(refusal, "cl3") {
		t.Errorf("the refusal must name the cluster, got %q", refusal)
	}
}

func TestConnectIsAllowedWhenTheServerIsThere(t *testing.T) {
	if refusal := connectRefusal(docWith("z42", true, ""), "z42"); refusal != "" {
		t.Fatalf("a reachable cluster must connect, got refusal %q", refusal)
	}
}

/*
Two cases that must NOT be refused, because refusing them would be inventing a verdict from
missing evidence rather than reading one.

A daemon with no document yet knows nothing; a cluster absent from the document may simply be
newer than the last poll. In both, let the request through and let the server answer.
*/
func TestConnectIsNotRefusedOnEvidenceTheDaemonDoesNotHave(t *testing.T) {
	if refusal := connectRefusal(nil, "z42"); refusal != "" {
		t.Errorf("no document means no verdict, got %q", refusal)
	}
	if refusal := connectRefusal(docWith("z42", true, ""), "new1"); refusal != "" {
		t.Errorf("a cluster the document has not seen yet is not a refusal, got %q", refusal)
	}
}

/*
Disconnect is never refused, and this is the important half.

Devices are already stuck in the dead state this change prevents (cl3, measured 2026-08-18).
Applying the same check to disconnect would trap them there permanently: refusing to disconnect
from an unreachable cluster because it is unreachable.
*/
func TestDisconnectIsNeverRefused(t *testing.T) {
	if refusal := connectRefusal(docWith("cl3", false, "no VPN server installed"), "cl3"); refusal == "" {
		t.Fatal("guard the connect side")
	}
	// The daemon only consults connectRefusal on the connect path; this test pins the intent so a
	// later edit that applies it to both paths fails here rather than in someone's terminal.
	if disconnectIsGuarded {
		t.Fatal("disconnect must never be refused: it is the only way out of the dead state")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
