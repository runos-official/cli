package dynacmd

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Linux provides a PTY without adding a dependency to production builds.
func evictionTestTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	if !term.IsTerminal(int(slave.Fd())) {
		t.Fatal("PTY slave is not a terminal")
	}
	return master, slave
}

func TestEvictionHostnameTimeoutOnBothSurfaces(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive=%v", interactive), func(t *testing.T) {
			useNodeNameConfig(t, &config.Config{})
			loadNodeNameConfig = func() (*config.Config, error) {
				time.Sleep(nodeNameDeadline + 100*time.Millisecond)
				return nil, nil
			}
			wait := waitForNodeNameWorker(t)
			defer wait()
			var input *os.File
			if interactive {
				master, slave := evictionTestTerminal(t)
				input = slave
				if _, err := master.WriteString("n\n"); err != nil {
					t.Fatal(err)
				}
			} else {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				input = reader
				writer.Close()
				t.Cleanup(func() { reader.Close() })
			}
			previous := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = previous }()
			c := evictionLeaf(t)
			var refusal error
			started := time.Now()
			stderr := captureStderr(t, func() { refusal = c.RunE(c, nil) })
			elapsed := time.Since(started)
			if elapsed < nodeNameDeadline || elapsed > nodeNameDeadline+400*time.Millisecond {
				t.Errorf("elapsed=%v", elapsed)
			}
			if refusal == nil {
				t.Fatal("command proceeded after timeout")
			}
			line := stderr
			if !interactive {
				if stderr != "" {
					t.Errorf("unexpected stderr=%q", stderr)
				}
				line = refusal.Error()
			}
			if !strings.Contains(line, "hostname=host-new"+evictionLookupFailed) || strings.Contains(line, "no record") || strings.Contains(line, "Warning:") {
				t.Errorf("unexpected timeout text: %q", line)
			}
		})
	}
}

func TestEvictionHostnameInteractiveMatrix(t *testing.T) {
	for _, tc := range evictionCases() {
		t.Run(tc.name, func(t *testing.T) {
			requests := useEvictionCase(t, tc)
			c := evictionLeaf(t)
			master, slave := evictionTestTerminal(t)
			previous := os.Stdin
			os.Stdin = slave
			defer func() { os.Stdin = previous }()
			if _, err := master.WriteString("n\n"); err != nil {
				t.Fatal(err)
			}
			var refusal error
			stderr := captureStderr(t, func() { refusal = c.RunE(c, nil) })
			want := "About to run `storage-groups evict-node` against hostname=host-new" + tc.want + " acknowledge-data-loss=true\n" +
				"This is irreversible (the conductor may not be able to restore deleted state).\nProceed? [y/N] "
			if stderr != want || refusal == nil || !strings.Contains(refusal.Error(), "cancelled") {
				t.Fatalf("stderr=%q; refusal=%v; want=%q", stderr, refusal, want)
			}
			if requests.Load() != 1 {
				t.Fatalf("reads=%d", requests.Load())
			}
		})
	}
}
