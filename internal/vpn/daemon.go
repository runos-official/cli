package vpn

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Daemon is the running tunnel: state on disk, one engine, one platform, and a poll loop that
// converges the machine to Conductor's desired-state document. It is driven entirely through
// Handle (the socket ops); the loop only re-applies what a poll changes.
//
// One mutex guards everything: the daemon does one thing at a time (apply a document, answer a
// status), so a coarse lock is simpler than per-field locking and the work is not hot.
type Daemon struct {
	mu       sync.Mutex
	stateDir string
	version  string
	verbose  bool

	state    *State
	platform platform

	// Live tunnel, nil when down.
	engine       *engine
	client       *conductorClient
	doc          *Document
	plan         Plan
	revision     string
	lastPoll     time.Time
	lastPollErr  string
	lastApplyErr string
	dns          DNSStatus

	pollInterval time.Duration
	cancelPoll   context.CancelFunc
	wg           sync.WaitGroup
}

// PollInterval is how often the daemon re-reads the desired-state document. 30 s matches the
// document's design (it changes on operator actions, so a poll is enough) and the 304 path keeps
// an unchanged poll cheap.
const PollInterval = 30 * time.Second

// NewDaemon loads (or initialises) the daemon state and returns a daemon ready to serve the
// socket. It does not bring a tunnel up; that waits for OpUp.
func NewDaemon(stateDir, version string, verbose bool) (*Daemon, error) {
	state, err := LoadState(stateDir)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		stateDir:     stateDir,
		version:      version,
		verbose:      verbose,
		state:        state,
		platform:     newPlatform(),
		pollInterval: PollInterval,
		dns:          DNSStatus{Mode: "unavailable", Error: "the VPN is down"},
	}, nil
}

// Resume brings the tunnel back up on daemon start when the state holds a session that has not
// lapsed: a daemon restart or a reboot must not force a new sign-in while the session is still
// valid. Called once after NewDaemon.
func (d *Daemon) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	active := d.state.Active()
	if active == nil || active.SessionToken == "" || active.SessionExpiresAt.Before(time.Now()) {
		return
	}
	if err := d.startTunnelLocked(); err != nil {
		d.lastApplyErr = err.Error()
		// The daemon starts at boot and races the network stack, so this is the ordinary failure,
		// not an exceptional one. Say that the retry loop is running, because the previous version
		// of this code gave up here and left no trace of having done so.
		logEvent("resume did not complete its first poll: %s (the retry loop is running)",
			redactURL(err.Error()))
	}
}

// Handle serves one socket request. It is the only entry point besides Resume.
func (d *Daemon) Handle(req Request) Response {
	d.mu.Lock()
	defer d.mu.Unlock()

	switch req.Op {
	case OpIdentity:
		return d.handleIdentityLocked(req.AccountID)
	case OpIdentities:
		return Response{Identities: d.identitiesLocked()}
	case OpStatus:
		return Response{Status: d.statusLocked()}
	case OpUp:
		return d.handleUpLocked(req)
	case OpDown:
		return d.handleDownLocked(false)
	case OpLogout:
		return d.handleDownLocked(true)
	case OpRotateKey:
		return d.handleRotateKeyLocked(req.AccountID)
	case OpForgetIdentity:
		return d.handleForgetIdentityLocked(req.AccountID)
	case OpConnect:
		return d.handleSetMembershipLocked(req.CID, true)
	case OpDisconnect:
		return d.handleSetMembershipLocked(req.CID, false)
	case OpPoll:
		if err := d.pollAndApplyLocked(); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Status: d.statusLocked()}
	default:
		return Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

func (d *Daemon) handleIdentityLocked(accountID string) Response {
	identity, err := d.state.IdentityForAccount(accountID)
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := SaveState(d.stateDir, d.state); err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Identity: d.renderIdentity(identity)}
}

func (d *Daemon) renderIdentity(identity *AccountState) *Identity {
	return &Identity{
		PublicKey: identity.PublicKey, DeviceID: identity.DeviceID, AccountID: identity.AccountID,
		Version: d.version, SessionPresent: identity.SessionToken != "" && identity.SessionExpiresAt.After(time.Now()),
		SessionExpiresAt: identity.SessionExpiresAt,
	}
}

func (d *Daemon) identitiesLocked() []Identity {
	identities := make([]Identity, 0, len(d.state.Accounts))
	for _, identity := range d.state.Accounts {
		identities = append(identities, *d.renderIdentity(identity))
	}
	return identities
}

// handleUpLocked records a freshly minted session and enrolment (the CLI did the sign-in and the
// mint), then brings the tunnel up and polls at once.
func (d *Daemon) handleUpLocked(req Request) Response {
	if req.SessionToken == "" || req.AccountID == "" || req.DeviceID == "" || req.ConductorURL == "" {
		return Response{Error: "up needs a session token, account, device and conductor url"}
	}
	/*
	 THE KEY MUST ALREADY EXIST. Minting one here is how a tunnel came up on a key conductor had
	 never seen.

	 `up` is handed a device id that the CLI enrolled moments ago, and enrolment names a public key.
	 If this account has no key on this machine, then whatever was enrolled belongs to a DIFFERENT
	 account, and starting a tunnel with a freshly minted key would produce exactly the reported
	 failure: an interface that comes up, clusters that list, and not one packet that routes.

	 Refusing is recoverable in one command. Silently minting was not recoverable at all, because
	 nothing about it looked wrong.
	*/
	identity := d.state.ExistingIdentityForAccount(req.AccountID)
	if identity == nil {
		return Response{Error: fmt.Sprintf(
			"this machine has no VPN key enrolled for account %s; run 'runos vpn up' again",
			req.AccountID,
		)}
	}
	// Keep the old tunnel until the target session arrives fully prepared.
	if old := d.state.Active(); old != nil && old.AccountID != req.AccountID && d.client != nil {
		if err := d.client.endSession(); err != nil {
			d.lastPollErr = "end old session: " + err.Error()
		}
	}
	d.stopTunnelLocked()
	identity.SessionToken = req.SessionToken
	identity.SessionExpiresAt = req.SessionExpiresAt
	identity.DeviceID = req.DeviceID
	identity.ConductorURL = req.ConductorURL
	identity.Enrolled = true
	d.state.ActiveAccountID = req.AccountID
	if err := SaveState(d.stateDir, d.state); err != nil {
		return Response{Error: err.Error()}
	}
	if err := d.startTunnelLocked(); err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Status: d.statusLocked()}
}

// handleDownLocked tears the tunnel down and ends the session server-side. When forget is set
// (logout) the enrolment is cleared too; the device key stays either way.
func (d *Daemon) handleDownLocked(forget bool) Response {
	active := d.state.Active()
	// Read BEFORE anything is torn down, because that is the only moment it is knowable. A caller
	// that says "disconnected the VPN" needs to have been told one was connected.
	tunnelWasUp := d.engine != nil
	if d.client != nil {
		if err := d.client.endSession(); err != nil {
			// Report but keep tearing down: a laptop the person told to go down must go down
			// locally even if conductor is unreachable; the session lapses on its own within 24h.
			d.lastPollErr = "end session: " + err.Error()
		}
	}
	d.stopTunnelLocked()
	if active != nil {
		if forget {
			d.state.ForgetAccount(active.AccountID)
		} else {
			active.ClearSession()
		}
	}
	if err := SaveState(d.stateDir, d.state); err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Status: d.statusLocked(), TunnelWasUp: tunnelWasUp}
}

// handleRotateKeyLocked drops the tunnel and the old identity and mints a new device keypair.
// No server-side call: the old key is already refused there, which is why the CLI asked.
func (d *Daemon) handleRotateKeyLocked(accountID string) Response {
	if accountID == "" {
		accountID = d.state.ActiveAccountID
	}
	if accountID == d.state.ActiveAccountID {
		d.stopTunnelLocked()
	}
	identity, err := d.state.RotateAccountKey(accountID)
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := SaveState(d.stateDir, d.state); err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Identity: d.renderIdentity(identity)}
}

func (d *Daemon) handleForgetIdentityLocked(accountID string) Response {
	if accountID == "" {
		return Response{Error: "account ID is required"}
	}
	if accountID == d.state.ActiveAccountID {
		if d.client != nil {
			if err := d.client.endSession(); err != nil {
				d.lastPollErr = "end session: " + err.Error()
			}
		}
		d.stopTunnelLocked()
	}
	d.state.ForgetAccount(accountID)
	if err := SaveState(d.stateDir, d.state); err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Status: d.statusLocked()}
}

// handleSetMembershipLocked adds or removes one cluster from the connected set through Conductor,
// then polls so the tunnel follows without waiting for the next tick.
func (d *Daemon) handleSetMembershipLocked(cid string, connect bool) Response {
	if d.client == nil {
		return Response{Error: "not connected; run 'runos vpn up' first"}
	}
	if cid == "" {
		return Response{Error: "a cluster id is required"}
	}
	// Connect only. A disconnect is always allowed, including from a cluster that is unreachable,
	// because that is the only way out of the dead state this guard exists to prevent.
	if connect {
		if refusal := connectRefusal(d.doc, cid); refusal != "" {
			return Response{Error: refusal}
		}
	}
	current := map[string]struct{}{}
	if d.doc != nil {
		for _, c := range d.doc.Clusters {
			if c.Connected {
				current[c.CID] = struct{}{}
			}
		}
	}
	if connect {
		current[cid] = struct{}{}
	} else {
		delete(current, cid)
	}
	cids := make([]string, 0, len(current))
	for c := range current {
		cids = append(cids, c)
	}
	if err := d.client.setClusters(cids); err != nil {
		return Response{Error: err.Error()}
	}
	if err := d.pollAndApplyLocked(); err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Status: d.statusLocked()}
}

// startTunnelLocked creates the engine and platform client from the current state and does the
// first poll+apply. Safe to call when a tunnel is already up (it replaces it).
func (d *Daemon) startTunnelLocked() error {
	active := d.state.Active()
	if active == nil {
		return fmt.Errorf("no active VPN account")
	}
	d.stopTunnelLocked()
	eng, err := newEngine(defaultTunName, d.verbose)
	if err != nil {
		return err
	}
	if err := eng.Up(); err != nil {
		eng.Close()
		return err
	}
	d.engine = eng
	d.client = newConductorClient(active.ConductorURL, active.AccountID, active.DeviceID, active.SessionToken)
	d.revision = ""
	resetPollLog()
	logEvent("tunnel up on %s for account %s device %s, conductor %s",
		eng.InterfaceName(), active.AccountID, active.DeviceID, redactURL(active.ConductorURL))
	return d.beginPollingLocked()
}

/*
Start the poll loop, then take the first poll.

THE ORDER IS THE WHOLE POINT, and it is the fix for a defect measured on a real machine
2026-09-01. This used to poll first and start the loop only on success:

	if err := d.pollAndApplyLocked(); err != nil {
	    return err          // returned HERE
	}
	d.startPollLoop()       // never reached

The daemon runs from a LaunchDaemon with RunAtLoad, so at boot it races the network stack and
losing that race is the ORDINARY case. One DNS failure on the first attempt therefore meant the
poll loop was never started at all: the interface stayed up with no address and no routes, the
session stayed valid, `lastPoll` stayed frozen at daemon start, and nothing ever tried again.
DNS recovered minutes later and no one noticed. The only exits were a manual `vpn up` or a
daemon restart.

The error is still returned, so a caller that wants to report the first failure still can. What
changed is that the daemon keeps TRYING while it does.
*/
func (d *Daemon) beginPollingLocked() error {
	d.startPollLoop()
	return d.pollAndApplyLocked()
}

func (d *Daemon) stopTunnelLocked() {
	if d.engine != nil {
		logEvent("tunnel down on %s", d.engine.InterfaceName())
	}
	if d.cancelPoll != nil {
		// Cancel only, never wait here: the loop goroutine takes d.mu, which this caller holds, so
		// waiting on it under the lock is a deadlock the moment a tick and a stop coincide. The
		// goroutine sees its context cancelled the next time it wakes and exits on its own.
		d.cancelPoll()
		d.cancelPoll = nil
	}
	if d.engine != nil {
		_ = d.platform.Teardown(d.engine.InterfaceName())
		d.engine.Close()
		d.engine = nil
	}
	d.client = nil
	d.doc = nil
	d.plan = Plan{}
	d.revision = ""
	d.lastApplyErr = ""
	d.dns = DNSStatus{Mode: "unavailable", Error: "the VPN is down"}
}

// pollAndApplyLocked fetches the document (conditional on the ETag) and, when it changed,
// converges the engine, routes and DNS to it. A 304 is a no-op. loginRequired tears the tunnel
// peers down but keeps the interface, so `status` can report "sign in again".
func (d *Daemon) pollAndApplyLocked() error {
	if d.client == nil {
		return fmt.Errorf("no tunnel is up")
	}
	d.lastPoll = time.Now()
	res, err := d.client.pollState(d.revision)
	// One place every poll passes through, so the log cannot drift from the status the daemon
	// reports. logPollOutcome writes only on a CHANGE: the first failure, and the recovery.
	logPollOutcome(err)
	if err != nil {
		d.lastPollErr = err.Error()
		return err
	}
	d.lastPollErr = ""
	if res.loginRequired {
		// The session lapsed: apply an empty plan (peers gone) and stop, so the daemon does not
		// hammer a 401 every tick. The interface and address stay so status is legible.
		logEvent("session has lapsed: peers removed, sign in again to restore the tunnel")
		d.applyLoginRequiredLocked()
		return nil
	}
	if res.notModified {
		// Unchanged document, but the machine may have changed under it: sleep/wake and a network
		// change drop routes on macOS and can reset resolver state. Every call below is idempotent,
		// so re-asserting the current plan on each tick is how the tunnel comes back on its own
		// without a wake listener per OS.
		if err := d.convergeRoutesAndDNSLocked(nil, d.plan); err != nil {
			d.lastApplyErr = err.Error()
			return err
		}
		d.lastApplyErr = ""
		return nil
	}
	if err := d.applyDocumentLocked(res.doc); err != nil {
		d.lastApplyErr = err.Error()
		return err
	}
	d.lastApplyErr = ""
	return nil
}

func (d *Daemon) applyLoginRequiredLocked() {
	if d.engine != nil {
		if hex, err := d.state.Active().PrivateKeyHex(); err == nil {
			_ = d.engine.ApplyPlan(hex, Plan{})
		}
		_ = d.platform.Teardown(d.engine.InterfaceName())
	}
	if d.cancelPoll != nil {
		d.cancelPoll()
		d.cancelPoll = nil
	}
	if d.doc != nil {
		d.doc.Device.Session.LoginRequired = true
	}
	d.plan = Plan{LoginRequired: true}
	d.lastApplyErr = ""
	d.dns = DNSStatus{Mode: "unavailable", Error: "the VPN session expired"}
}

// applyDocumentLocked converges the machine to a new document.
func (d *Daemon) applyDocumentLocked(doc *Document) error {
	plan, err := BuildPlan(doc)
	if err != nil {
		return err
	}
	privHex, err := d.state.Active().PrivateKeyHex()
	if err != nil {
		return err
	}

	iface := d.engine.InterfaceName()
	if plan.Address.IsValid() {
		if err := d.platform.SetInterfaceAddress(iface, plan.Address); err != nil {
			return err
		}
	}
	if err := d.engine.ApplyPlan(privHex, plan); err != nil {
		return err
	}
	if err := d.convergeRoutesAndDNSLocked(d.plan.Routes, plan); err != nil {
		return err
	}
	d.doc = doc
	d.plan = plan
	d.revision = doc.Revision
	return nil
}

// convergeRoutesAndDNSLocked brings the routes and the split DNS to the plan. haveRoutes is what
// the daemon believes is on the machine: the previous plan's routes when a document changed
// (so dropped clusters lose their route), nil to re-assert every route (AddRoute is idempotent
// on every platform). Resolvers are always read back from the platform.
func (d *Daemon) convergeRoutesAndDNSLocked(haveRoutes []netip.Prefix, plan Plan) error {
	if d.engine == nil {
		return nil
	}
	iface := d.engine.InterfaceName()
	routeDiff := DiffPrefixes(haveRoutes, plan.Routes)
	for _, prefix := range routeDiff.Add {
		if err := d.platform.AddRoute(iface, prefix); err != nil {
			return err
		}
	}
	for _, prefix := range routeDiff.Remove {
		if err := d.platform.RemoveRoute(iface, prefix); err != nil {
			return err
		}
	}

	clientAddr := netip.Addr{}
	if plan.Address.IsValid() {
		clientAddr = plan.Address.Addr()
	}
	dns, err := d.platform.ReconcileDNS(iface, clientAddr, plan.Resolvers)
	d.dns = dns
	if err != nil {
		return err
	}
	return nil
}

// startPollLoop runs pollAndApply on a ticker until cancelled. Errors are recorded on the daemon,
// never fatal: a control-plane outage must not drop a working tunnel (the cluster-autonomy
// promise), so a failed poll keeps the last applied document.
func (d *Daemon) startPollLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelPoll = cancel
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(d.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.mu.Lock()
				// The tunnel may have been stopped or replaced while this tick waited for the lock;
				// a cancelled context means this loop belongs to a dead tunnel and must not touch
				// the new one.
				if ctx.Err() == nil && d.client != nil {
					_ = d.pollAndApplyLocked()
				}
				d.mu.Unlock()
			}
		}
	}()
}

// Close stops the poll loop and the engine, for daemon shutdown. It does NOT end the session:
// a daemon stopped for an update or a reboot should resume the same session on restart.
func (d *Daemon) Close() {
	d.mu.Lock()
	d.stopTunnelLocked()
	d.mu.Unlock()
	// Outside the lock, so a loop goroutine waiting on d.mu can take it, see its cancelled
	// context and exit.
	d.wg.Wait()
}
