package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

// The `runos vpn` commands drive a local root daemon (internal/vpn) over a unix socket: this
// cannot be a manifest command because it talks to the daemon, not to an endpoint. The daemon
// holds the device key and the session token; the CLI holds the person's Firebase credential and
// does the sign-in, enrolment and session mint. `vpn devices list|show|rename|revoke` are the
// separate manifest commands that merge under this same parent.

var vpnCmd = &cobra.Command{
	Use:   "vpn",
	Short: "Connect this machine to your RunOS clusters over a VPN",
	Long: `Manage the RunOS VPN on this machine.

  runos vpn install      Install the VPN service (needs admin once)
  runos vpn up           Connect to your default cluster (sign in first with 'runos login')
  runos vpn status       Show the tunnel and each cluster
  runos vpn connect <cid> / disconnect <cid>
  runos vpn down         Disconnect and end the session
  runos vpn forget-key   Down, and throw away this machine's VPN key

Each machine is a device with its own key and address. Signing in is 'runos login'
and connecting is 'runos vpn up': one identity, and a tunnel that uses it. A sign-in
lasts 24 hours, after which the tunnel is cut and you sign in again.`,
}

var vpnUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Bring the VPN up (sign in first with 'runos login')",
	RunE:  runVPNUp,
}

var vpnDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Disconnect and end the VPN session",
	RunE:  runVPNDown,
}

var vpnStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the VPN tunnel and each cluster",
	RunE:  runVPNStatus,
}

func init() {
	vpnUpCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnUpCmd.Flags().Bool("no-browser", false, "Report the sign-in link instead of opening a browser; the caller opens it. Use when a UI shows the device code and opens the link on a click")
	vpnUpCmd.Flags().Bool("non-interactive", false, "Never open a browser: fail if a fresh sign-in is needed (for unattended callers such as connect-at-startup)")
	vpnDownCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnStatusCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnCmd.AddCommand(vpnUpCmd, vpnDownCmd, vpnStatusCmd)
	rootCmd.AddCommand(vpnCmd)
}

/*
registerSocketFlag declares the hidden `--socket` override on a command that dials the daemon.

It has to be declared per command, and four commands that dial the socket had never declared it.
`vpnSocketClient` drops the lookup error, so a missing declaration is invisible: the command
silently uses the production socket, which is also why a test could not point one somewhere safe.
*/
func registerSocketFlag(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.Flags().String("socket", "", "path to the daemon control socket (advanced)")
		_ = c.Flags().MarkHidden("socket")
	}
}

// vpnSocketClient is the socket path the CLI dials. A hidden --socket flag on the parent overrides
// it for tests and for a non-default install; defaults to internal/vpn.SocketPath.
func vpnSocketClient(cmd *cobra.Command) *vpn.Client {
	path, _ := cmd.Flags().GetString("socket")
	return vpn.NewClient(path)
}

// refuseVPNWithPAT refuses to bring the VPN up under a stored secret. A tunnel is a person's
// session (decision 2: a sign-in is the 2FA gate), and a PAT is evidence of possession, never of a
// person being present. Reads/other commands are unaffected; only up/connect need a person.
func refuseVPNWithPAT(cfg *config.Config) error {
	if auth.Kind(cfg).IsPAT() {
		return fmt.Errorf("a personal access token cannot bring the VPN up: a tunnel is a person's 24-hour session, not a stored secret.\nSign in interactively with 'runos login', then run 'runos vpn up'")
	}
	return nil
}

/*
Whether an `up` that needs a fresh sign-in may open a browser, as an error or nil.

An unattended caller (the desktop app connecting at login) must never have a browser window
appear on its behalf: at computer startup that is a window nobody asked for at the worst moment.
It fails instead, clearly enough that the caller can put the sign-in in front of the person when
they choose. An interactive `up` is unchanged and signs in as it always did.
*/
func signInRequiredError(nonInteractive bool) error {
	if !nonInteractive {
		return nil
	}
	return fmt.Errorf("the VPN needs a fresh sign-in and this run may not open a browser; run 'runos vpn up' when you are at the machine")
}

/*
vpnSignedOutError turns "no credential" into the one sentence that fixes it.

Kept separate from the reporting below so the wording is a pure function with a test. Any OTHER
resolution failure passes through untouched: a locked keychain is not a sign-out, and rewording it
into one would send the person to a browser that cannot help.
*/
func vpnSignedOutError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, auth.ErrNotAuthenticated) {
		return fmt.Errorf("you are not signed in. Run 'runos login' first, then 'runos vpn up'")
	}
	return err
}

// reportSignedOut returns the sentence AND, under --json, emits it as an event. The stream is the
// only channel a UI driving this command can see; without the event the window shows a spinner and
// then a generic failure, which is what RunOS Desktop did.
func reportSignedOut(cmd *cobra.Command, err error) error {
	wrapped := vpnSignedOutError(err)
	if useJSON, _ := cmd.Flags().GetBool("json"); useJSON && errors.Is(err, auth.ErrNotAuthenticated) {
		(&jsonSignIn{out: cmd.OutOrStdout()}).Failed("not_signed_in", wrapped.Error())
	}
	return wrapped
}

/*
describeAccountChange reports a confirmation that came back as a different person.

Empty means carry on, which is the ordinary case. A sentence means stop: everything downstream is
account-scoped, so continuing would post one account's device id into another account's URL.
*/
func describeAccountChange(before, after string) string {
	if before == after || after == "" {
		return ""
	}
	return fmt.Sprintf(
		"you signed in to account %s, not %s, so the VPN was disconnected. Run 'runos vpn up' to connect %s",
		after, before, after,
	)
}

/*
prepareVPNSession runs the whole account-scoped sequence: this machine's key for THIS account, its
enrolment, and the session mint.

One function because the three steps share an account and must never be retried apart. A nil device
with a nil error is conductor asking for a fresh sign-in; the caller signs in and calls this again
from the top, which re-derives the key and the device id for whatever account the sign-in landed on.
*/
func prepareVPNSession(
	cmd *cobra.Command,
	cfg *config.Config,
	token string,
	daemon *vpn.Client,
) (*vpnDeviceView, *mintedSession, error) {

	// This machine's device key for this account (the daemon generates one on first use).
	identity, err := daemon.Call(vpn.Request{Op: vpn.OpIdentity, AccountID: cfg.GetAccountID()})
	if err != nil {
		return nil, nil, err
	}
	publicKey := identity.Identity.PublicKey

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "this-machine"
	}

	device, needSignIn, err := enrolDevice(cfg, token, publicKey, hostname, runtime.GOOS)
	if needSignIn {
		return nil, nil, nil
	}
	if errors.Is(err, errKeyRevoked) {
		// The key was revoked (console, or an admin) and can never enrol again: rotate it in the
		// daemon and enrol the new one, so a revoked machine is one `up` from working again
		// rather than stuck forever. The old device row stays revoked in the account.
		fmt.Fprintln(cmd.ErrOrStderr(), "This machine's previous VPN key was revoked; enrolling a new one.")
		rotated, rErr := daemon.Call(vpn.Request{Op: vpn.OpRotateKey, AccountID: cfg.GetAccountID()})
		if rErr != nil {
			return nil, nil, rErr
		}
		// THE FLAG, NOT JUST THE ERROR. Dropping it here left `device` nil beside a nil error, so
		// the guard below passed and the next line read `device.ID`: `runos vpn up` died with a Go
		// panic instead of asking for a sign-in, and a caller parsing its JSON got a stack trace on
		// stderr and a signal exit. The first call has honoured this flag since it was written.
		device, needSignIn, err = enrolDevice(cfg, token, rotated.Identity.PublicKey, hostname, runtime.GOOS)
		if needSignIn {
			return nil, nil, nil
		}
	}
	if err != nil {
		return nil, nil, err
	}

	session, needSignIn, err := mintSession(cfg, token, device.ID)
	if err != nil {
		return nil, nil, err
	}
	if needSignIn {
		return nil, nil, nil
	}
	return device, session, nil
}

func runVPNUp(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := refuseVPNWithPAT(cfg); err != nil {
		return err
	}

	daemon := vpnSocketClient(cmd)

	/*
	 CONNECTING CONSUMES A SIGN-IN. IT NEVER CREATES ONE (FPL26 D1).

	 This resolve used to happen before `up` had decided anything, and its failure was returned raw.
	 On a signed-out machine that meant exit 1, an empty stdout and the remedy on stderr, which is
	 invisible to RunOS Desktop: its Sign In button ran this command, read stdout, and so could
	 never sign anybody in. Reported 2026-08-28.

	 `runos login` is now the one place an identity is established, and this says so, in the JSON
	 stream as well so a UI can put the remedy in front of the person.
	*/
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return reportSignedOut(cmd, err)
	}

	device, session, err := prepareVPNSession(cmd, cfg, token, daemon)
	if err != nil {
		return err
	}
	if device == nil {
		/*
		 Conductor wants a sign-in from the last few minutes before it will mint a session. That is
		 the "2FA on the VPN" gate and it is deliberate, so the answer is to ask the person to
		 confirm it is them and then RUN THE WHOLE SEQUENCE AGAIN.

		 The whole sequence, not just the failed step. The device key and the device id are both
		 account-scoped: retrying only the mint carried the previous account's device id into the new
		 account's URL, which conductor answers 404 for, and left the daemon holding a key that
		 account never enrolled. That is the "sign in twice" report, and a tunnel that came up and
		 routed nothing.
		*/
		accountBefore := cfg.GetAccountID()
		if cfg, token, err = signInAndReload(cmd); err != nil {
			return err
		}
		// D3: the tunnel never outlives the identity that opened it. A confirmation that came back
		// on another account is an account SWITCH, so the old tunnel goes down and the person
		// connects the new account deliberately rather than by accident.
		if changed := describeAccountChange(accountBefore, cfg.GetAccountID()); changed != "" {
			// The teardown itself belongs to the sign-in, not to this caller: EVERY path that
			// changes identity has to do it, and having it here meant `runos login` did not. See
			// `reportVPNAccountChange`. This is only the part that is specific to `vpn up`:
			// stopping, rather than connecting an account the person did not ask for.
			return errors.New(changed)
		}
		if device, session, err = prepareVPNSession(cmd, cfg, token, daemon); err != nil {
			return err
		}
		if device == nil {
			return fmt.Errorf("the sign-in did not refresh the session window; try 'runos login' then 'runos vpn up' again")
		}
	}

	// Hand the session to the daemon, which brings the tunnel up and polls the desired state.
	up, err := daemon.Call(vpn.Request{
		Op:               vpn.OpUp,
		SessionToken:     session.Token,
		SessionExpiresAt: session.ExpiresAt,
		AccountID:        cfg.GetAccountID(),
		DeviceID:         device.ID,
		ConductorURL:     cfg.GetAPIURL(),
	})
	if err != nil {
		return err
	}

	// First connection: if the device is connected to nothing, connect it to the CLI's default
	// cluster (decision 3). A device that already has a connected set keeps it.
	status := up.Status
	if status != nil && !anyConnected(status) {
		if def := cfg.GetDefaultClusterID(); def != "" {
			if connected, cErr := daemon.Call(vpn.Request{Op: vpn.OpConnect, CID: def}); cErr == nil {
				status = connected.Status
			}
		}
	}
	return emitVPNStatus(cmd, status)
}

func runVPNDown(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpDown})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

func runVPNStatus(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpStatus})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

func anyConnected(status *vpn.Status) bool {
	for _, c := range status.Clusters {
		if c.Connected {
			return true
		}
	}
	return false
}

// emitVPNStatus renders a status both ways: the whole struct in --json mode, a human summary
// otherwise. The human view leads with the session (the thing that lapses) then each cluster.
func emitVPNStatus(cmd *cobra.Command, status *vpn.Status) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		data, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}
	return printVPNStatus(cmd, status)
}

/*
Run the browser sign-in, then reload what it changed.

Shared by the two places `vpn up` can be told to sign in: conductor refusing the enrolment because
the whole session aged out, and refusing the session mint because the sign-in is not recent enough.
One remedy, one implementation; two copies of a re-auth path is how they end up differing.

Under --json the sign-in reports as events on stdout, so a caller driving this from a UI can show
the device id, the URL and a status that changes. See login_events.go.
*/
func signInAndReload(cmd *cobra.Command) (*config.Config, string, error) {
	nonInteractive, _ := cmd.Flags().GetBool("non-interactive")
	if err := signInRequiredError(nonInteractive); err != nil {
		return nil, "", err
	}
	useJSON, _ := cmd.Flags().GetBool("json")
	if !useJSON {
		fmt.Fprintln(cmd.ErrOrStderr(), "This VPN session needs a fresh sign-in.")
	}
	var report signInReporter = textSignIn{out: cmd.OutOrStdout()}
	if useJSON {
		report = &jsonSignIn{out: cmd.OutOrStdout()}
	}
	noBrowser, _ := cmd.Flags().GetBool("no-browser")
	if err := interactiveLoginReporting(cmd, report, !useJSON, !noBrowser); err != nil {
		return nil, "", err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil, "", err
	}
	return cfg, token, nil
}
