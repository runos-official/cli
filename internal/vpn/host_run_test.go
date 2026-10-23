package vpn

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

func shortStopTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := stopTimeout
	stopTimeout = d
	t.Cleanup(func() { stopTimeout = previous })
}

/*
The stop itself has a time limit.

THE GAP. startStopGrace bounds a start that is still running when the signal arrives. A start that
had already returned left the stop unbounded: stop closes the daemon, which waits for the daemon
lock, and a resume holds that lock through external commands that each take up to 10 s. Their sum
can pass launchd's 20 s, and the process ended in SIGKILL with no word. A second SIGTERM during the
stop was swallowed. The limit is below launchd's, and a second signal ends the wait at once. Whatever
the stop did not finish is swept by the next start.
*/
func TestAStopThatCannotFinishDoesNotHoldTheHost(t *testing.T) {
	shortStopTimeout(t, 300*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sig := make(chan os.Signal, 2)
	done := make(chan error, 1)
	go func() {
		done <- runHost(func() (func(), error) {
			return func() { <-release }, nil
		}, sig)
	}()
	sig <- os.Interrupt

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stop that did not finish must exit with a reason")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the host is still waiting on a stop that cannot finish")
	}
}

func TestASecondSignalDuringTheStopEndsTheWait(t *testing.T) {
	shortStopTimeout(t, time.Minute)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sig := make(chan os.Signal, 2)
	done := make(chan error, 1)
	go func() {
		done <- runHost(func() (func(), error) {
			return func() { <-release }, nil
		}, sig)
	}()
	sig <- os.Interrupt
	time.Sleep(100 * time.Millisecond)
	sig <- os.Interrupt

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an interrupted stop must exit with a reason")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second signal during the stop was swallowed")
	}
}

// The case of the reviewer's report: the stop arrives in the middle of a resume that sits in an
// external command. The host leaves within the limit, and once the command ends the resume stops by
// itself instead of carrying on to bring a tunnel up.
func TestAStopInTheMiddleOfAResumeLeavesWithinTheLimitAndTheResumeStops(t *testing.T) {
	shortStopTimeout(t, 300*time.Millisecond)
	calls := stubEngine(t)
	d := newTestDaemon(t)
	platform := &blockingTeardownPlatform{entered: make(chan struct{}), release: make(chan struct{})}
	d.platform = platform
	activeSession(t, d, time.Now().Add(time.Hour))
	sock := shortSocketPath(t, "runos-vpn-stopmid.sock")
	sig := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- runHost(func() (func(), error) {
			l, err := d.ServeAndResume(func() (net.Listener, error) { return Serve(d, sock, "", false) })
			if err != nil {
				return nil, err
			}
			return func() { l.Close(); d.Close() }, nil
		}, sig)
	}()
	<-platform.entered
	sig <- os.Interrupt

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the host stayed blocked behind the resume")
	}

	close(platform.release) // the external command ends
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-d.ready:
			if *calls != 0 {
				t.Fatalf("tunnel requests = %d: the resume went on after the stop", *calls)
			}
			return
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("the resume never ended after its command returned")
}

// recordingApplyPlatform notes each step of an apply and can raise the stop between them.
type recordingApplyPlatform struct {
	fakePlatform
	d          *Daemon
	routes     int
	dnsApplied int
	stopAfter  int
}

func (r *recordingApplyPlatform) AddRoute(string, netip.Prefix) error {
	r.routes++
	if r.routes == r.stopAfter {
		r.d.closed.Store(true)
	}
	return nil
}

func (r *recordingApplyPlatform) ReconcileDNS(string, netip.Addr, []ResolverPlan) (DNSStatus, error) {
	r.dnsApplied++
	return DNSStatus{Available: true, Mode: "native"}, nil
}

// An apply that finds a stop waiting does not go on to the remaining routes or to the DNS step.
func TestAnApplyStopsBetweenStepsWhenTheDaemonIsClosing(t *testing.T) {
	d := newTestDaemon(t)
	platform := &recordingApplyPlatform{d: d, stopAfter: 1}
	d.platform = platform
	d.engine = &engine{name: "utun9"}
	plan := Plan{Routes: []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}}

	err := d.convergeRoutesAndDNSLocked(nil, plan)

	if !errors.Is(err, errStopping) {
		t.Fatalf("err = %v, want errStopping", err)
	}
	if platform.routes != 1 {
		t.Errorf("routes added = %d, want 1: the apply carried on after the stop", platform.routes)
	}
	if platform.dnsApplied != 0 {
		t.Errorf("DNS applied %d times after the stop, want 0", platform.dnsApplied)
	}
}

func TestAnApplyOnAnOpenDaemonStillRunsEveryStep(t *testing.T) {
	d := newTestDaemon(t)
	platform := &recordingApplyPlatform{d: d}
	d.platform = platform
	d.engine = &engine{name: "utun9"}
	plan := Plan{Routes: []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"),
	}}

	if err := d.convergeRoutesAndDNSLocked(nil, plan); err != nil {
		t.Fatal(err)
	}
	if platform.routes != 2 || platform.dnsApplied != 1 {
		t.Errorf("routes=%d dns=%d, want 2 and 1", platform.routes, platform.dnsApplied)
	}
}
