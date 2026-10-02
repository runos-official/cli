package vpn

import (
	"fmt"
	"strings"
	"testing"
)

func TestWireguardErrorLinesAreRateLimited(t *testing.T) {
	// wireguard-go retries a failing handshake every few seconds; at that rate one dead endpoint
	// writes thousands of identical lines a day.
	out := captureLog(t, func() {
		logf := rateLimitedLogf(func(format string, args ...any) { logEvent("wireguard: "+format, args...) })
		for i := 0; i < 200; i++ {
			logf("%v - Failed to send handshake initiation: %v", "peer(abcd)", "network is unreachable")
		}
	})
	if n := strings.Count(out, "Failed to send handshake initiation"); n > 2 {
		t.Fatalf("wrote %d identical engine lines, want the first only:\n%s", n, out)
	}
}

func TestWireguardLinesForDifferentPeersAreNotSuppressedAsOne(t *testing.T) {
	// The limit is on the message, not the format: one failing peer must not hide another's failure.
	var got []string
	logf := rateLimitedLogf(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	for i := 0; i < 5; i++ {
		logf("%v - Failed to send handshake initiation: %v", "peer(aaaa)", "network is unreachable")
		logf("%v - Failed to send handshake initiation: %v", "peer(bbbb)", "network is unreachable")
	}
	var a, b int
	for _, l := range got {
		if strings.Contains(l, "peer(aaaa)") {
			a++
		}
		if strings.Contains(l, "peer(bbbb)") {
			b++
		}
	}
	if a != 1 || b != 1 {
		t.Errorf("peer a logged %d times and peer b %d times, want once each:\n%s", a, b, strings.Join(got, "\n"))
	}
}

func TestWireguardLinesAreRedactedBeforeTheyAreWritten(t *testing.T) {
	// The engine's text is not ours: an error it formats can echo a configuration line.
	secret := "Sup3rS3cretValue99"
	var got []string
	logf := rateLimitedLogf(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	logf("%v - rejected configuration: %v", "peer(abcd)", "private_key="+secret)
	logf("%v - rejected configuration: https://user:%s@host.example.com/x", "peer(abcd)", secret)
	if len(got) != 2 {
		t.Fatalf("got %d lines: %v", len(got), got)
	}
	for _, line := range got {
		if strings.Contains(line, secret) {
			t.Errorf("a credential reached the log: %q", line)
		}
	}
}

func TestTheRateLimiterDoesNotGrowWithoutBound(t *testing.T) {
	// A message that carries a counter or a timestamp is different every time.
	logf := rateLimitedLogf(func(string, ...any) {})
	for i := 0; i < 5000; i++ {
		logf("tick %d", i)
	}
	// No assertion on the map (it is private to the closure): the loop above must simply finish
	// quickly and, under the race detector, without a data race. The bound is asserted through the
	// exported behaviour: an old message is forgotten and may be written again.
	var n int
	logf2 := rateLimitedLogf(func(string, ...any) { n++ })
	for i := 0; i < maxLimiterKeys+10; i++ {
		logf2("tick %d", i)
	}
	logf2("tick 0")
	if n != maxLimiterKeys+11 {
		t.Errorf("wrote %d lines, want %d: the forgotten first message should be written again", n, maxLimiterKeys+11)
	}
}
