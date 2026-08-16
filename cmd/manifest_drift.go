package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/runos-official/cli/internal/dynacmd"
)

// A 4xx from a dispatch and a stale command list look the same (goal 21, B7).
//
// `explainPossiblyStaleManifest` covers the case where cobra never found the command. It does
// not cover the next one along: the command exists in the cached list, the CLI dispatches it,
// and conductor answers 400 or 404 because the route was renamed or a field it sends was
// dropped. The refusal reads as a server fault, and nothing compares the cached manifest
// version against the one the server serves. An agent then debugs the API, which is the wrong
// place, exactly as it did for the unknown-command case eight times before that fix.
//
// The comparison costs one small GET, so it runs at most once per process and only for a
// client error.

// driftCheckOnce keeps the version comparison to one per process, however
// many 4xx responses a single invocation collects.
var driftCheckOnce sync.Once

// driftCheckApplies reports whether an HTTP status is worth comparing
// manifest versions over.
//
// Client errors only, and not the auth ones: a 401 or 403 is about
// credentials and says nothing about the command list, while a 5xx is the
// server's own fault and refreshing a manifest would not touch it.
func driftCheckApplies(status int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	return status != http.StatusUnauthorized && status != http.StatusForbidden
}

// manifestDriftGuidance returns the line to print when the cached command
// list disagrees with the server's, or "" when it does not or either
// version is unknown.
func manifestDriftGuidance(cached, server string) string {
	if cached == "" || server == "" || cached == server {
		return ""
	}
	return fmt.Sprintf(
		"\nYour cached command list is %s and the server is serving %s, so this refusal may be drift "+
			"rather than a bad request: a renamed route or a field this CLI still sends.\n"+
			"Run `runos manifest update`, then try again.\n", cached, server)
}

// explainManifestDriftOn4xx prints the drift guidance after a dispatch
// failed with a client error. It writes to stderr and never changes the
// exit code.
func explainManifestDriftOn4xx(err error) {
	var apiErr *dynacmd.APIError
	if !errors.As(err, &apiErr) || !driftCheckApplies(apiErr.StatusCode) {
		return
	}
	driftCheckOnce.Do(func() {
		loader, lerr := newManifestLoader()
		if lerr != nil {
			return
		}
		cached := ""
		if m, merr := loader.LoadLocal(); merr == nil && m != nil {
			cached = m.Version
		}
		server, serr := loader.ServerVersion()
		if serr != nil {
			return
		}
		if guidance := manifestDriftGuidance(cached, server); guidance != "" {
			fmt.Fprint(os.Stderr, guidance)
		}
	})
}
