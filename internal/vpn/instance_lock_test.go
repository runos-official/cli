package vpn

import (
	"errors"
	"testing"
	"time"
)

func TestSecondDaemonCannotTakeTheInstanceLock(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireInstanceLock(dir, 0)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	if _, err := AcquireInstanceLock(dir, 0); !errors.Is(err, ErrDaemonAlreadyRunning) {
		t.Fatalf("second lock error = %v, want ErrDaemonAlreadyRunning: two daemons would share one machine", err)
	}
}

func TestInstanceLockIsFreeAgainAfterRelease(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireInstanceLock(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	release()

	again, err := AcquireInstanceLock(dir, 0)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}

// A restart starts the new daemon while the old one is still finishing its teardown. The new one
// must wait for it rather than give up, or launchd throttles the restart.
func TestStartingDaemonWaitsForTheOldOneToLeave(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireInstanceLock(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()

	next, err := AcquireInstanceLock(dir, 5*time.Second)
	if err != nil {
		t.Fatalf("the new daemon gave up instead of waiting: %v", err)
	}
	next()
}
