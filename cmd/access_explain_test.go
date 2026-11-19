package cmd

import (
	"strings"
	"testing"
)

func TestValidateExplainSelectorRequiresExactlyOneSubject(t *testing.T) {
	for _, tc := range []struct {
		name               string
		person, credential string
		action             string
		accountGiven       bool
		ok                 bool
	}{
		{"person only", "p_1", "", "", false, true},
		{"credential only, with account and action", "", "pat_abc123", "clusters.read", true, true},
		{"neither", "", "", "", false, false},
		{"both", "p_1", "pat_abc123", "clusters.read", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExplainSelector(tc.person, tc.credential, tc.action, tc.accountGiven)
			if (err == nil) != tc.ok {
				t.Fatalf("got %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestValidateExplainSelectorRequiresAnExplicitAccountForACredential(t *testing.T) {
	err := validateExplainSelector("", "pat_abc123", "clusters.read", false)
	if err == nil || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("got %v, want a complaint naming --account", err)
	}
}

func TestValidateExplainSelectorRequiresAnActionForACredential(t *testing.T) {
	err := validateExplainSelector("", "pat_abc123", "", true)
	if err == nil || !strings.Contains(err.Error(), "--action") {
		t.Fatalf("got %v, want a complaint naming --action", err)
	}
}

func TestValidateExplainSelectorDoesNotRequireAnAccountOrActionForAPerson(t *testing.T) {
	if err := validateExplainSelector("p_1", "", "", false); err != nil {
		t.Fatalf("person path: %v", err)
	}
}

// A bearer-shaped --credential value must never reach a request, and the
// refusal must never echo it back, in case it is a real secret someone
// pasted by mistake.
func TestRejectSecretShapedCredentialCatchesEverySecretPrefixWithoutEchoingIt(t *testing.T) {
	const canary = "CANARY-SECRET-VALUE-DO-NOT-PRINT"
	for _, bad := range []string{
		"runos_pat_" + canary,
		"runos_key_" + canary,
		"rk_" + canary,
	} {
		t.Run(bad[:8], func(t *testing.T) {
			err := rejectSecretShapedCredential(bad)
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error echoed the secret: %v", err)
			}
		})
	}
}

func TestRejectSecretShapedCredentialAcceptsAPublicID(t *testing.T) {
	for _, good := range []string{"pat_abc123", "key_abc123"} {
		if err := rejectSecretShapedCredential(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestValidateExplainSelectorRefusesASecretShapedCredentialBeforeAnythingElse(t *testing.T) {
	err := validateExplainSelector("", "runos_pat_x.y", "clusters.read", true)
	if err == nil || !strings.Contains(err.Error(), "public id") {
		t.Fatalf("got %v", err)
	}
}
