package vpn

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/desktopcontract"
)

/*
`runos vpn status --json` is read by RunOS Desktop with strict types, so what matters is the bytes
on the wire and not the Go struct: a nil slice is `null`, an unset time is "0001-01-01T00:00:00Z",
and a fraction of a second depends on the macOS release. The 2026-10-26 report was one such null
(`"clusters": null`) and it blanked the whole menu with no error. Every case here encodes the real
type and runs the bytes through the documented table in internal/desktopcontract, so a new field
has to be added to the table and a null cannot slip out.
*/

func encodeStatus(t *testing.T, s *Status) []byte {
	t.Helper()
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func populatedStatus() *Status {
	s := &Status{}
	desktopcontract.Populate(s)
	return s
}

// A daemon whose conductor document leaves the optional lists out: the realistic way for every
// per-cluster slice to be nil at once.
func statusFromSparseDocument(t *testing.T) *Status {
	t.Helper()
	doc := &Document{Clusters: []DocumentCluster{{CID: "c1", Name: "one", Connected: true}}}
	d := &Daemon{state: &State{}, doc: doc, version: "1.0.0"}
	return d.statusLocked()
}

func TestVPNStatusMatchesTheDesktopContract(t *testing.T) {
	cases := []struct {
		name   string
		status func(*testing.T) *Status
	}{
		{"zero value", func(*testing.T) *Status { return &Status{} }},
		{"fully populated", func(*testing.T) *Status { return populatedStatus() }},
		{"daemon status, no document yet", func(*testing.T) *Status {
			return (&Daemon{state: &State{}, version: "1.0.0"}).statusLocked()
		}},
		{"daemon status, sparse document", statusFromSparseDocument},
		{"one empty cluster", func(*testing.T) *Status { return &Status{Clusters: []ClusterStatus{{}}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desktopcontract.Check(t, tc.name, encodeStatus(t, tc.status(t)), desktopcontract.VPNStatus)
		})
	}
}

// A zero time is "never", and Desktop would otherwise read year 1 as a real date. The key is left
// out, which Desktop's optional Date reads as nothing.
func TestVPNStatusOmitsTimesThatWereNeverSet(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(encodeStatus(t, &Status{Clusters: []ClusterStatus{{}}}), &doc); err != nil {
		t.Fatal(err)
	}
	session := doc["session"].(map[string]any)
	cluster := doc["clusters"].([]any)[0].(map[string]any)
	for name, has := range map[string]bool{
		"lastPollAt":               doc["lastPollAt"] != nil,
		"session.expiresAt":        session["expiresAt"] != nil,
		"clusters[].lastHandshake": cluster["lastHandshake"] != nil,
	} {
		if has {
			t.Errorf("%s is written for a time that was never set", name)
		}
	}
}

// Times are written as UTC whole seconds whatever zone and precision the daemon held them in.
func TestVPNStatusWritesTimesAsUTCWholeSeconds(t *testing.T) {
	when := time.Date(2026, 10, 26, 14, 30, 15, 987654321, time.FixedZone("test", 2*3600))
	s := &Status{LastPollAt: when, Clusters: []ClusterStatus{{LastHandshake: when}}}
	s.Session.ExpiresAt = when
	var doc struct {
		LastPollAt string `json:"lastPollAt"`
		Session    struct {
			ExpiresAt string `json:"expiresAt"`
		} `json:"session"`
		Clusters []struct {
			LastHandshake string `json:"lastHandshake"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal(encodeStatus(t, s), &doc); err != nil {
		t.Fatal(err)
	}
	const want = "2026-10-26T12:30:15Z"
	for name, got := range map[string]string{
		"lastPollAt": doc.LastPollAt, "session.expiresAt": doc.Session.ExpiresAt, "lastHandshake": doc.Clusters[0].LastHandshake,
	} {
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
