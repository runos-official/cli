package roidauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func noSleep(time.Duration) {}

func TestDeviceLoginBindsTheKeyAndPollsUntilApproved(t *testing.T) {
	f := newFakeRoid(t)
	f.pendingFor = 2
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(f.srv.URL)
	c.Sleep = noSleep
	start, err := c.StartDevice(key)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.UserCode == "" || !strings.Contains(start.VerificationURIComplete, start.UserCode) {
		t.Fatalf("start = %+v", start)
	}
	if f.jkt != key.Thumbprint() {
		t.Fatalf("device flow bound to %q, want the key thumbprint %q", f.jkt, key.Thumbprint())
	}
	f.approve()
	pending := 0
	tok, err := c.PollDevice(context.Background(), start, key, func() { pending++ })
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.HasPrefix(tok.AccessToken, "at_") || !strings.HasPrefix(tok.RefreshToken, "rt_") {
		t.Fatalf("tokens = %+v", tok)
	}
	if pending != 2 || f.polls != 3 {
		t.Fatalf("pending callbacks %d, polls %d; want 2 and 3", pending, f.polls)
	}
}

func TestDevicePollKeepsTheIntervalAtLeastFiveSeconds(t *testing.T) {
	f := newFakeRoid(t)
	f.pendingFor = 1
	key, _ := NewKey()
	c := NewClient(f.srv.URL)
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	start, err := c.StartDevice(key)
	if err != nil {
		t.Fatal(err)
	}
	start.Interval = 1 // a server must never make the CLI poll faster than the RFC floor
	f.approve()
	if _, err := c.PollDevice(context.Background(), start, key, nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range slept {
		if d < 5*time.Second {
			t.Fatalf("slept %v between polls; the floor is 5s", d)
		}
	}
}

func TestDevicePollStopsWhenTheCodeExpires(t *testing.T) {
	f := newFakeRoid(t)
	key, _ := NewKey()
	c := NewClient(f.srv.URL)
	now := time.Now()
	c.Now = func() time.Time { return now }
	c.Sleep = func(d time.Duration) { now = now.Add(d) }
	start, err := c.StartDevice(key)
	if err != nil {
		t.Fatal(err)
	}
	start.ExpiresIn = 12
	_, err = c.PollDevice(context.Background(), start, key, nil)
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "expired_token" {
		t.Fatalf("err = %v, want expired_token", err)
	}
}

func TestDevicePollReportsADeniedApproval(t *testing.T) {
	f := newFakeRoid(t)
	key, _ := NewKey()
	c := NewClient(f.srv.URL)
	c.Sleep = noSleep
	start, err := c.StartDevice(key)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deviceCode = "dc_other" // the server no longer knows this code
	f.mu.Unlock()
	_, err = c.PollDevice(context.Background(), start, key, nil)
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" {
		t.Fatalf("err = %v, want invalid_grant", err)
	}
}

func TestProofHeaderIsRefusedForAnotherPurpose(t *testing.T) {
	f := newFakeRoid(t)
	key, _ := NewKey()
	c := NewClient(f.srv.URL)
	c.Sleep = noSleep
	start, err := c.StartDevice(key)
	if err != nil {
		t.Fatal(err)
	}
	f.approve()
	tok, err := c.PollDevice(context.Background(), start, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A proof made for the access token cannot spend the refresh token.
	other, _ := NewKey()
	if _, err := c.Refresh(tok.RefreshToken, other); err == nil {
		t.Fatal("refresh with a proof from another key succeeded")
	}
	if _, err := c.Refresh(tok.RefreshToken, key); err != nil {
		t.Fatalf("refresh with the bound key: %v", err)
	}
}
