package vpn

import (
	"testing"
	"time"
)

// The limits are the point: they sit above what the service manager takes to stop the old daemon.
func TestServiceCommandsGetLimitsAboveTheServiceManagerStopTime(t *testing.T) {
	if got := serviceCommandLimit("launchctl"); got < 30*time.Second {
		t.Errorf("launchctl limit = %v, want at least 30s (launchd waits up to 20s for the daemon)", got)
	}
	if got := serviceCommandLimit("systemctl"); got < 100*time.Second {
		t.Errorf("systemctl limit = %v, want at least 100s (systemd waits up to 90s for the daemon)", got)
	}
	if serviceCommandLimit("launchctl") <= commandTimeout {
		t.Errorf("a service command must outlast an ordinary one")
	}
}

type recordedCall struct {
	timeout time.Duration
	name    string
	args    []string
}

func recordLongCalls(t *testing.T) *[]recordedCall {
	t.Helper()
	var calls []recordedCall
	previous := runLongFn
	runLongFn = func(timeout time.Duration, name string, args ...string) ([]byte, error) {
		calls = append(calls, recordedCall{timeout, name, args})
		return nil, nil
	}
	t.Cleanup(func() { runLongFn = previous })
	return &calls
}
