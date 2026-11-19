package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/roidapi"
)

// explainRequestVersion is the one accepted request schema version
// (docs/api/scoped-credentials.yaml in the roid repo, ExplainRequest).
const explainRequestVersion = "runos.credential-explanation.v1"

// explainCredentialRef is the safe, secret-free credential identity in an
// explanation response (ExplainCredentialReference in the same document).
// custodialAccountId is absent for a viewer who is not the custodian, so it
// decodes to the empty string rather than a guessed value.
type explainCredentialRef struct {
	Version              string `json:"version"`
	CredentialType       string `json:"credentialType"`
	CredentialID         string `json:"credentialId"`
	PrincipalKind        string `json:"principalKind"`
	PrincipalID          string `json:"principalId"`
	CustodialAccountID   string `json:"custodialAccountId,omitempty"`
	LegacyClassification string `json:"legacyClassification"`
}

// explainPermissionSource names what decided the outcome, without
// including a grant from an unrelated account (PermissionSource in the
// same document).
type explainPermissionSource struct {
	Kind               string   `json:"kind"`
	RoleIDs            []string `json:"roleIds"`
	GrantIDs           []string `json:"grantIds"`
	RestrictionActions []string `json:"restrictionActions"`
}

// explainCredentialResponse is the exact runos.credential-explanation.v1
// response for the explainCredential operation. Its outcome enum is
// permit, deny, needs, or unavailable: there is no "indeterminate" value,
// and conditions is a flat list of safe strings, not an object per entry.
// authoritativeness is authoritative, conditional, or
// legacy_non_authoritative, and is the field that decides whether a permit
// may ever render as allowed (explainCredentialVerdict).
type explainCredentialResponse struct {
	Version           string                  `json:"version"`
	Credential        explainCredentialRef    `json:"credential"`
	Action            string                  `json:"action"`
	Scope             string                  `json:"scope"`
	Outcome           string                  `json:"outcome"`
	Code              string                  `json:"code"`
	Reason            string                  `json:"reason"`
	PolicyVersion     int                     `json:"policyVersion"`
	EvaluatedAt       string                  `json:"evaluatedAt"`
	PermissionSource  explainPermissionSource `json:"permissionSource"`
	Conditions        []string                `json:"conditions"`
	Authoritativeness string                  `json:"authoritativeness"`
	LifecycleState    string                  `json:"lifecycleState"`
	ExpiresAt         *string                 `json:"expiresAt"`
	PolicyFreshness   string                  `json:"policyFreshness"`
}

// buildExplainRequest is the exact request body for explainCredential. The
// CLI never fabricates context it has no honest way to supply (a source
// address, a risk verdict, or proof of a fresh sign-in or approval), so
// context is always empty: an absent requirement stays absent, and the
// server, never the CLI, turns that into a conditional or needs outcome.
func buildExplainRequest(credentialID, action, scope string) map[string]any {
	return map[string]any{
		"version":      explainRequestVersion,
		"credentialId": credentialID,
		"action":       action,
		"scope":        scope,
		"context":      map[string]any{},
	}
}

// explainCredential asks the identity service for the real, current
// decision on one credential, action and scope, through the same
// evaluator as a live request. It returns the decoded response for human
// rendering alongside the raw result, whose body --json prints unchanged.
func (t *accessTarget) explainCredential(credentialID, action, scope string) (*explainCredentialResponse, *api.Result, error) {
	res, err := t.do(roidapi.Call{Op: "explainCredential", Body: buildExplainRequest(credentialID, action, scope)})
	if err != nil {
		return nil, nil, err
	}
	var resp explainCredentialResponse
	if err := json.Unmarshal(res.Body, &resp); err != nil {
		return nil, nil, fmt.Errorf("could not read the explanation: %w", err)
	}
	return &resp, res, nil
}

// explainCredentialVerdict is the one place that turns a response into a
// word a person reads as a decision. Only an authoritative permit renders
// as allowed. Every other combination, including a legacy compatibility
// path that would usually allow the action, renders as something else, so
// the output can never be mistaken for an unconditional allow (story 345
// AC3).
func explainCredentialVerdict(r explainCredentialResponse) string {
	switch {
	case r.Authoritativeness == "legacy_non_authoritative":
		return "non-authoritative (legacy compatibility, not a current decision)"
	case r.Outcome == "deny":
		return "denied"
	case r.Outcome == "unavailable":
		return "not determined"
	case r.Outcome == "needs":
		return "conditional"
	case r.Outcome == "permit" && r.Authoritativeness == "authoritative":
		return "allowed"
	default:
		// A permit that is not authoritative should not occur per the
		// published contract, but never render it as allowed if it does.
		return "not determined"
	}
}

// renderExplainCredential writes the human-readable explanation: the
// selected public identity (never a secret), lifecycle, scope, action, the
// truthful verdict and reason, any missing evidence, what decided it, and
// policy freshness.
func renderExplainCredential(w io.Writer, r explainCredentialResponse) {
	c := r.Credential
	fmt.Fprintf(w, "Credential: %s (%s)\n", c.CredentialID, c.CredentialType)
	fmt.Fprintf(w, "Principal: %s %s\n", c.PrincipalKind, c.PrincipalID)
	if c.CustodialAccountID != "" {
		fmt.Fprintf(w, "Custodial account: %s\n", c.CustodialAccountID)
	}
	if c.LegacyClassification != "" && c.LegacyClassification != "none" {
		fmt.Fprintf(w, "Legacy classification: %s\n", c.LegacyClassification)
	}
	fmt.Fprintf(w, "Lifecycle: %s", r.LifecycleState)
	if r.ExpiresAt != nil {
		fmt.Fprintf(w, " (expires %s)", *r.ExpiresAt)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Scope: %s\n", r.Scope)
	fmt.Fprintf(w, "Action: %s\n", r.Action)
	fmt.Fprintf(w, "Decision: %s\n", explainCredentialVerdict(r))
	if r.Reason != "" {
		fmt.Fprintf(w, "Reason: %s\n", r.Reason)
	}
	if len(r.Conditions) > 0 {
		fmt.Fprintln(w, "Missing or unmet:")
		for _, cond := range r.Conditions {
			fmt.Fprintf(w, "  - %s\n", cond)
		}
	}
	ps := r.PermissionSource
	fmt.Fprintf(w, "Permission source: %s\n", ps.Kind)
	if len(ps.RoleIDs) > 0 {
		fmt.Fprintf(w, "  Roles: %s\n", strings.Join(ps.RoleIDs, ", "))
	}
	if len(ps.GrantIDs) > 0 {
		fmt.Fprintf(w, "  Grants: %s\n", strings.Join(ps.GrantIDs, ", "))
	}
	if len(ps.RestrictionActions) > 0 {
		fmt.Fprintf(w, "  Restricted to: %s\n", strings.Join(ps.RestrictionActions, ", "))
	}
	fmt.Fprintf(w, "Policy: version %d, %s\n", r.PolicyVersion, r.PolicyFreshness)
	fmt.Fprintf(w, "Evaluated at: %s\n", r.EvaluatedAt)
}
