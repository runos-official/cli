package cmd

import (
	"strings"
	"testing"
)

func TestValidateExplainSelectorRequiresExactlyOneSubject(t *testing.T) {
	for _, tc := range []struct {
		name               string
		person, credential string
		action, account    string
		ok                 bool
	}{
		{"person only", "p_1", "", "", "", true},
		{"credential only, with account and action", "", "pat_abc123", "clusters.read", "a1", true},
		{"neither", "", "", "", "", false},
		{"both", "p_1", "pat_abc123", "clusters.read", "a1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExplainSelector(tc.person, tc.credential, tc.action, tc.account)
			if (err == nil) != tc.ok {
				t.Fatalf("got %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// Review finding 1081: Cobra reports Changed("account") as true for
// --account="", and the shared session then falls back to the active
// account for an empty value. A credential explanation must never reach
// that fallback, so validateExplainSelector takes the flag's actual
// string value and refuses blank, not merely "the flag was present".
func TestValidateExplainSelectorRequiresANonBlankAccountForACredential(t *testing.T) {
	err := validateExplainSelector("", "pat_abc123", "clusters.read", "")
	if err == nil || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("got %v, want a complaint naming --account", err)
	}
	if err := validateExplainSelector("", "pat_abc123", "clusters.read", "a1"); err != nil {
		t.Fatalf("a real account was refused: %v", err)
	}
}

func TestValidateExplainSelectorRequiresAnActionForACredential(t *testing.T) {
	err := validateExplainSelector("", "pat_abc123", "", "a1")
	if err == nil || !strings.Contains(err.Error(), "--action") {
		t.Fatalf("got %v, want a complaint naming --action", err)
	}
}

func TestValidateExplainSelectorDoesNotRequireAnAccountOrActionForAPerson(t *testing.T) {
	if err := validateExplainSelector("p_1", "", "", ""); err != nil {
		t.Fatalf("person path: %v", err)
	}
}

// Review findings 1082 and 1084: a prefix-only check lets a pasted bearer
// through when it is wrapped in anything the real PublicId grammar does
// not allow (a label, quotes, leading whitespace), because those only
// change what comes BEFORE the secret. validatePublicCredentialID checks
// the whole published grammar instead: a fixed charset (which a space or
// quote is never part of), and the schema's own unanchored runos_pat_ and
// runos_key_ exclusions plus the anchored rk_ exclusion.
func TestValidatePublicCredentialIDMatchesThePublishedGrammarNotJustAPrefix(t *testing.T) {
	const canary = "CANARY-SECRET-VALUE-DO-NOT-PRINT"
	bad := []string{
		"runos_pat_" + canary,
		"runos_key_" + canary,
		"rk_" + canary,
		"Bearer runos_pat_" + canary,
		"\"runos_key_" + canary + "\"",
		" runos_pat_" + canary,
		"pat_" + canary + "_runos_pat_x", // runos_pat_ appears mid-string, not just as a prefix
	}
	for _, v := range bad {
		t.Run(v[:8], func(t *testing.T) {
			err := validatePublicCredentialID(v)
			if err == nil {
				t.Fatalf("%q: want an error", v)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error echoed the secret: %v", err)
			}
		})
	}
	good := []string{"pat_abc123", "key_abc123", "pat_work_rk_1"} // "rk_" present but not a prefix
	for _, v := range good {
		if err := validatePublicCredentialID(v); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
}

func TestValidateExplainSelectorRefusesASecretShapedCredentialBeforeAnythingElse(t *testing.T) {
	err := validateExplainSelector("", "runos_pat_x.y", "clusters.read", "a1")
	if err == nil || !strings.Contains(err.Error(), "public id") {
		t.Fatalf("got %v", err)
	}
}
