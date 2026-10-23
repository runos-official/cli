package cmd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

/*
A wedged daemon must not stall `runos update`.

The update asks the daemon for its status before the download, to know whether a tunnel is up.
That call used the socket client's 120 s timeout, so a daemon that accepted the connection and never
answered held the update for two minutes with no message. The tunnel notice is a courtesy; the
update comes first.
*/
func TestDaemonStatusGivesUpQuicklyOnADaemonThatNeverAnswers(t *testing.T) {
	previous := daemonStatusTimeout
	daemonStatusTimeout = 300 * time.Millisecond
	t.Cleanup(func() { daemonStatusTimeout = previous })

	sock := filepath.Join(os.TempDir(), "runos-wedged-status.sock")
	_ = os.Remove(sock)
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close(); _ = os.Remove(sock) })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn // accepted and never answered
		}
	}()
	cmd := &cobra.Command{Use: "update"}
	registerSocketFlag(cmd)
	if err := cmd.Flags().Set("socket", sock); err != nil {
		t.Fatal(err)
	}

	got := make(chan struct{}, 1)
	go func() {
		if status := daemonStatus(cmd); status != nil {
			t.Errorf("status = %+v, want nil: the daemon never answered", status)
		}
		got <- struct{}{}
	}()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("daemonStatus is still waiting on a daemon that never answers")
	}
}
