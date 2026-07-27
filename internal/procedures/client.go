package procedures

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

// Client talks to Conductor's Procedure surface for one account.
//
// The token is resolved ONCE at construction and the credential Kind is
// kept beside it, so a command that must refuse a stored secret asks the
// same object that will send the credential. Re-deriving the kind at the
// call site is how a refusal and a request come to disagree.
type Client struct {
	api   *api.Client
	token string
	kind  auth.CredentialKind
	aid   string
}

// NewClient resolves the account, base URL and credential from config.
func NewClient(cfg *config.Config) (*Client, error) {
	aid := cfg.GetAccountID()
	if aid == "" {
		return nil, fmt.Errorf("no account id configured; run 'runos login' or set %s", auth.AccountIDEnvVar)
	}
	baseURL := cfg.GetAPIURL()
	if baseURL == "" {
		return nil, fmt.Errorf("no API URL configured; run 'runos config env <environment>'")
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{api: api.NewClient(baseURL), token: token, kind: auth.Kind(cfg), aid: aid}, nil
}

// Kind reports which credential this client will send.
func (c *Client) Kind() auth.CredentialKind { return c.kind }

// RefuseStoredSecret returns a refusal when this client is authenticated
// by a PAT.
//
// SCOPE NARROWED BY Q&A 131, which supersedes Q&A 120: approving,
// rejecting and revoking no longer refuse a PAT, because a developer
// running this CLI under a PAT is a person at a keyboard. What still
// refuses is RELEASING a kill switch or a scope freeze, which Q&A 131
// deliberately left undecided and where conductor still mounts
// `denyApiKey`. Those refuse for a different stated reason: releasing is
// the direction that restores the ability to mutate.
//
// THE REFUSAL IS CLIENT-SIDE ON PURPOSE. Conductor refuses these too, so
// this is not the security boundary; a command that let the request
// travel and reported the server's 403 would be relying on the boundary
// rather than being one.
//
// THE WORDING MATTERS AS MUCH AS THE REFUSAL. A message that reads as
// "wrong role" sends the user to fix a permission, which will not help:
// no role makes a stored secret into a person. The recovery is a
// different credential, so that is what this says.
func (c *Client) RefuseStoredSecret(act string) error {
	if !c.kind.IsPAT() {
		return nil
	}
	where := "the RUNOS_API_KEY environment variable"
	if c.kind == auth.CredentialStoredPAT {
		where = "a personal access token stored by 'runos login --api-key'"
	}
	return fmt.Errorf(
		"a personal access token cannot %s.\n\n"+
			"This session authenticates with %s. A stored secret is evidence of possession, never of a\n"+
			"person being present, and releasing a control is the direction that restores the ability\n"+
			"to mutate, so it is the half a stored secret does not reach.\n\n"+
			"This is not a permission problem. Sign in interactively with 'runos login' and retry.\n"+
			"(Approving, rejecting, revoking and invoking all stay available under a PAT.)",
		act, where)
}

func (c *Client) clusterPath(cid, suffix string) string {
	return "/" + url.PathEscape(c.aid) + "/" + url.PathEscape(cid) + suffix
}

func (c *Client) accountPath(suffix string) string {
	return "/" + url.PathEscape(c.aid) + suffix
}

// Catalog lists every registered Procedure for a cluster.
func (c *Client) Catalog(cid string) ([]CatalogEntry, error) {
	result, err := c.api.Do(http.MethodGet, c.clusterPath(cid, "/procedures"), c.token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		return nil, statusError(result, "list the Procedure catalog")
	}
	var body struct {
		Data []CatalogEntry `json:"data"`
	}
	if err := result.Decode(&body); err != nil {
		return nil, err
	}
	return body.Data, nil
}

// Lookup finds one catalog entry by its immutable `id@version` ref.
func (c *Client) Lookup(cid, ref string) (*CatalogEntry, error) {
	entries, err := c.Catalog(cid)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Ref == ref {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("no Procedure is registered as %q in this cluster; run 'runos procedures list' to see the catalog. A Procedure is cited by its immutable id@version", ref)
}

// PlanOutcome is what an operations POST produced: exactly one of the
// three is non-nil. Blocked is not an error: it is a deterministic
// refusal a human needs to read in full.
type PlanOutcome struct {
	Created *CreatedOperation
	DryRun  *DryRun
	Blocked *Blocked
	Invalid *InvalidArgs
}

// CreateOperation builds a plan and, unless dryRun, persists an
// operation. See the handbook article `procedures-create-operation`.
func (c *Client) CreateOperation(cid, ref string, args map[string]any, dryRun bool) (*PlanOutcome, error) {
	path := c.clusterPath(cid, "/procedures/"+url.PathEscape(ref)+"/operations")
	if dryRun {
		path += "?dryRun=true"
	}
	// An empty argument map must still be an object on the wire: a nil
	// body would be sent with no Content-Type and conductor would read
	// the arguments as absent rather than as empty.
	if args == nil {
		args = map[string]any{}
	}
	result, err := c.api.Do(http.MethodPost, path, c.token, args)
	if err != nil {
		return nil, err
	}

	switch {
	case result.StatusCode == http.StatusConflict:
		var blocked Blocked
		if err := result.Decode(&blocked); err != nil {
			return nil, err
		}
		return &PlanOutcome{Blocked: &blocked}, nil
	case result.StatusCode == http.StatusBadRequest:
		var invalid InvalidArgs
		if err := result.Decode(&invalid); err != nil {
			return nil, err
		}
		return &PlanOutcome{Invalid: &invalid}, nil
	case !result.OK():
		return nil, statusError(result, "create the operation")
	case dryRun:
		var run DryRun
		if err := result.Decode(&run); err != nil {
			return nil, err
		}
		return &PlanOutcome{DryRun: &run}, nil
	default:
		var created CreatedOperation
		if err := result.Decode(&created); err != nil {
			return nil, err
		}
		return &PlanOutcome{Created: &created}, nil
	}
}

// Operation reads one operation and its approval render.
func (c *Client) Operation(cid, operationID string) (*Operation, error) {
	path := c.clusterPath(cid, "/procedures/operations/"+url.PathEscape(operationID))
	result, err := c.api.Do(http.MethodGet, path, c.token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		return nil, statusError(result, "read the operation")
	}
	var operation Operation
	if err := result.Decode(&operation); err != nil {
		return nil, err
	}
	return &operation, nil
}

// Decide approves or rejects, naming the exact plan hash the human read.
//
// planHash is a REQUIRED parameter rather than something this function
// fetches for itself. Reading the hash here would let the CLI approve a
// plan that changed between the render and the decision, which is the
// one thing the hash binding exists to prevent.
func (c *Client) Decide(cid, operationID, decision, planHash string) (*DecisionResult, *api.Result, error) {
	path := c.clusterPath(cid, "/procedures/operations/"+url.PathEscape(operationID)+"/decision")
	result, err := c.api.Do(http.MethodPost, path, c.token, map[string]string{
		"decision": decision,
		"planHash": planHash,
	})
	if err != nil {
		return nil, nil, err
	}
	if !result.OK() {
		// The raw result is returned so the caller can distinguish
		// `reauthentication_required` (recoverable by signing in again)
		// from every other refusal, which it cannot do from a message.
		return nil, result, statusError(result, "record the decision")
	}
	var decided DecisionResult
	if err := result.Decode(&decided); err != nil {
		return nil, result, err
	}
	return &decided, result, nil
}

// Revoke withdraws an authorization the reconciler has not consumed.
func (c *Client) Revoke(cid, operationID string) (*DecisionResult, error) {
	path := c.clusterPath(cid, "/procedures/operations/"+url.PathEscape(operationID)+"/revoke")
	result, err := c.api.Do(http.MethodPost, path, c.token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		return nil, statusError(result, "revoke the authorization")
	}
	var revoked DecisionResult
	if err := result.Decode(&revoked); err != nil {
		return nil, err
	}
	return &revoked, nil
}

// KillSwitchList reads the account's live kill switches with the note
// that rides on the response. The note is returned rather than dropped:
// a platform-wide switch also stops this account's work and is not in
// the list, so an empty list is not proof that nothing is stopping work.
func (c *Client) KillSwitchList() ([]KillSwitch, string, error) {
	result, err := c.api.Do(http.MethodGet, c.accountPath("/procedures/kill-switches"), c.token, nil)
	if err != nil {
		return nil, "", err
	}
	if !result.OK() {
		return nil, "", statusError(result, "list kill switches")
	}
	var body struct {
		Data []KillSwitch `json:"data"`
		Note string       `json:"note"`
	}
	if err := result.Decode(&body); err != nil {
		return nil, "", err
	}
	return body.Data, body.Note, nil
}

// KillSwitchEngage stops Procedure work at the scope named. A PAT MAY do
// this: engaging is the safe direction, and an incident is exactly when
// a script needs to be able to stop things.
func (c *Client) KillSwitchEngage(scope, cid, procedureID, reason string) (string, error) {
	body := map[string]any{"scope": scope, "reason": reason}
	if cid != "" {
		body["cid"] = cid
	}
	if procedureID != "" {
		body["procedureId"] = procedureID
	}
	result, err := c.api.Do(http.MethodPost, c.accountPath("/procedures/kill-switches"), c.token, body)
	if err != nil {
		return "", err
	}
	if !result.OK() {
		return "", statusError(result, "engage the kill switch")
	}
	var engaged struct {
		SwitchID string `json:"switchId"`
	}
	if err := result.Decode(&engaged); err != nil {
		return "", err
	}
	return engaged.SwitchID, nil
}

// KillSwitchRelease lifts a live kill switch.
func (c *Client) KillSwitchRelease(switchID string) error {
	path := c.accountPath("/procedures/kill-switches/" + url.PathEscape(switchID))
	result, err := c.api.Do(http.MethodDelete, path, c.token, nil)
	if err != nil {
		return err
	}
	if !result.OK() {
		return statusError(result, "release the kill switch")
	}
	return nil
}

// FreezeList reads the account's live scope freezes.
func (c *Client) FreezeList() ([]ScopeFreeze, error) {
	result, err := c.api.Do(http.MethodGet, c.accountPath("/procedures/scope-freezes"), c.token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		return nil, statusError(result, "list scope freezes")
	}
	var body struct {
		Data []ScopeFreeze `json:"data"`
	}
	if err := result.Decode(&body); err != nil {
		return nil, err
	}
	return body.Data, nil
}

// FreezeRelease is the "authorized human recovery" that lifts a freeze.
func (c *Client) FreezeRelease(freezeID string) error {
	path := c.accountPath("/procedures/scope-freezes/" + url.PathEscape(freezeID))
	result, err := c.api.Do(http.MethodDelete, path, c.token, nil)
	if err != nil {
		return err
	}
	if !result.OK() {
		return statusError(result, "release the scope freeze")
	}
	return nil
}

// statusError turns a non-2xx into an error that keeps Conductor's own
// wording. Conductor's refusals on this surface explain themselves in
// full sentences, and replacing them with a status code is how a user
// loses the only explanation they get.
func statusError(result *api.Result, act string) error {
	if message := result.ErrorMessage(); message != "" {
		return fmt.Errorf("could not %s (HTTP %d): %s", act, result.StatusCode, message)
	}
	return fmt.Errorf("could not %s: HTTP %d", act, result.StatusCode)
}

// DecisionCode extracts conductor's machine-readable refusal code from a
// failed decision (`approval_requires_interactive_human`,
// `reauthentication_required`, `role_not_authorized`,
// `membership_recheck_failed`, `authentication_instant_unknown`,
// `authentication_instant_in_future`). Returns "" when the body carries
// none, so a caller falls back to the message rather than to a guess.
func DecisionCode(result *api.Result) string {
	if result == nil {
		return ""
	}
	var body struct {
		Code string `json:"code"`
	}
	if result.Decode(&body) != nil {
		return ""
	}
	return body.Code
}
