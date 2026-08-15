package vpn

import (
	"context"
	"fmt"
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
	engine   *engine
	client   *conductorClient
	doc      *Document
	plan     Plan
	revision string
	lastPoll time.Time
	lastErr  string

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
	}, nil
}

// Resume brings the tunnel back up on daemon start when the state holds a session that has not
// lapsed: a daemon restart or a reboot must not force a new sign-in while the session is still
// valid. Called once after NewDaemon.
func (d *Daemon) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state.SessionToken == "" || d.state.SessionExpiresAt.Before(time.Now()) {
		return
	}
	if err := d.startTunnelLocked(); err != nil {
		d.lastErr = err.Error()
	}
}

// Handle serves one socket request. It is the only entry point besides Resume.
func (d *Daemon) Handle(req Request) Response {
	d.mu.Lock()
	defer d.mu.Unlock()

	switch req.Op {
	case OpIdentity:
		return Response{Identity: d.identityLocked()}
	case OpStatus:
		return Response{Status: d.statusLocked()}
	case OpUp:
		return d.handleUpLocked(req)
	case OpDown:
		return d.handleDownLocked(false)
	case OpLogout:
		return d.handleDownLocked(true)
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

func (d *Daemon) identityLocked() *Identity {
	return &Identity{
		PublicKey: d.state.PublicKey,
		DeviceID:  d.state.DeviceID,
		AccountID: d.state.AccountID,
		Version:   d.version,
	}
}

// handleUpLocked records a freshly minted session and enrolment (the CLI did the sign-in and the
// mint), then brings the tunnel up and polls at once.
func (d *Daemon) handleUpLocked(req Request) Response {
	if req.SessionToken == "" || req.AccountID == "" || req.DeviceID == "" || req.ConductorURL == "" {
		return Response{Error: "up needs a session token, account, device and conductor url"}
	}
	d.state.SessionToken = req.SessionToken
	d.state.SessionExpiresAt = req.SessionExpiresAt
	d.state.AccountID = req.AccountID
	d.state.DeviceID = req.DeviceID
	d.state.ConductorURL = req.ConductorURL
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
	if d.client != nil {
		if err := d.client.endSession(); err != nil {
			// Report but keep tearing down: a laptop the person told to go down must go down
			// locally even if conductor is unreachable; the session lapses on its own within 24h.
			d.lastErr = "end session: " + err.Error()
		}
	}
	d.stopTunnelLocked()
	if forget {
		d.state.ClearIdentity()
	} else {
		d.state.ClearSession()
	}
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
	d.client = newConductorClient(d.state.ConductorURL, d.state.AccountID, d.state.DeviceID, d.state.SessionToken)
	d.revision = ""
	if err := d.pollAndApplyLocked(); err != nil {
		return err
	}
	d.startPollLoop()
	return nil
}

func (d *Daemon) stopTunnelLocked() {
	if d.cancelPoll != nil {
		d.cancelPoll()
		d.cancelPoll = nil
		d.wg.Wait()
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
	if err != nil {
		d.lastErr = err.Error()
		return err
	}
	if res.loginRequired {
		// The session lapsed: apply an empty plan (peers gone) and stop, so the daemon does not
		// hammer a 401 every tick. The interface and address stay so status is legible.
		d.applyLoginRequiredLocked()
		return nil
	}
	if res.notModified {
		d.lastErr = ""
		return nil
	}
	if err := d.applyDocumentLocked(res.doc); err != nil {
		d.lastErr = err.Error()
		return err
	}
	d.lastErr = ""
	return nil
}

func (d *Daemon) applyLoginRequiredLocked() {
	if d.engine != nil {
		if hex, err := d.state.PrivateKeyHex(); err == nil {
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
}

// applyDocumentLocked converges the machine to a new document.
func (d *Daemon) applyDocumentLocked(doc *Document) error {
	plan, err := BuildPlan(doc)
	if err != nil {
		return err
	}
	privHex, err := d.state.PrivateKeyHex()
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

	// Routes: add the plan's routes, remove any the previous plan had that this one drops.
	routeDiff := DiffPrefixes(d.plan.Routes, plan.Routes)
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

	// DNS: converge the resolver files to the plan's zones.
	have, err := d.platform.Resolvers()
	if err != nil {
		return err
	}
	resolverDiff := DiffResolvers(have, plan.Resolvers)
	changed := false
	for _, r := range resolverDiff.Set {
		if err := d.platform.SetResolver(r.Zone, r.Resolver); err != nil {
			return err
		}
		changed = true
	}
	for _, zone := range resolverDiff.Remove {
		if err := d.platform.RemoveResolver(zone); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		_ = d.platform.FlushDNS()
	}

	d.doc = doc
	d.plan = plan
	d.revision = doc.Revision
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
				// A login-required teardown cancels the loop, so this only runs while up.
				if d.client != nil {
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
	defer d.mu.Unlock()
	d.stopTunnelLocked()
}
