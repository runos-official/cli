// Package vpn is the RunOS VPN client: a root daemon that embeds a WireGuard-compatible engine
// (wireguard-go) and converges an interface, routes and split DNS to the desired-state document
// Conductor serves for this device, plus the socket client the `runos vpn` commands use to drive
// it. Identity is a per-device key generated inside the daemon; policy is a 24-hour session
// Conductor mints from a fresh interactive sign-in; enforcement is peer removal on the servers.
//
// This file is the wire contract between the CLI process (the person's user, holding the
// Firebase credential) and the daemon (root, holding the device key and the session token). The
// two never share a file: everything crosses the local socket as one JSON request and one JSON
// reply.
package vpn

import "time"

// Op names one request the CLI can make of the daemon.
type Op string

const (
	// OpIdentity returns the device's public key (generating a key on first use), and whatever
	// enrolment the daemon already holds. Needs no session.
	OpIdentity Op = "identity"
	// OpIdentities lists locally known account identities without returning private keys.
	OpIdentities Op = "identities"
	// OpUp hands the daemon a freshly minted session and the account/device it belongs to; the
	// daemon polls the state document at once and converges. Returns the status afterwards.
	OpUp Op = "up"
	// OpDown ends the session server-side (peers removed) and tears the tunnel down locally.
	// The device key and enrolment are kept, so the next up needs only a sign-in.
	OpDown Op = "down"
	// OpStatus reports the daemon's view: interface, session, per-cluster peers and traffic.
	OpStatus Op = "status"
	// OpConnect and OpDisconnect change the connected set through Conductor with the session
	// token, then re-poll so the tunnel follows without waiting for the next tick.
	OpConnect    Op = "connect"
	OpDisconnect Op = "disconnect"
	// OpPoll forces a state poll now.
	OpPoll Op = "poll"
	// OpLogout is down plus forgetting the device key and enrolment on this machine. The device
	// row stays in the account until it is revoked.
	OpLogout Op = "logout"
	// OpRotateKey tears the tunnel down and replaces the device keypair, for a key conductor
	// refuses to enrol again (revoked). Returns the new identity.
	OpRotateKey Op = "rotate-key"
	// OpForgetIdentity removes one account identity and leaves server records unchanged.
	OpForgetIdentity Op = "forget-identity"
)

// Request is what the CLI writes to the socket, one JSON object per line.
type Request struct {
	Op Op `json:"op"`
	// Session fields, for OpUp.
	SessionToken     string    `json:"sessionToken,omitempty"`
	SessionExpiresAt time.Time `json:"sessionExpiresAt,omitempty"`
	AccountID        string    `json:"accountId,omitempty"`
	DeviceID         string    `json:"deviceId,omitempty"`
	ConductorURL     string    `json:"conductorUrl,omitempty"`
	// Cluster id, for OpConnect and OpDisconnect.
	CID string `json:"cid,omitempty"`
}

// Response is what the daemon writes back. Exactly one of Error or the payload is meaningful.
type Response struct {
	Error      string     `json:"error,omitempty"`
	Identity   *Identity  `json:"identity,omitempty"`
	Identities []Identity `json:"identities,omitempty"`
	Status     *Status    `json:"status,omitempty"`
}

// Identity is what the daemon knows about this device before or after enrolment.
type Identity struct {
	PublicKey string `json:"publicKey"`
	// Empty until `up` enrolled the device and told the daemon.
	DeviceID  string `json:"deviceId,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	// The daemon's binary version, so a CLI can tell when the daemon needs a restart after an
	// update (the running inode stays old until launchd restarts it).
	Version          string    `json:"version"`
	SessionPresent   bool      `json:"sessionPresent"`
	SessionExpiresAt time.Time `json:"sessionExpiresAt,omitempty"`
}

// Status is the daemon's view of the tunnel, shaped for `runos vpn status --json`.
type Status struct {
	SchemaVersion int    `json:"schemaVersion"`
	Running       bool   `json:"running"`
	Interface     string `json:"interface,omitempty"`
	Address       string `json:"address,omitempty"`
	DeviceID      string `json:"deviceId,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
	Session       struct {
		Present       bool      `json:"present"`
		ExpiresAt     time.Time `json:"expiresAt,omitempty"`
		LoginRequired bool      `json:"loginRequired"`
	} `json:"session"`
	DNS      DNSStatus       `json:"dns"`
	Clusters []ClusterStatus `json:"clusters"`
	// The revision of the last document applied, and when it was fetched.
	Revision     string    `json:"revision,omitempty"`
	LastPollAt   time.Time `json:"lastPollAt,omitempty"`
	LastPollErr  string    `json:"lastPollError,omitempty"`
	LastApplyErr string    `json:"lastApplyError,omitempty"`
	Version      string    `json:"version"`
}

// DNSStatus reports private DNS separately from tunnel reachability.
type DNSStatus struct {
	Available bool   `json:"available"`
	Mode      string `json:"mode"`
	Error     string `json:"error"`
}

// ClusterStatus is one cluster of the account as the daemon sees it: the document's facts plus
// the live peer, when the daemon holds one.
type ClusterStatus struct {
	CID        string   `json:"cid"`
	Name       string   `json:"name"`
	Connected  bool     `json:"connected"`
	Reachable  bool     `json:"reachable"`
	Reason     string   `json:"reason,omitempty"`
	Endpoint   string   `json:"endpoint,omitempty"`
	AllowedIPs []string `json:"allowedIps"`
	Resolver   string   `json:"resolver,omitempty"`
	Zones      []string `json:"zones"`
	PeeredWith []string `json:"peeredWith"`
	// True when the engine currently holds a peer for this cluster.
	PeerUp        bool      `json:"peerUp"`
	LastHandshake time.Time `json:"lastHandshake,omitempty"`
	RxBytes       int64     `json:"rxBytes"`
	TxBytes       int64     `json:"txBytes"`
}
