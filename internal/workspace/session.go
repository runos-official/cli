// Session is how a client asks for a session and is told where to take it.
//
// THE CLIENT NO LONGER KNOWS WHERE ANYTHING LIVES. It asks the control plane for a session and
// receives an address, a pass and the exact subprotocols to offer. Before this it held a workspace's
// public hostname and a shared key, and built the URL itself, which meant every client had its own
// copy of a routing decision and its own way to get it wrong.
package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Session is what the control plane returns from POST /:aid/:cid/sessions.
type Session struct {
	Kind string `json:"kind"`
	// Pass is the credential. It is NEVER logged, never printed, and never put in a URL.
	Pass string `json:"pass"`
	// URL is the gate's address. Built by the control plane from the CLUSTER's domain, so the
	// client connects to the cluster rather than through the API.
	URL string `json:"url"`
	// Subprotocols is exactly what to offer, in order. Taken as given rather than rebuilt, so the
	// wire format can change without every client needing a release.
	Subprotocols []string `json:"subprotocols"`
	// HTTPBase is set for the file kind only.
	HTTPBase  string `json:"httpBase,omitempty"`
	ExpiresAt string `json:"expiresAt"`
}

// SessionRequest is the body. It carries NO workspace name, NO user id and NO host: the control
// plane derives the target from who is asking. There is deliberately nothing here to name a
// colleague with.
type SessionRequest struct {
	Kind string `json:"kind"`
	// Shell is which login to open, for a workspace kind. One of dev or devops.
	Shell string `json:"shell,omitempty"`
	// Dir is an optional working directory.
	Dir string `json:"dir,omitempty"`
	// Cmd is an optional one-off command, verbatim. NOT base64: the control plane signs it and the
	// workspace reads it from the signed bytes.
	Cmd string `json:"cmd,omitempty"`
	// VMID names a machine, for a VM kind. A lookup key rather than a target: a machine the caller
	// may not see returns not-found, and the namespace and object name come from the row.
	VMID string `json:"vmid,omitempty"`
}

// ParseSession reads the control plane's answer and refuses anything unusable.
//
// Checked rather than trusted, because the failure it prevents is ugly: an empty pass or an empty
// URL produces a dial against "" and an error naming neither, and the person reading it has no way
// to tell a broken control plane from a broken network.
func ParseSession(body []byte) (*Session, error) {
	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("the session response could not be read: %w", err)
	}
	if strings.TrimSpace(s.Pass) == "" {
		return nil, fmt.Errorf("the session response carried no pass")
	}
	if strings.TrimSpace(s.URL) == "" {
		return nil, fmt.Errorf("the session response carried no address")
	}
	if len(s.Subprotocols) == 0 {
		return nil, fmt.Errorf("the session response named no subprotocols")
	}
	// A pass in a URL would be written down in the ingress access log and in shell history. The
	// control plane does not put it there; this makes sure a future one that did is caught here
	// rather than after a customer's log has it.
	if strings.Contains(s.URL, s.Pass) {
		return nil, fmt.Errorf("the session address contains the pass, which must never travel in a URL")
	}
	return &s, nil
}

// Redact removes a pass from text that is about to be shown or logged.
//
// The pass reaches an error message by the shortest possible route: a dial failure that includes the
// URL, a wrapped error, a debug print somebody adds later. This is the one place that has to know.
func Redact(text, pass string) string {
	if pass == "" {
		return text
	}
	return strings.ReplaceAll(text, pass, "<pass redacted>")
}

// jsonMarshal exists so a test can assert the exact shape of the request body, which is the thing
// that must never grow a field naming another person.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
