package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

// retiredEnvironment is the CDN environment that no longer exists. A config on it moves to the CDN
// default at the next sign-in.
const retiredEnvironment = "beta"

// endStoredRoidSignIn ends the roid sign-in a config holds, and removes its stored secrets. A var so
// tests stay off the credential store.
var endStoredRoidSignIn = endRoidSignIn

// endSignInWait bounds how long a sign-in waits for the retired environment to confirm the revoke.
// The revoke goes on in the background after that, and the local secrets are removed whatever the
// server says (roidauth.Session.Logout), so the wait is only about not holding the person.
var endSignInWait = 3 * time.Second

func endSignInWithin(cmd *cobra.Command, cfg *config.Config, wait time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		endStoredRoidSignIn(cmd, cfg)
	}()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

/*
moveOffRetiredEnvironment moves a config on a retired environment to the CDN default, at sign-in.

It reports whether it moved the config. A config is on the retired environment when its label says
so and the URLs are still the ones that label wrote, or when its console and API URLs equal the CDN's
entry for it (a label lost or typed by hand). It never touches a custom environment, a hand-set URL
that is not that entry, or a process with RUNOS_API_URL set. When the CDN cannot be read it changes
nothing and the sign-in goes on as before.

URLs, label and issuer come from the CDN default through the same apply functions `runos config
env` uses. The rest of the config stays. The stored session belongs to the retired environment, so
it is ended: every credential is cleared (ClearSession) and a roid sign-in is revoked and removed
from the credential store. One notice says so, on stderr and, for a caller reading the JSON stream,
as a notice event.

This runs at sign-in only. A command start would put a network read, a config write and a session
end in front of every command, including the root-only VPN ones that must not do either.
*/
func moveOffRetiredEnvironment(cmd *cobra.Command, cfg *config.Config, report signInReporter) bool {
	if os.Getenv("RUNOS_API_URL") != "" {
		return false
	}
	switch env := cfg.GetEnv(); {
	case env == retiredEnvironment:
	case env == config.EnvCustom && couldBeCDNEntry(cfg):
	default:
		return false
	}
	rc, err := cdnConfig()
	if err != nil || rc == nil {
		return false
	}
	if cfg.GetEnv() != retiredEnvironment && !onRetiredEntry(cfg, rc) {
		return false
	}
	target, ok := rc.Environments[rc.Default]
	if !ok || rc.Default == retiredEnvironment {
		return false
	}

	before := cfg.Clone()
	cfg.ClearSession()
	cfg.ApplyEnvironment(rc.Default, target.Domains.Console, target.Domains.APIURL())
	if err := cfg.ApplyEnvironmentAuth(target.Auth); err != nil {
		cfg.AuthIssuer = ""
	}
	if err := cfg.Save(); err != nil {
		*cfg = before
		return false
	}

	// The person reads this before the revoke starts, so a host that does not answer is never a
	// silent wait.
	message := fmt.Sprintf("The %s environment is retired, so this CLI now uses the %s environment and has ended your %s sign-in. Sign in again to continue.", retiredEnvironment, rc.Default, retiredEnvironment)
	fmt.Fprintln(cmd.ErrOrStderr(), message)
	if n, ok := report.(signInNoticer); ok {
		n.Notice("environment_retired", message)
	}
	endSignInWithin(cmd, &before, endSignInWait)
	return true
}

// onRetiredEntry reports whether the config's console and API URLs are the CDN's entry for the
// retired environment.
func onRetiredEntry(cfg *config.Config, rc *config.RemoteConfig) bool {
	entry, ok := rc.Environments[retiredEnvironment]
	if !ok {
		return false
	}
	return cfg.ConsoleURL == entry.Domains.Console && cfg.ConductorURL == entry.Domains.APIURL()
}
