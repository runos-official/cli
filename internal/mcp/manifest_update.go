package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/runos-official/cli/internal/manifest"
)

// A running MCP server used to be frozen at the manifest it started with (goal 21, B2).
//
// The server loads the CLI manifest once in `runos mcp serve` and never looks again. Ship a
// command in conductor and every live MCP session stays blind to it until the operator
// restarts the IDE host, which is the same failure the `unknown command` recovery exists for,
// except that here there is no recovery at all: the agent cannot even refresh the cache,
// because `runos manifest update` is a CLI verb and no MCP tool wraps it. The server also
// reported `listChanged: false`, so a client had no reason to re-read the list even if the
// server had changed it.
//
// Three parts: this tool, the `listChanged` capability (so the client re-reads), and a
// version re-check on `tools/list` (so a client that reconnects picks up drift without being
// told).

// manifestUpdateToolName is the tool an agent calls to refresh the
// command list without restarting the server.
const manifestUpdateToolName = "manifest_update"

// versionProbeInterval is the shortest gap between two tools/list version
// probes. 30 s is far below any realistic conductor deploy cadence and
// far above a client's burst of tools/list calls.
const versionProbeInterval = 30 * time.Second

// ManifestReloader refreshes the CLI manifest from the API. Implemented
// by internal/manifest.Loader; an interface so the server can be tested
// without a network.
type ManifestReloader interface {
	// ServerVersion reports the manifest version the API serves, without
	// downloading the manifest.
	ServerVersion() (string, error)
	// ForceUpdate downloads and caches the manifest, bypassing the TTL.
	ForceUpdate() (*manifest.Manifest, error)
}

// SetManifestReloader wires the loader the server uses to refresh its
// manifest. Leaving it unset disables the refresh paths entirely, which
// is what the tests want and what a caller with no loader gets.
func (s *Server) SetManifestReloader(r ManifestReloader) {
	s.reloader = r
}

// manifestUpdateTool is the static tool definition, listed on every
// category: any server can be the one holding a stale list.
func manifestUpdateTool() Tool {
	return Tool{
		Name: manifestUpdateToolName,
		Description: `Refresh this server's RunOS command list from the API, without restarting it.

Call this when a tool you expect does not exist, when a tool refuses an argument the documentation says it takes, or right after someone deploys conductor. The server loads the command list once at startup, so a command shipped since then is invisible until this runs.

Returns the version before and after. When the version changes, the tool list changes with it and the server tells your client to re-read it.`,
		InputSchema: InputSchema{Type: "object", Properties: map[string]Property{}},
	}
}

// isManifestUpdateTool reports whether toolName is the static refresh tool.
func isManifestUpdateTool(toolName string) bool {
	return toolName == manifestUpdateToolName
}

// handleManifestUpdate refreshes the manifest in place and reports the
// version change. The bool says whether the tool list actually changed,
// which is the only case the client has to be told about (review 2 item
// 22).
//
// The manifest is updated THROUGH the pointer rather than reassigned:
// the executor holds the same *manifest.Manifest, so one write updates
// both and no plumbing has to reach into the executor.
func (s *Server) handleManifestUpdate() (string, bool, error) {
	if s.reloader == nil {
		return "", false, fmt.Errorf("%s: this server was started without a manifest loader, so it cannot refresh. Restart the MCP server to pick up a new command list", manifestUpdateToolName)
	}
	before := s.manifest.Version
	updated, err := s.reloader.ForceUpdate()
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", manifestUpdateToolName, err)
	}
	*s.manifest = *updated
	if before == updated.Version {
		return fmt.Sprintf("Command list is already current at %s. Nothing changed.", updated.Version), false, nil
	}
	return fmt.Sprintf("Command list refreshed from %s to %s. The tool list has changed; re-read it.", before, updated.Version), true, nil
}

// refreshManifestIfDrifted compares the loaded manifest version against
// the one the API serves and refreshes when they differ.
//
// Runs on tools/list, which is when a client asks what exists, so a
// reconnecting client picks up a conductor deploy without anyone calling
// manifest_update. Reports whether the list actually changed. Every
// failure is silent: an offline version check must not break tools/list,
// and the stale list is still better than no list.
//
// At most one probe per versionProbeInterval, whatever the outcome. A
// client that lists tools repeatedly (they do, on every reconnect and
// after every list_changed) otherwise paid the loader's 10 s timeout
// every time the API was unreachable (review 2 item 22).
func (s *Server) refreshManifestIfDrifted() bool {
	if s.reloader == nil || s.manifest == nil {
		return false
	}
	if !s.lastVersionProbe.IsZero() && time.Since(s.lastVersionProbe) < versionProbeInterval {
		return false
	}
	s.lastVersionProbe = time.Now()
	serverVersion, err := s.reloader.ServerVersion()
	if err != nil || serverVersion == "" || serverVersion == s.manifest.Version {
		return false
	}
	updated, err := s.reloader.ForceUpdate()
	if err != nil || updated == nil {
		return false
	}
	*s.manifest = *updated
	return true
}

// manifestDriftNote returns a line to append to a failed tool call when
// the failure was a client error AND this server's command list
// disagrees with the one conductor serves.
//
// The CLI prints the same explanation after its own dispatch fails with
// a 4xx; the MCP surface printed nothing, so an agent reading "400 Bad
// Request" had no reason to suspect its tool definitions were months old
// (goal 21, B7). Checked at most once per process, and never for an auth
// or server error, which say nothing about the command list.
func (s *Server) manifestDriftNote(err error) string {
	if s.reloader == nil || s.manifest == nil || !isClientErrorEnvelope(err) {
		return ""
	}
	if s.driftChecked {
		return ""
	}
	s.driftChecked = true
	serverVersion, verr := s.reloader.ServerVersion()
	if verr != nil || serverVersion == "" || serverVersion == s.manifest.Version {
		return ""
	}
	return fmt.Sprintf(
		"\n\nNOTE: this server's command list is %s and the API is serving %s. This refusal may be drift "+
			"rather than a bad request. Call manifest_update, then retry.", s.manifest.Version, serverVersion)
}

// isClientErrorEnvelope reports whether an executor error carries a 4xx
// statusCode, excluding the auth codes. The error text is the JSON
// envelope apiErrorEnvelope built, so the code is read back out of it.
func isClientErrorEnvelope(err error) bool {
	var envelope struct {
		StatusCode int `json:"statusCode"`
	}
	if jErr := json.Unmarshal([]byte(err.Error()), &envelope); jErr != nil {
		return false
	}
	if envelope.StatusCode < 400 || envelope.StatusCode >= 500 {
		return false
	}
	return envelope.StatusCode != http.StatusUnauthorized && envelope.StatusCode != http.StatusForbidden
}
