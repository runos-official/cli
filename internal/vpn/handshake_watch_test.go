package vpn

import (
	"strings"
	"testing"
	"time"
)

/*
The first handshake is the moment a tunnel is proven, and the one step the daemon cannot observe
directly: wireguard-go does it on its own. The watch reads it back on each poll and writes a line
only when something CHANGES, so a working tunnel stays silent after the first line.
*/

func TestFirstHandshakeIsLoggedOnce(t *testing.T) {
	w := newHandshakeWatch()
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

	if got := w.observe(t0, []peerObs{{CID: "c1"}}); len(got) != 0 {
		t.Fatalf("a peer inside its grace period is not a failure yet: %v", got)
	}
	got := w.observe(t0.Add(30*time.Second), []peerObs{{CID: "c1", LastHandshake: t0.Add(20 * time.Second)}})
	if len(got) != 1 || !strings.Contains(got[0], "step=handshake status=ok") || !strings.Contains(got[0], "c1") {
		t.Fatalf("want one ok line naming the cluster, got %v", got)
	}
	for i := 1; i < 100; i++ {
		now := t0.Add(time.Duration(30+i*30) * time.Second)
		if got := w.observe(now, []peerObs{{CID: "c1", LastHandshake: now.Add(-10 * time.Second)}}); len(got) != 0 {
			t.Fatalf("a healthy tunnel must stay silent, poll %d wrote %v", i, got)
		}
	}
}

func TestNoHandshakeIsReportedOnceAfterTheGracePeriod(t *testing.T) {
	w := newHandshakeWatch()
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, w.observe(t0.Add(time.Duration(i*30)*time.Second), []peerObs{{CID: "c1", Endpoint: "192.0.2.10:51820"}})...)
	}
	if len(lines) != 1 {
		t.Fatalf("want exactly one failure line for ten minutes without a handshake, got %d: %v", len(lines), lines)
	}
	for _, want := range []string{"step=handshake status=failed", "c1", "192.0.2.10:51820"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("failure line lacks %q: %s", want, lines[0])
		}
	}
	// The line is how the reader tells "blocked" from "never tried", so it names the likely cause.
	if !strings.Contains(strings.ToLower(lines[0]), "udp") {
		t.Errorf("failure line should name UDP reachability as the usual cause: %s", lines[0])
	}
}

func TestAStaleHandshakeIsReportedAndItsRecoveryToo(t *testing.T) {
	w := newHandshakeWatch()
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	w.observe(t0, []peerObs{{CID: "c1", LastHandshake: t0}})

	stale := w.observe(t0.Add(peerStaleAfter+time.Minute), []peerObs{{CID: "c1", LastHandshake: t0}})
	if len(stale) != 1 || !strings.Contains(stale[0], "status=warn") {
		t.Fatalf("want one warn line when a working peer goes quiet, got %v", stale)
	}
	again := w.observe(t0.Add(peerStaleAfter+2*time.Minute), []peerObs{{CID: "c1", LastHandshake: t0}})
	if len(again) != 0 {
		t.Fatalf("the same stale condition must not repeat: %v", again)
	}
	back := w.observe(t0.Add(peerStaleAfter+3*time.Minute), []peerObs{{CID: "c1", LastHandshake: t0.Add(peerStaleAfter + 3*time.Minute)}})
	if len(back) != 1 || !strings.Contains(back[0], "status=ok") {
		t.Fatalf("want one ok line when it resumes, got %v", back)
	}
}

func TestADroppedPeerIsForgotten(t *testing.T) {
	w := newHandshakeWatch()
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	w.observe(t0, []peerObs{{CID: "c1"}})
	w.observe(t0.Add(time.Minute), nil)
	// Re-added later: the grace period starts again rather than inheriting the old clock.
	if got := w.observe(t0.Add(time.Hour), []peerObs{{CID: "c1"}}); len(got) != 0 {
		t.Fatalf("a re-added peer got an instant failure: %v", got)
	}
}

func TestAPeerWhoseHandshakeWasResetStartsItsGraceAgain(t *testing.T) {
	// Found live on a Linux daemon: the engine re-created a working peer, the poll saw no handshake,
	// and the watch wrote "no handshake for 2562047h47m16s" (the zero time subtracted from now).
	w := newHandshakeWatch()
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	w.observe(t0, []peerObs{{CID: "c1", LastHandshake: t0}})

	if got := w.observe(t0.Add(30*time.Second), []peerObs{{CID: "c1"}}); len(got) != 0 {
		t.Fatalf("a reset peer is inside its grace period, not a failure: %v", got)
	}
	late := w.observe(t0.Add(30*time.Second+handshakeGrace), []peerObs{{CID: "c1"}})
	if len(late) != 1 || !strings.Contains(late[0], "status=failed") || strings.Contains(late[0], "2562047") {
		t.Fatalf("want one failed line with a sane duration once the grace ends, got %v", late)
	}
}
