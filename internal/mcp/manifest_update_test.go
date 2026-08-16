package mcp

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// what was written. The server writes JSON-RPC frames there, so a
// notification can only be observed this way.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	w.Close()
	os.Stdout = original
	return <-done
}

// Review 2 item 22. manifest_update notified tools/list_changed on every
// success, including the common case where the version was already
// current. A client that re-reads 600 tool definitions because nothing
// changed pays for a lie.
func TestManifestUpdate_NotifiesOnlyWhenTheListChanged(t *testing.T) {
	t.Run("unchanged version sends no notification", func(t *testing.T) {
		live := &manifest.Manifest{Version: "41.0.0"}
		srv := &Server{manifest: live, executor: &mockExecutor{}, category: "write", bootstrapped: true}
		srv.SetManifestReloader(&fakeReloader{serverVersion: "41.0.0", updated: &manifest.Manifest{Version: "41.0.0"}})

		out := captureStdout(t, func() {
			srv.handleToolsCall(makeToolCallRequest("manifest_update"))
		})

		if strings.Contains(out, "tools/list_changed") {
			t.Errorf("no notification is due when the version did not change, got: %s", out)
		}
	})

	t.Run("changed version sends the notification", func(t *testing.T) {
		live := &manifest.Manifest{Version: "40.1.0"}
		srv := &Server{manifest: live, executor: &mockExecutor{}, category: "write", bootstrapped: true}
		srv.SetManifestReloader(&fakeReloader{serverVersion: "41.0.0", updated: &manifest.Manifest{Version: "41.0.0"}})

		out := captureStdout(t, func() {
			srv.handleToolsCall(makeToolCallRequest("manifest_update"))
		})

		if !strings.Contains(out, "tools/list_changed") {
			t.Errorf("a changed list must be announced, got: %s", out)
		}
	})
}

// Review 2 item 22. tools/list probed the API for the manifest version on
// every call, and the probe carries a 10 s timeout, so a client that
// lists tools three times on an unreachable API stalls for 30 s. One
// probe per versionProbeInterval is enough to notice a conductor deploy.
func TestToolsList_VersionProbeIsCached(t *testing.T) {
	live := &manifest.Manifest{Version: "41.0.0"}
	srv := &Server{manifest: live, executor: &mockExecutor{}, category: "read"}
	reloader := &fakeReloader{serverVersion: "41.0.0"}
	srv.SetManifestReloader(reloader)

	for i := 0; i < 3; i++ {
		srv.handleToolsList(&Request{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	}

	if reloader.versionCalls != 1 {
		t.Fatalf("expected one version probe across three tools/list calls, got %d", reloader.versionCalls)
	}
}
