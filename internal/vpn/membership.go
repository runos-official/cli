package vpn

import "fmt"

/*
Whether a connect may proceed, decided from the document the daemon already holds.

WHY THIS EXISTS. `vpn connect` used to write the device into any cluster's connected set without
looking. On a cluster with no VPN server that produced a state with no exit: the device is
recorded as connected, nothing routes, and `vpn status` reads "connected but unreachable" until
somebody thinks to disconnect. Measured on the operator's own machine 2026-08-18, where cl3 had
been sitting in exactly that state, and reported as "i am connecting to nothing it seems yet it
says i can connect".

The daemon holds the answer before it asks: the document carries every cluster of the account with
its server's reachability and the reason. Refusing here turns a silent dead end into a sentence.

WHAT IT DELIBERATELY DOES NOT DO. It refuses only on evidence it actually has. No document, or a
cluster the document has not seen, is not a refusal: a document may simply be older than a cluster
someone just built, and inventing a verdict from missing evidence is the failure this codebase
keeps paying for. Let those through and let Conductor answer.
*/
func connectRefusal(doc *Document, cid string) string {
	if doc == nil {
		return ""
	}
	for _, c := range doc.Clusters {
		if c.CID != cid {
			continue
		}
		if c.Server.Reachable {
			return ""
		}
		reason := c.Server.Reason
		if reason == "" {
			reason = "its VPN server is not reachable"
		}
		return fmt.Sprintf(
			"%s cannot be connected: %s. Connecting would record this device against %s and route nothing, so it is refused rather than left looking connected.",
			cid, reason, cid)
	}
	return ""
}

// disconnectIsGuarded records, for the test that pins it, that the refusal above is consulted on
// the CONNECT path only. Devices already stuck in the dead state must always be able to leave it;
// refusing a disconnect because the cluster is unreachable would trap them there for good.
const disconnectIsGuarded = false
