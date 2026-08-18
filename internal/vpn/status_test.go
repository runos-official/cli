package vpn

import "testing"

func TestVPNStatusSchemaTwoIncludesPrivateDNSState(t *testing.T) {
	daemon := &Daemon{
		state:        &State{},
		lastPollErr:  "poll failed",
		lastApplyErr: "apply failed",
		dns: DNSStatus{
			Available: true,
			Mode:      "local-proxy",
		},
	}
	status := daemon.statusLocked()
	if status.SchemaVersion != 2 {
		t.Fatalf("schema version is %d, want 2", status.SchemaVersion)
	}
	if !status.DNS.Available || status.DNS.Mode != "local-proxy" || status.DNS.Error != "" {
		t.Fatalf("DNS status is %+v", status.DNS)
	}
	if status.LastPollErr != "poll failed" || status.LastApplyErr != "apply failed" {
		t.Fatalf("status combined poll and apply failures: %+v", status)
	}
}
