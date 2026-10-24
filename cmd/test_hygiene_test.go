package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/vpn"
)

/*
An account change in a test binary never reaches the real VPN daemon.

MEASURED twice on a developer machine: running this package's tests took the real tunnel down, and
the control plane logged the real VPN session ending in the same minute. An account-change test that
passed no --socket made disconnectVPNForAccountChange dial the default socket, which is the real
root daemon, and send it `down`. With no socket given, the call must end as "not running" against a
path that is not the real daemon's.
*/
func TestAnAccountChangeWithNoSocketNeverReachesTheRealDaemon(t *testing.T) {
	result := disconnectVPNForAccountChange("", "aaaaa", "bbbbb")
	if result.State != vpnStateNotRunning {
		t.Fatalf("state = %q (%s), want %q: the call reached a daemon", result.State, result.Message, vpnStateNotRunning)
	}
	if strings.Contains(result.Message, vpn.SocketPath) {
		t.Fatalf("the call named the real daemon socket: %q", result.Message)
	}
}
