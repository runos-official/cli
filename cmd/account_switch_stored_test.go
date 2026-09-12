package cmd

import (
	"bytes"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

func switchTestCommand() *cobra.Command {
	command := &cobra.Command{}
	command.Flags().BoolP("json", "j", false, "")
	registerSocketFlag(command)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	return command
}

// Nothing stored means the browser, exactly as before. This is every switch on
// the first run after an upgrade, so it must not become an error.
func TestStoredSwitchDefersToTheBrowserWhenItHasNothing(t *testing.T) {
	cfg := &config.Config{}
	cfg.RememberAccount("bbbbb", "2026-01-01T00:00:00Z")
	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "bbbbb", "aaaaa")
	if switched || err != nil {
		t.Fatalf("expected a deferral, got switched=%v err=%v", switched, err)
	}
}

/*
Switching to the account you are already on must NOT open a browser.

Reported by an operator the first time they hit it: it reads as the CLI having
lost the session it had just used. Refreshing a sign-in is what `runos login`
is for.
*/
func TestSwitchingToTheCurrentAccountDoesNotSignInAgain(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	// A resolvable credential: a stored PAT needs no network to resolve.
	cfg.ApplyAPIKeyLogin("aaaaa", "pat-a", "2026-01-01T00:00:00Z")

	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "aaaaa", "aaaaa")
	if !switched || err != nil {
		t.Fatalf("expected a no-op switch, got switched=%v err=%v", switched, err)
	}
}

/*
Unless the credential it is already using is dead.

"Already on it" is unhelpful when the token behind it no longer works: the next
command would be the one to find out.
*/
func TestSwitchingToTheCurrentAccountSignsInWhenItsCredentialIsDead(t *testing.T) {
	cfg := &config.Config{AccountID: "aaaaa"}
	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "aaaaa", "aaaaa")
	if switched || err != nil {
		t.Fatalf("expected a deferral to the browser, got switched=%v err=%v", switched, err)
	}
}

/*
A dead credential must leave the account UNCHANGED before falling back.

Activating and then failing would mean the switch both failed and changed the
account, so the next command would talk to the wrong one. The Firebase exchange
cannot succeed here (there is no server), which is exactly the dead-credential
path.
*/
func TestADeadStoredCredentialDoesNotLeaveTheAccountChanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("bbbbb", &config.FirebaseConfig{APIKey: "fb"}, "token-b", "2026-01-02T00:00:00Z")
	cfg.Firebase = nil // no credential path resolves, so ResolveToken fails

	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "aaaaa", "bbbbb")
	if switched || err != nil {
		t.Fatalf("expected a deferral to the browser, got switched=%v err=%v", switched, err)
	}
	if cfg.AccountID != "bbbbb" {
		t.Fatalf("the failed switch changed the active account to %q", cfg.AccountID)
	}
}
