package cmd

import (
	"strings"
	"testing"
)

/*
`runos vpn logout` does not log anybody out, and under FPL26 D1 that name is now a trap.

BEFORE THIS WORK the word "logout" meant roughly the same thing everywhere, because the app and the
CLI both treated a VPN session as the sign-in. It does not any more:

	runos logout        ends the IDENTITY, and drops the tunnel with it
	runos vpn logout    forgets this machine's KEY, and leaves the identity alone

Two commands one word apart, doing unrelated things, one of them irreversible. And its own
confirmation said "log out is not reversible without a fresh sign-in", which is wrong twice: nobody
is logged out, and a fresh sign-in is not what recovers it.

MEASURED on a live account 2026-08-31: it carries three device rows for one laptop, one live and two
revoked, each left behind by a key wipe. That is what this command does, and its name says none of
it.
*/

func TestTheKeyCommandSaysWhatItActuallyDoes(t *testing.T) {
	if vpnForgetKeyCmd.Use != "forget-key" {
		t.Errorf("the command must be named for what it does, got %q", vpnForgetKeyCmd.Use)
	}
	short := strings.ToLower(vpnForgetKeyCmd.Short)
	if strings.Contains(short, "log out") || strings.Contains(short, "logout") {
		t.Errorf("it must not describe itself as a sign-out, got %q", vpnForgetKeyCmd.Short)
	}
	if !strings.Contains(short, "key") {
		t.Errorf("it must say a key is what goes, got %q", vpnForgetKeyCmd.Short)
	}
}

// `runos vpn logout` is in muscle memory and in scripts, so it keeps working. It is hidden from the
// help, because the help is where somebody LEARNS the vocabulary and it must teach the right one.
func TestTheOldNameStillWorksButIsNoLongerAdvertised(t *testing.T) {
	var alias string
	for _, name := range vpnForgetKeyCmd.Aliases {
		if name == "logout" {
			alias = name
		}
	}
	if alias == "" {
		t.Error("`runos vpn logout` must keep working; it is in scripts and in muscle memory")
	}
	if !vpnForgetKeyCmd.Hidden && strings.Contains(vpnCmd.Long, "vpn logout") {
		t.Error("the help must teach the new name, not the one that misleads")
	}
}

/*
And the confirmation must describe the actual consequence.

The old text: "log out is not reversible without a fresh sign-in" and "this machine signs in fresh
next time". Neither is true. Nothing signs in; the machine ENROLS A NEW KEY, which is a new device
row on the account, which is the only lasting effect and the only thing worth warning about.
*/
func TestTheConfirmationWarnsAboutTheThingThatActuallyHappens(t *testing.T) {
	prompt := forgetKeyPrompt()
	lower := strings.ToLower(prompt)

	if strings.Contains(lower, "sign in fresh") || strings.Contains(lower, "signs in fresh") {
		t.Errorf("no sign-in is involved, got %q", prompt)
	}
	if !strings.Contains(lower, "new device") {
		t.Errorf("must warn that a new device row appears on the account, got %q", prompt)
	}
	if !strings.Contains(lower, "key") {
		t.Errorf("must name what is destroyed, got %q", prompt)
	}
}

// The verb in the refusal is the command's own, so a non-TTY caller is told what it declined to do.
func TestTheNonTTYRefusalNamesTheRightAction(t *testing.T) {
	err := confirmRefusal("forget this machine's VPN key")
	if err == nil {
		t.Fatal("a non-TTY caller must be refused, not silently obeyed")
	}
	if strings.Contains(err.Error(), "log out") {
		t.Errorf("it is not a log out, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("must name the way to proceed deliberately, got %q", err.Error())
	}
}
