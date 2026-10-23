package vpn

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// blockingTeardownPlatform holds the start-up sweep until the test lets it go, which freezes the
// daemon inside its resume the way a slow first poll does.
type blockingTeardownPlatform struct {
	fakePlatform
	entered chan struct{}
	release chan struct{}
}

func (b *blockingTeardownPlatform) Teardown(iface string) error {
	close(b.entered)
	<-b.release
	return b.fakePlatform.Teardown(iface)
}

/*
The control socket exists while the daemon is still resuming, and requests wait for the resume.

THE GAP. The daemon bound its socket only after Resume, which takes seconds (an engine, a Conductor
poll, a DNS check). After `vpn restart` every CLI call in that window failed with "the RunOS VPN
service is not running ... Run 'sudo runos vpn install'", and the Desktop app could offer to
reinstall the service, which rewrites its definition. The service was running and about to answer.
Binding first and holding the daemon's lock through the resume turns that error into a short wait
with the true answer.
*/
func TestControlSocketAnswersWhileTheDaemonIsStillResuming(t *testing.T) {
	d := newTestDaemon(t)
	platform := &blockingTeardownPlatform{entered: make(chan struct{}), release: make(chan struct{})}
	d.platform = platform
	sock := shortSocketPath(t, "runos-vpn-first.sock")

	listenerCh := make(chan net.Listener, 1)
	go func() {
		listener, err := d.ServeAndResume(func() (net.Listener, error) { return Serve(d, sock, "", false) })
		if err != nil {
			t.Errorf("serve and resume: %v", err)
		}
		listenerCh <- listener
	}()

	select {
	case <-platform.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never reached its start-up sweep")
	}

	// The daemon is mid-resume. The socket must already be there, and a request must wait.
	answered := make(chan error, 1)
	go func() {
		_, err := NewClient(sock).Call(Request{Op: OpStatus})
		answered <- err
	}()
	select {
	case err := <-answered:
		t.Fatalf("a status request was answered during the resume (err=%v): it must wait for the resume to finish", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(platform.release)
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("the waiting request failed after the resume: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting request never completed")
	}
	if listener := <-listenerCh; listener != nil {
		listener.Close()
	}
}

/*
A request that waits on a long resume gets an answer, not a dropped connection.

serveConn gives each connection 60 s. A request queued behind a resume that runs longer used to
reach that deadline: the client read "EOF", and the request still ran later (a queued up, down or
logout executed after its caller had left). Requests now wait for the resume only up to
startRequestWait, then get a plain "still starting" answer and run nothing.
*/
func TestRequestDuringALongResumeGetsAStillStartingAnswerInTime(t *testing.T) {
	previous := startRequestWait
	startRequestWait = 300 * time.Millisecond
	t.Cleanup(func() { startRequestWait = previous })
	d := newTestDaemon(t)
	platform := &blockingTeardownPlatform{entered: make(chan struct{}), release: make(chan struct{})}
	d.platform = platform
	sock := shortSocketPath(t, "runos-vpn-longres.sock")
	listener, err := d.ServeAndResume(func() (net.Listener, error) { return Serve(d, sock, "", false) })
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	select {
	case <-platform.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the resume never started")
	}

	started := time.Now()
	_, err = NewClient(sock).call(Request{Op: OpDown}, 3*time.Second)

	if err == nil || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("err = %v, want a still-starting answer", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("the answer took %v; the wait must be bounded", time.Since(started))
	}
	var notRunning *NotRunningError
	if errors.As(err, &notRunning) || strings.Contains(err.Error(), "vpn install") {
		t.Fatalf("a starting service must not read as missing: %v", err)
	}

	// The refused down must not run later: after the resume the daemon is still as it was.
	close(platform.release)
	if _, err := NewClient(sock).call(Request{Op: OpStatus}, 5*time.Second); err != nil {
		t.Fatalf("status after the resume: %v", err)
	}
}
