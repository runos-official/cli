package cmd

import (
	"strings"
	"testing"
)

/*
The regression. An operator signed in to recover one account, authenticated as
another, and was told only "Authenticated successfully!". Every later command
failed and the CLI never named the account it had actually signed in as.
*/
func TestLoginNamesTheAccountItSignedInAs(t *testing.T) {
	got := loginOutcomeMessage("", "kkkkk")
	if !strings.Contains(got, "kkkkk") {
		t.Fatalf("the account signed in as must be named, got: %s", got)
	}
}

func TestSigningIntoADifferentAccountNamesBoth(t *testing.T) {
	got := loginOutcomeMessage("kkkkk", "mmmmm")
	if !strings.Contains(got, "mmmmm") || !strings.Contains(got, "kkkkk") {
		t.Fatalf("a switch must name both accounts, got: %s", got)
	}
}

// Because the previous account's credential IS kept, and a reader who has just
// realised they signed into the wrong one needs to know the way back.
func TestASwitchSaysHowToGoBack(t *testing.T) {
	got := loginOutcomeMessage("kkkkk", "mmmmm")
	if !strings.Contains(got, "account switch kkkkk") {
		t.Fatalf("a switch must say how to return, got: %s", got)
	}
}

func TestSameAccountDoesNotClaimASwitch(t *testing.T) {
	got := loginOutcomeMessage("kkkkk", "kkkkk")
	if strings.Contains(got, "switched") {
		t.Fatalf("re-signing into the same account is not a switch, got: %s", got)
	}
	if !strings.Contains(got, "kkkkk") {
		t.Fatalf("it must still name the account, got: %s", got)
	}
}

// Nothing to name is not a reason to say nothing at all.
func TestUnknownAccountStillReportsSuccess(t *testing.T) {
	if loginOutcomeMessage("kkkkk", "") != "Authenticated successfully!" {
		t.Fatal("with no account id it must still report success")
	}
}
