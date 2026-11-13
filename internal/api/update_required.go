package api

import (
	"net/http"
	"sync"
)

// UpdateRequiredCode is the code in conductor's 426 body when the CLI is below the minimum version
// the operator set.
const UpdateRequiredCode = "cli.update_required"

// updateRequiredMessage is the whole message a person sees. It names the fix and nothing else: the
// request URL and conductor's body stay out of it. It does not offer to update by itself.
const updateRequiredMessage = "Your RunOS CLI is too old to talk to RunOS. Update it with 'runos update' (or download the latest release), then try again."

// UpdateRequiredError is conductor's refusal of a CLI that is below the minimum version. It stops
// the command; the CLI never updates by itself.
type UpdateRequiredError struct {
	// fields is every top-level field of conductor's body (the minimum version, the version it saw,
	// its fix sentence), forwarded as they are so a caller reading the JSON error sees them.
	fields map[string]any
}

// Error is one plain sentence.
func (e *UpdateRequiredError) Error() string { return updateRequiredMessage }

// Envelope is the JSON error shape that `--json`, MCP and RunOS Desktop callers already read:
// "error", "statusCode" and "code", plus the fields conductor sent.
func (e *UpdateRequiredError) Envelope() map[string]any {
	envelope := map[string]any{}
	for k, v := range e.fields {
		envelope[k] = v
	}
	envelope["error"] = e.Error()
	envelope["statusCode"] = http.StatusUpgradeRequired
	envelope["code"] = UpdateRequiredCode
	return envelope
}

var (
	observerMu sync.Mutex
	observer   func()
)

// SetUpdateRequiredObserver registers a function that runs when a request is refused by the
// version gate, before the error reaches any caller. The command layer uses it to silence cobra's
// own error line, so the refusal is printed once, in one place. Pass nil to remove it.
func SetUpdateRequiredObserver(f func()) {
	observerMu.Lock()
	defer observerMu.Unlock()
	observer = f
}

func notifyUpdateRequired() {
	observerMu.Lock()
	f := observer
	observerMu.Unlock()
	if f != nil {
		f()
	}
}
