package cmd

import (
	"strings"
	"testing"
)

func TestAccountParentContainsStaticCommands(t *testing.T) {
	for _, path := range []string{"list", "add", "switch", "forget"} {
		if command, _, err := accountCmd.Find([]string{path}); err != nil || command == accountCmd {
			t.Fatalf("account %s is not registered", path)
		}
	}
}

func TestVerifyRequestedAccountRejectsMismatch(t *testing.T) {
	err := verifyRequestedAccount("wanted", "other")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestVerifyRequestedAccountAcceptsAddAndMatchingSwitch(t *testing.T) {
	if err := verifyRequestedAccount("", "account"); err != nil {
		t.Fatal(err)
	}
	if err := verifyRequestedAccount("account", "account"); err != nil {
		t.Fatal(err)
	}
}
