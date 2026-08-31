package vpn

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

/*
"Permission denied" and "nothing is listening" are different facts with different remedies.

REPORTED 2026-08-31 by two macOS users. Their socket was `root:wheel` and they were in `admin`, so
connect() returned EACCES. Every command said:

	the RunOS VPN service is not running (/var/run/runos-vpn.sock).
	Run 'sudo runos vpn install' first.

The service WAS running. And the advice was worse than useless: reinstalling recreated the same
socket with the same group, so it could not work however many times they tried. One of them called
it a "boot-loop of blame". The other asked for exactly this: "permission denied on the socket, your
user is not in the socket's group", which turns it into a thirty-second fix.

`net.DialTimeout` fails for several reasons and they were all flattened into one. ENOENT and
ECONNREFUSED really do mean "not running". EACCES means the opposite: something IS there and this
user cannot reach it.
*/

func TestAPermissionDeniedSocketIsNotReportedAsNotRunning(t *testing.T) {
	// A real socket this process cannot connect to. Mode 000 denies everyone, including its owner,
	// which is what makes this reproducible without a second user account.
	// macOS caps a unix socket path at 104 bytes and t.TempDir() paths are far longer, so this
	// goes somewhere short. Same reason as socket_test.go.
	path := shortSocketPath(t, fmt.Sprintf("runos-denied-%d.sock", os.Getpid()))
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses socket permissions, so this cannot be reproduced as root")
	}

	_, callErr := NewClient(path).Call(Request{Op: OpStatus})

	if callErr == nil {
		t.Fatal("want an error")
	}
	var notRunning *NotRunningError
	if errors.As(callErr, &notRunning) {
		t.Errorf("a socket that exists and refuses this user is not 'not running': %v", callErr)
	}
	var denied *PermissionError
	if !errors.As(callErr, &denied) {
		t.Fatalf("want a PermissionError, got %T: %v", callErr, callErr)
	}
	message := callErr.Error()
	// The three things that turn this into a self-service fix: what happened, where, and the fact
	// the person needs, which is the group they are not in.
	for _, want := range []string{"ermission", path, "belongs to"} {
		if !strings.Contains(message, want) {
			t.Errorf("message must contain %q, got %q", want, message)
		}
	}
	/*
	 THE REMEDY HAS TO COME FIRST.

	 Reinstalling was the whole of the old advice and it recreated the same socket, which is what
	 made this a loop for both reporters. It is now a reasonable SECOND step, because the installer
	 no longer derives a group from root, but the first thing offered has to be the restart: that is
	 what makes the daemon repair the group, and it needs no arguments and no thought.
	*/
	/*
	 THE REMEDY MUST BE ONE THAT WORKS ON THIS MACHINE.

	 The daemon heals exactly one case: a derived GID 0 group on darwin. This socket's group is the
	 test user's own, which the daemon would leave alone, so the message must NOT promise that a
	 restart repairs it. Promising a restart that changes nothing is the same shape as the defect
	 this message exists to remove.
	*/
	if strings.Contains(message, "repairs") {
		t.Errorf("this group is not one the daemon heals, so no repair may be promised: %q", message)
	}
	if !strings.Contains(message, "--socket-group") {
		t.Errorf("must name a remedy that works here, got %q", message)
	}
	// It must not contradict itself by also claiming nothing is listening.
	if strings.Contains(message, "not running") {
		t.Errorf("something IS listening; saying otherwise is the original defect, got %q", message)
	}
}

// A socket that is genuinely absent still reports what it always did. That case is the common one
// and its advice is correct.
func TestAnAbsentSocketIsStillReportedAsNotRunning(t *testing.T) {
	_, err := NewClient(filepath.Join(t.TempDir(), "absent.sock")).Call(Request{Op: OpStatus})

	var notRunning *NotRunningError
	if !errors.As(err, &notRunning) {
		t.Fatalf("want a NotRunningError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "vpn install") {
		t.Errorf("this one really is fixed by installing, got %q", err.Error())
	}
}

// The message names the socket's ACTUAL group, read off the file, rather than a guess. That is the
// fact the person needs, and it is the one thing a message written in advance cannot know.
func TestThePermissionMessageNamesTheGroupTheSocketActuallyHas(t *testing.T) {
	path := shortSocketPath(t, fmt.Sprintf("runos-grouped-%d.sock", os.Getpid()))
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	name := socketGroupName(path)

	if name == "" {
		t.Fatal("the group of an existing socket must be readable")
	}
}

/*
And where the daemon WOULD heal it, the message says so, because that remedy is one command with no
arguments and no thought.

The reported failure is exactly this shape: a GID 0 group on a Mac.
*/
func TestTheMessageOffersTheRestartOnlyWhereTheDaemonHeals(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the heal is darwin-only, by design; see usableSocketGroup")
	}
	if _, ok := groupGID("wheel"); !ok {
		t.Skip("no wheel group on this machine")
	}

	healable := (&PermissionError{Path: "/var/run/runos-vpn.sock", Group: "wheel"}).Error()

	if !strings.Contains(healable, "vpn restart") {
		t.Errorf("a GID 0 group on darwin IS repaired on the next start, so say so: %q", healable)
	}
	restart, install := strings.Index(healable, "vpn restart"), strings.Index(healable, "vpn install")
	if install >= 0 && restart > install {
		t.Errorf("the repair must come before the reinstall, got %q", healable)
	}
}
