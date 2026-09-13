package cmd

import (
	"errors"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// An expired session must not be reported as an unreachable API.
//
// MEASURED 2026-09-13. A provider account whose token had expired ran an
// ordinary command and was told "RunOS could not reach the API to check whether
// your cached command list is current", so the reader went and checked the API,
// which answered 200 to an unauthenticated health probe the whole time. The real
// answer was a 401 and one `runos login`. `runos manifest update` reported the
// 401 correctly, so the two paths disagreed about the same failure.
func TestExpiredSessionIsNotReportedAsUnreachable(t *testing.T) {
	got := judgeStaleManifest("45.38.0", "", manifest.ErrNotAuthenticated)
	if got == verdictCannotTell {
		t.Fatal("a refused token was judged the same as an unreachable API; they need opposite advice")
	}
	if got != verdictNotSignedIn {
		t.Fatalf("expected verdictNotSignedIn, got %v", got)
	}
}

// A wrapped one still counts, because callers add context to errors.
func TestWrappedAuthErrorIsStillRecognised(t *testing.T) {
	wrapped := errors.Join(errors.New("fetching the version"), manifest.ErrNotAuthenticated)
	if judgeStaleManifest("45.38.0", "", wrapped) != verdictNotSignedIn {
		t.Fatal("a wrapped authentication failure must still be recognised")
	}
}

// A genuinely unreachable API must keep the offline advice.
func TestOfflineStillReportsCannotTell(t *testing.T) {
	if judgeStaleManifest("45.38.0", "", errors.New("dial tcp: no route to host")) != verdictCannotTell {
		t.Fatal("an unreachable API must still be reported as unreachable")
	}
}

// And the two working verdicts are unchanged.
func TestKnownVerdictsUnchanged(t *testing.T) {
	if judgeStaleManifest("45.38.0", "45.38.0", nil) != verdictCommandUnknown {
		t.Fatal("a matching version still means the command does not exist")
	}
	if judgeStaleManifest("45.37.0", "45.38.0", nil) != verdictCacheStale {
		t.Fatal("a behind version still means the cache is stale")
	}
}
