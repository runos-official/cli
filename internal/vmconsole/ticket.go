// Package vmconsole opens a live session on a virtual machine: the serial console, the
// graphical screen, or a TCP tunnel to the guest's SSH port.
//
// A session is two steps, and the split is the security design rather than an implementation
// detail. First an ordinary authenticated request trades this CLI's real credential for a PASS
// worth almost nothing on its own: one VM, one kind of session, one use, sixty seconds, and bound
// to the caller that asked. Then a websocket presents that pass in the one place a credential may
// travel.
//
// THE SECOND STEP GOES STRAIGHT TO THE CLUSTER. It used to go to conductor, which held a
// cluster-admin credential for the life of every session and relayed every byte, so a terminal's
// responsiveness depended on the control plane and every console session copied a customer's admin
// credentials out of their cluster. The pass is verified by a small gate INSIDE the cluster, and
// conductor is not in the data path at all.
//
// The pass is minted immediately before connecting and again on every retry. It is single use and
// short-lived, so holding one is not an optimisation, it is a session that will not open.
package vmconsole

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/runos-official/cli/internal/api"
)

// Kind is what to open. These are RunOS's words; conductor maps them onto KubeVirt's.
type Kind string

const (
	// KindSerial is the guest's own tty: the way in when SSH is not answering.
	KindSerial Kind = "serial"
	// KindVNC is the graphical framebuffer, speaking RFB.
	KindVNC Kind = "vnc"
	// KindSSH is a plain TCP tunnel to the guest's port 22, and nothing else.
	KindSSH Kind = "ssh"
)

// Ticket is one session's credential and everything needed to spend it.
type Ticket struct {
	Kind string `json:"kind"`
	// Pass is the credential. NEVER logged, never printed, never put in a URL.
	Pass string `json:"pass"`
	// WebsocketURL is absolute and built by the control plane from the CLUSTER's own domain, so the
	// caller never has to guess which host answers the upgrade, and so the connection goes to the
	// cluster rather than through the API.
	WebsocketURL string `json:"url"`
	// Subprotocols is exactly what to offer as Sec-WebSocket-Protocol. Taken verbatim rather
	// than reconstructed here, so the format stays the control plane's to change.
	Subprotocols []string `json:"subprotocols"`
	ExpiresAt    string   `json:"expiresAt"`
}

// passKind maps RunOS's word for a session onto the pass kind the gate verifies.
//
// A table rather than string concatenation: "vm."+kind would happily build "vm.rdp" from a typo and
// send it, and the refusal would come from a gate rather than from here.
func passKind(kind Kind) (string, error) {
	switch kind {
	case KindSerial:
		return "vm.serial", nil
	case KindVNC:
		return "vm.vnc", nil
	case KindSSH:
		return "vm.ssh", nil
	default:
		return "", fmt.Errorf("%q is not a session kind", kind)
	}
}

// MintTicket trades the caller's credential for a single-use session ticket.
//
// aid, cid and vmid are escaped rather than interpolated raw: they are RunOS ids and so are
// safe today, and a path built by concatenation is the kind of thing that stops being safe
// quietly when someone later passes a name instead of an id.
func MintTicket(client *api.Client, token, aid, cid, vmid string, kind Kind) (*Ticket, error) {
	wanted, err := passKind(kind)
	if err != nil {
		return nil, err
	}

	// ONE PATH FOR EVERY KIND OF SESSION, and it names no machine. The vmid travels in the body as
	// a lookup key: a machine the caller may not see comes back as not-found, and the namespace and
	// object name are read from the row rather than sent by the client.
	path := fmt.Sprintf("/%s/%s/sessions", url.PathEscape(aid), url.PathEscape(cid))

	result, err := client.Do("POST", path, token, map[string]string{"kind": wanted, "vmid": vmid})
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		if message := result.ErrorMessage(); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, fmt.Errorf("could not open a session on %s (HTTP %d)", vmid, result.StatusCode)
	}

	var ticket Ticket
	if err := result.Decode(&ticket); err != nil {
		return nil, err
	}
	if ticket.WebsocketURL == "" || len(ticket.Subprotocols) == 0 || ticket.Pass == "" {
		// Any one missing means the far end is not what this expects. Failing here beats dialling
		// something and reporting a confusing handshake error.
		return nil, fmt.Errorf("the server did not return a usable session for %s", vmid)
	}
	// A pass in a URL would be written into the ingress access log and into shell history. The
	// control plane does not put it there; this catches a future one that starts to.
	if strings.Contains(ticket.WebsocketURL, ticket.Pass) {
		return nil, fmt.Errorf("the session address contains the pass, which must never travel in a URL")
	}
	return &ticket, nil
}
