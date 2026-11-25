package cmd

import (
	"bytes"
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
	writeConditionGroups(w, r.Conditions)
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

// conditionGroup buckets one condition tag by the producer's own
// vocabulary (roid/internal/server/credentialexplain.go at commit
// 5acd44af): a tag ending ":satisfied" already passed; a tag starting
// "needs:" or the literal "risk:unassessed" is an unmet requirement;
// everything else (credential:*, grant:<id>, grant:source_range:*,
// reason:*, account:*:satisfied is covered above) is restriction or
// decision evidence the server reports regardless of outcome, neither
// satisfied nor missing. Review finding 1080: the renderer used to put
// every tag under "Missing or unmet", which misreported a satisfied
// condition (for example "grant:window:satisfied") on an authoritative
// permit.
func conditionGroup(tag string) string {
	switch {
	case strings.HasSuffix(tag, ":satisfied"):
		return "satisfied"
	case strings.HasPrefix(tag, "needs:"), tag == "risk:unassessed":
		return "unmet"
	default:
		return "evidence"
	}
}

// writeConditionGroups renders the server's condition tags under the
// label their own vocabulary implies, instead of one undifferentiated
// list.
func writeConditionGroups(w io.Writer, conditions []string) {
	var unmet, satisfied, evidence []string
	for _, c := range conditions {
		switch conditionGroup(c) {
		case "unmet":
			unmet = append(unmet, c)
		case "satisfied":
			satisfied = append(satisfied, c)
		default:
			evidence = append(evidence, c)
		}
	}
	writeConditionList(w, "Missing or unmet:", unmet)
	writeConditionList(w, "Satisfied:", satisfied)
	writeConditionList(w, "Other evidence:", evidence)
}

func writeConditionList(w io.Writer, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w, label)
	for _, c := range items {
		fmt.Fprintf(w, "  - %s\n", c)
	}
}

// renderExplainCredentialTo writes either the human explanation or the
// server's own response (pretty-printed, byte-for-byte faithful to
// rawBody), matching --json. Both the command and its tests call this one
// function, so a test can assert the exact text either mode produces
// (review finding 1083: the prior tests never exercised the command's
// real --json output path).
func renderExplainCredentialTo(w io.Writer, jsonOut bool, resp explainCredentialResponse, rawBody []byte) error {
	if !jsonOut {
		renderExplainCredential(w, resp)
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(rawBody))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		_, err := w.Write(rawBody)
		return err
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(pretty))
	return err
}
