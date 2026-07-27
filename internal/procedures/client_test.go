package procedures

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
)

// Q&A 120 and 130's first consequence: a CLI session authenticated only
// by a PAT must refuse the approval command, and the refusal must not
// read as a role problem. A message saying "wrong role" sends the user
// to ask an admin for a permission that cannot help: no role turns a
// stored secret into a person.
func TestRefuseStoredSecretRefusesBothPATShapes(t *testing.T) {
	for _, kind := range []auth.CredentialKind{auth.CredentialEnvPAT, auth.CredentialStoredPAT} {
		t.Run(string(kind), func(t *testing.T) {
			client := &Client{kind: kind}
			err := client.RefuseStoredSecret("approve a Procedure operation")
			if err == nil {
				t.Fatal("a PAT was not refused")
			}
			message := err.Error()
			if !strings.Contains(message, "runos login") {
				t.Error("the refusal does not name the recovery path")
			}
			if !strings.Contains(message, "not a permission problem") {
				t.Error("the refusal does not say it is not a permission problem, so it reads as a role error")
			}
			if strings.Contains(strings.ToLower(message), "role not authorized") {
				t.Error("the refusal reads as a role error and sends the user to fix the wrong thing")
			}
			// The distinction Q&A 130 asks the implementation to keep
			// apart: a PAT may still INVOKE.
			if !strings.Contains(message, "procedures run") {
				t.Error("the refusal does not tell the user that invoking is a different act still open to them")
			}
		})
	}
}

// The other half of the same rule: an interactive session must NOT be
// refused. A refusal that fired on everything would pass the test above
// and make the whole surface unusable.
func TestRefuseStoredSecretAllowsAnInteractiveSession(t *testing.T) {
	client := &Client{kind: auth.CredentialInteractive}
	if err := client.RefuseStoredSecret("approve a Procedure operation"); err != nil {
		t.Fatalf("an interactive session was refused: %v", err)
	}
}

// CredentialNone reaching here would mean NewClient returned a client
// with no credential, which ResolveToken already refuses. Pinned so a
// future change that made the client constructible without a credential
// cannot silently make the PAT refusal the only thing standing between
// an unauthenticated caller and a decision request.
func TestRefuseStoredSecretDoesNotClaimAnAbsentCredentialIsAPAT(t *testing.T) {
	client := &Client{kind: auth.CredentialNone}
	if err := client.RefuseStoredSecret("approve"); err != nil {
		t.Fatalf("CredentialNone must not be reported as a PAT: %v", err)
	}
}

func TestDecisionCodeReadsConductorsRefusalCode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"freshness", `{"error":"Cannot decide","code":"reauthentication_required","reason":"..."}`, "reauthentication_required"},
		{"pat", `{"error":"Cannot decide","code":"approval_requires_interactive_human"}`, "approval_requires_interactive_human"},
		{"role", `{"code":"role_not_authorized"}`, "role_not_authorized"},
		{"no code", `{"error":"Plan mismatch","planHash":"abc"}`, ""},
		{"not json", `<html>gateway timeout</html>`, ""},
		{"empty", ``, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := DecisionCode(&api.Result{StatusCode: 409, Body: []byte(testCase.body)})
			if got != testCase.want {
				t.Fatalf("DecisionCode = %q, want %q", got, testCase.want)
			}
		})
	}
	if got := DecisionCode(nil); got != "" {
		t.Fatalf("DecisionCode(nil) = %q, want empty", got)
	}
}
