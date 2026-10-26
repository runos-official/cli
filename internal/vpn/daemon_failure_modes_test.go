package vpn

import (
	"errors"
	"strings"
	"testing"
)

/*
Steps that can fail after the tunnel is already up, and what each one leaves behind.

The rule these tests state: a step that fails must (1) say so in words a person can act on, (2) leave
the machine in a state the status describes truthfully, and (3) keep trying where trying can help.
They run the real daemon against a fake tunnel engine and a fake platform (daemon_dns_harness_test.go).
*/
func TestFailuresAfterTheTunnelIsUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		// arrange breaks one step.
		arrange func(h *dnsHarness)
		// wantErr is a fragment of the error up returns; empty means up succeeds.
		wantErr string
		// wantRunning: does status say a tunnel exists afterwards?
		wantRunning bool
		// wantRetry: is the poll loop still running, so the daemon keeps trying?
		wantRetry bool
		// wantDNSNot: text the DNS status must NOT carry (it would misreport the cause).
		wantDNSNot string
		// wantDNSHas: text the DNS status must carry.
		wantDNSHas string
	}{
		{
			name: "the interface cannot be created",
			arrange: func(h *dnsHarness) {
				createEngine = func(string, bool) (tunnelEngine, error) { return nil, errors.New("operation not permitted") }
			},
			wantErr: "operation not permitted", wantRunning: false, wantRetry: false,
		},
		{
			name:    "the WireGuard engine will not start",
			arrange: func(h *dnsHarness) { h.engine.upE = errors.New("device busy") },
			wantErr: "device busy", wantRunning: false, wantRetry: false,
		},
		{
			name:    "the address cannot be set",
			arrange: func(h *dnsHarness) { h.platform.addrErr = errors.New("ifconfig: bad address") },
			wantErr: "bad address", wantRunning: true, wantRetry: true,
			wantDNSNot: "the VPN is down",
		},
		{
			name:    "the WireGuard configuration is refused",
			arrange: func(h *dnsHarness) { h.engine.applyE = errors.New("invalid peer") },
			wantErr: "invalid peer", wantRunning: true, wantRetry: true,
			wantDNSNot: "the VPN is down",
		},
		{
			// Routes failed, so DNS was never attempted. The DNS line must not say "the VPN is down"
			// about a tunnel that is up.
			name:    "a route cannot be added",
			arrange: func(h *dnsHarness) { h.platform.routeErr = errors.New("route: not permitted") },
			wantErr: "not permitted", wantRunning: true, wantRetry: true,
			wantDNSNot: "the VPN is down", wantDNSHas: dnsNotSetUpText,
		},
		{
			name:    "the resolver files cannot be written",
			arrange: func(h *dnsHarness) { h.platform.reconcileErr = errDisk },
			wantErr: "permission denied", wantRunning: true, wantRetry: true,
			wantDNSHas: "permission denied",
		},
		{
			name:        "macOS has not listed the zones yet",
			arrange:     func(h *dnsHarness) {},
			wantRunning: true, wantRetry: true,
			wantDNSHas: dnsPendingText,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDNSHarness(t)
			tc.arrange(h)

			resp := h.up()

			if tc.wantErr == "" && resp.Error != "" || tc.wantErr != "" && !strings.Contains(resp.Error, tc.wantErr) {
				t.Fatalf("up error = %q, want it to contain %q", resp.Error, tc.wantErr)
			}
			st := h.status()
			if st.Running != tc.wantRunning {
				t.Errorf("running = %v, want %v", st.Running, tc.wantRunning)
			}
			h.d.mu.Lock()
			retrying := h.d.cancelPoll != nil
			h.d.mu.Unlock()
			if retrying != tc.wantRetry {
				t.Errorf("poll loop running = %v, want %v: the daemon must keep trying exactly when trying can help", retrying, tc.wantRetry)
			}
			if tc.wantDNSNot != "" && strings.Contains(st.DNS.Error, tc.wantDNSNot) {
				t.Errorf("dns error %q must not say %q about a tunnel that is up", st.DNS.Error, tc.wantDNSNot)
			}
			if tc.wantDNSHas != "" && !strings.Contains(st.DNS.Error, tc.wantDNSHas) {
				t.Errorf("dns error %q, want it to say %q", st.DNS.Error, tc.wantDNSHas)
			}
		})
	}
}

// The placeholder a stopped daemon shows and the text the routes check compares against are one
// string; this binds them.
func TestTheStoppedDaemonShowsTheDownText(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	h.d.Handle(Request{Op: OpDown})
	if got := h.status().DNS.Error; got != dnsDownText {
		t.Errorf("a stopped daemon's DNS error = %q, want %q", got, dnsDownText)
	}
}

// A teardown that fails leaves resolver entries pointing at a dead resolver. The person cannot see
// that, so the daemon log must say it.
func TestAFailedTeardownIsLogged(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	h.platform.teardownErr = errors.New("remove resolver: operation not permitted")

	logged := captureLog(t, func() { h.d.Handle(Request{Op: OpDown}) })

	if !strings.Contains(logged, "operation not permitted") {
		t.Errorf("the daemon log does not mention the failed teardown:\n%s", logged)
	}
}
