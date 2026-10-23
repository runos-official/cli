//go:build darwin

package vpn

import "testing"

// A restart waits for the old daemon to exit, so it runs under the long limit. The runner is a fake:
// no launchctl runs.
func TestLaunchdRestartRunsUnderTheLongLimit(t *testing.T) {
	calls := recordLongCalls(t)

	if err := (launchdService{}).Restart(); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want exactly one launchctl kickstart", *calls)
	}
	call := (*calls)[0]
	if call.name != "launchctl" || call.args[0] != "kickstart" || call.timeout != serviceCommandLimit("launchctl") {
		t.Errorf("call = %+v, want launchctl kickstart under the service limit", call)
	}
}
