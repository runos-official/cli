package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/vpn"
	"github.com/spf13/cobra"
)

func renderStatus(t *testing.T, status *vpn.Status) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	if err := printVPNStatus(command, status); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

/*
The Private DNS line says what is true: ready, being applied, or failed (FCR 1730).

A tunnel that is up while macOS applies the cluster's DNS settings is connected, so the status says so
in plain words instead of "unavailable". Desktop shows these words unchanged.
*/
func TestVPNStatusPrivateDNSStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dns     vpn.DNSStatus
		want    []string
		wantNot []string
	}{
		{
			name:    "ready",
			dns:     vpn.DNSStatus{Available: true, Mode: "native"},
			want:    []string{"Private DNS: available (native)"},
			wantNot: []string{"pending", "unavailable"},
		},
		{
			name:    "being applied by macOS",
			dns:     vpn.DNSStatus{Mode: "native", Pending: true, Error: "cluster names are still being applied by macOS"},
			want:    []string{"Private DNS: pending", vpn.DNSPendingNotice},
			wantNot: []string{"unavailable", "failed"},
		},
		{
			name:    "failed after the bound",
			dns:     vpn.DNSStatus{Mode: "native", Error: "macOS did not apply the private DNS settings for this cluster. The VPN is connected, but cluster names may not resolve. Turn the VPN off and on again."},
			want:    []string{"Private DNS: unavailable - macOS did not apply the private DNS settings", "Turn the VPN off and on again"},
			wantNot: []string{"pending", vpn.DNSPendingNotice},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderStatus(t, &vpn.Status{Running: true, Interface: "utun9", Address: "192.0.2.2/32", DNS: tc.dns})
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output lacks %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wantNot {
				if strings.Contains(got, bad) {
					t.Errorf("output must not contain %q:\n%s", bad, got)
				}
			}
			if strings.Contains(got, "`") {
				t.Errorf("output has a backtick:\n%s", got)
			}
		})
	}
}

// Released clients decode the status JSON: the old fields keep their names, and the new one is only
// there when it is true.
func TestDNSStatusJSONStaysCompatible(t *testing.T) {
	pending, _ := json.Marshal(vpn.DNSStatus{Mode: "native", Pending: true, Error: "x"})
	ready, _ := json.Marshal(vpn.DNSStatus{Available: true, Mode: "native"})
	if string(pending) != `{"available":false,"mode":"native","error":"x","pending":true}` {
		t.Errorf("pending JSON = %s", pending)
	}
	if string(ready) != `{"available":true,"mode":"native","error":""}` {
		t.Errorf("ready JSON = %s: the pending field must be omitted when false", ready)
	}
	var old struct {
		Available bool   `json:"available"`
		Mode      string `json:"mode"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(pending, &old); err != nil || old.Mode != "native" || old.Error != "x" {
		t.Errorf("a client that predates pending cannot read it: %+v %v", old, err)
	}
}
