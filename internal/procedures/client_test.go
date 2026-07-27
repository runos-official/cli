package procedures

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
)

// Q&A 131 narrowed this to the two control RELEASES; approving no longer
// refuses a PAT. What the test still pins is that BOTH PAT shapes are
// caught (env and on-disk, the drift this whole predicate exists to
// prevent) and that the refusal does not read as a role problem, since
// no role turns a stored secret into a person.
func TestRefuseStoredSecretRefusesBothPATShapes(t *testing.T) {
	for _, kind := range []auth.CredentialKind{auth.CredentialEnvPAT, auth.CredentialStoredPAT} {
		t.Run(string(kind), func(t *testing.T) {
			client := &Client{kind: kind}
			err := client.RefuseStoredSecret("release a Procedure kill switch")
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
			// A PAT keeps every other act on this surface, and the
			// refusal says so rather than implying a blanket ban.
			if !strings.Contains(message, "stay available under a PAT") {
				t.Error("the refusal does not tell the user which acts remain open to them")
			}
		})
	}
}

// The other half of the same rule: an interactive session must NOT be
// refused. A refusal that fired on everything would pass the test above
// and make the whole surface unusable.
func TestRefuseStoredSecretAllowsAnInteractiveSession(t *testing.T) {
	client := &Client{kind: auth.CredentialInteractive}
	if err := client.RefuseStoredSecret("release a Procedure kill switch"); err != nil {
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
