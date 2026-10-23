//go:build linux

package vpn

import "testing"

func TestSystemdRestartRunsUnderTheLongLimit(t *testing.T) {
	calls := recordLongCalls(t)

	if err := (systemdService{}).Restart(); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want exactly one systemctl restart", *calls)
	}
	call := (*calls)[0]
	if call.name != "systemctl" || call.args[0] != "restart" || call.timeout != serviceCommandLimit("systemctl") {
		t.Errorf("call = %+v, want systemctl restart under the service limit", call)
	}
}
