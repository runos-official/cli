package vpn

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/desktopcontract"
)

/*
A daemon and a CLI are different binaries that update at different moments: `runos update` replaces
the file, and launchd keeps the old daemon loaded until something restarts it. So the status reply
crosses a version boundary in both directions, and RunOS Desktop reads whatever the CLI re-encodes.

These fixtures are the Status shapes from this file's git history, copied as the old daemons wrote
them (internal/vpn/protocol.go at commits c0ea0b1, 911d84a and de14046), including the unset times
as "0001-01-01T00:00:00Z" and the `"clusters": null` an empty document produced.
*/

const (
	// c0ea0b1: the first daemon. No schemaVersion, no dns.
	statusReplyV0 = `{"running":true,"interface":"utun7","address":"192.0.2.10/32","deviceId":"d1","accountId":"aaaaa",
	  "session":{"present":true,"expiresAt":"2026-10-27T10:00:00Z","loginRequired":false},
	  "clusters":[{"cid":"c1","name":"one","connected":true,"reachable":true,"endpoint":"192.0.2.1:51820",
	    "allowedIps":["198.51.100.0/24"],"resolver":"198.51.100.1","zones":["one.example.test"],"peeredWith":[],
	    "peerUp":true,"lastHandshake":"2026-10-26T09:59:00Z","rxBytes":10,"txBytes":20}],
	  "revision":"r1","lastPollAt":"2026-10-26T09:59:30.123456789+02:00","version":"dev-2026-08-01T10:00:00Z"}`

	// 911d84a: schemaVersion 1. Still no dns, and a document with no clusters encoded null.
	statusReplyV1 = `{"schemaVersion":1,"running":false,
	  "session":{"present":false,"expiresAt":"0001-01-01T00:00:00Z","loginRequired":false},
	  "clusters":null,"lastPollAt":"0001-01-01T00:00:00Z","version":"1.10.0"}`

	// de14046: schemaVersion 2 adds dns. This one has a cluster whose lists the document left out.
	statusReplyV2 = `{"schemaVersion":2,"running":true,"interface":"utun7","address":"192.0.2.10/32",
	  "session":{"present":true,"expiresAt":"2026-10-27T10:00:00Z","loginRequired":false},
	  "dns":{"available":false,"mode":"unavailable","error":"private DNS has not been set up yet"},
	  "clusters":[{"cid":"c2","name":"two","connected":false,"reachable":false,"reason":"no VPN server",
	    "allowedIps":null,"zones":null,"peeredWith":null,"peerUp":false,
	    "lastHandshake":"0001-01-01T00:00:00Z","rxBytes":0,"txBytes":0}],
	  "version":"1.12.0"}`
)

// OLD DAEMON, NEW CLI: every reply decodes into the current Status and re-encodes to a document
// that still honours the contract. Nothing becomes null, no required key goes missing, and a field
// the old daemon did not know comes out as its zero value rather than disappearing.
func TestOldDaemonReplyReEncodesWithinTheContract(t *testing.T) {
	cases := []struct {
		name, reply string
		schema      int
		clusters    int
	}{
		{"schema 0 (no schemaVersion, no dns)", statusReplyV0, 0, 1},
		{"schema 1 (null clusters)", statusReplyV1, 1, 0},
		{"schema 2 (null cluster lists)", statusReplyV2, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var status Status
			if err := json.Unmarshal([]byte(tc.reply), &status); err != nil {
				t.Fatalf("the current Status cannot read this reply: %v", err)
			}
			if status.SchemaVersion != tc.schema || len(status.Clusters) != tc.clusters {
				t.Fatalf("decoded schemaVersion %d with %d clusters, want %d and %d",
					status.SchemaVersion, len(status.Clusters), tc.schema, tc.clusters)
			}
			out, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			desktopcontract.Check(t, tc.name, out, desktopcontract.VPNStatus)
		})
	}
}

// What the old CLI would decode: the Status of commit 911d84a, copied. It has no dns and no pending.
type statusV1 struct {
	SchemaVersion int    `json:"schemaVersion"`
	Running       bool   `json:"running"`
	Interface     string `json:"interface"`
	Session       struct {
		Present       bool      `json:"present"`
		ExpiresAt     time.Time `json:"expiresAt"`
		LoginRequired bool      `json:"loginRequired"`
	} `json:"session"`
	Clusters []struct {
		CID        string   `json:"cid"`
		Name       string   `json:"name"`
		PeeredWith []string `json:"peeredWith"`
		PeerUp     bool     `json:"peerUp"`
		RxBytes    int64    `json:"rxBytes"`
	} `json:"clusters"`
	Version string `json:"version"`
}

// NEW DAEMON, OLD CLI: the current reply, fully populated, still decodes into the old shape and
// carries the same facts. Unknown keys (dns, pending) are ignored by the old reader, and a time it
// reads is a valid timestamp, so an omitted zero time cannot break it.
func TestNewDaemonReplyStillDecodesInTheOldShape(t *testing.T) {
	for name, status := range map[string]*Status{"populated": populatedStatus(), "zero": {}} {
		t.Run(name, func(t *testing.T) {
			out, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			var old statusV1
			if err := json.Unmarshal(out, &old); err != nil {
				t.Fatalf("the old Status shape cannot read the current reply: %v\n%s", err, out)
			}
			if old.Running != status.Running || old.Version != status.Version || len(old.Clusters) != len(status.Clusters) {
				t.Errorf("old reader saw %+v, want the facts of %+v", old, status)
			}
		})
	}
}
