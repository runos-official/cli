// Package vmconsole opens a live session on a virtual machine: the serial console, the
// graphical screen, or a TCP tunnel to the guest's SSH port.
//
// A session is two steps, and the split is the security design rather than an implementation
// detail. First an ordinary authenticated request trades this CLI's real credential for a
// TICKET worth almost nothing on its own: one VM, one kind of session, one use, about a minute,
// and bound to the caller that asked. Then a websocket presents that ticket in the one place a
// credential may travel, and conductor relays the stream to the cluster.
//
// The ticket is minted immediately before connecting and again on every retry. It is single use
// and short-lived, so holding one is not an optimisation, it is a session that will not open.
package vmconsole

import (
	"fmt"
	"net/url"

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
	VMID string `json:"vmid"`
	Kind string `json:"kind"`
	// WebsocketURL is absolute and built by conductor from its own public URL, so the caller
	// never has to guess which host answers the upgrade.
	WebsocketURL string `json:"websocketUrl"`
	// Subprotocols is exactly what to offer as Sec-WebSocket-Protocol. Taken verbatim rather
	// than reconstructed here, so the format stays conductor's to change.
	Subprotocols []string `json:"subprotocols"`
	ExpiresAt    string   `json:"expiresAt"`
}

// MintTicket trades the caller's credential for a single-use session ticket.
//
// aid, cid and vmid are escaped rather than interpolated raw: they are RunOS ids and so are
// safe today, and a path built by concatenation is the kind of thing that stops being safe
// quietly when someone later passes a name instead of an id.
func MintTicket(client *api.Client, token, aid, cid, vmid string, kind Kind) (*Ticket, error) {
	path := fmt.Sprintf(
		"/%s/%s/vms/%s/console-ticket",
		url.PathEscape(aid), url.PathEscape(cid), url.PathEscape(vmid),
	)

	result, err := client.Do("POST", path, token, map[string]string{"kind": string(kind)})
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
	if ticket.WebsocketURL == "" || len(ticket.Subprotocols) == 0 {
		// Either one missing means the far end is not the relay this expects. Failing here
		// beats dialling something and reporting a confusing handshake error.
		return nil, fmt.Errorf("the server did not return a usable console ticket for %s", vmid)
	}
	return &ticket, nil
}
