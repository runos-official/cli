package dynacmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/runos-official/cli/internal/auth"
)

// humanOnlyAPIKeyMessage is conductor's exact sentence for a route every
// API key (and every PAT; same code path) is refused on, whether or not
// the credential itself is valid (src/api/middleware/auth.ts,
// denyApiKey). No stable code distinguishes this from an
// invalid-credential 401 (FCR 2720), so the match is on this exact
// string, kept in this one place: a wording change in conductor fails
// TestFormatRefusalHint instead of silently restoring the "revoked,
// rotated or expired" guidance for a route that was never going to
// accept the credential at all.
const humanOnlyAPIKeyMessage = "Forbidden: API keys cannot perform this action"

// securityKeyRequiredReason is the value Roid's typed-credential create
// routes send when the caller holds no registered security key: 403,
// code authz.needs (objective plan FPL403 ruling R7-6; handbook topic
// api-keys). Review finding 1556: the repo's own already-shipped
// decoders for this exact Roid refusal (internal/roidapi/refusal.go:147,
// internal/kubeaccess/refusal.go:108-117/token.go:117) all put the
// discriminator in `requirement`, not `reason`; securityKeyRequired
// checks both field names, flat and nested under "error", since
// conductor relays Roid's typed-credential refusals as-is and this repo
// has no confirmed live sample of this exact route's wire shape.
const securityKeyRequiredReason = "security_key_required"

// consoleSecurityKeyRefusal is the R7-6 refusal sentence: byte-identical
// across Console, conductor's manifest help (story 423 criterion 6) and
// the CLI's own text (this file, and cmd/access_grants.go's help).
const consoleSecurityKeyRefusal = "This action needs a registered security key. Add a security key on your MFA page, then try again."

// formatRefusalHint renders a 403 the CLI's own surface can explain
// better than conductor's bare sentence: a route closed until the
// caller registers a security key (ruling R7-6), or a route closed to
// every secret credential regardless of validity (FCR 2720). (_, false)
// when neither shape matches, so the caller keeps the default
// rendering.
func formatRefusalHint(err error, credential auth.CredentialKind) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusForbidden {
		return "", false
	}
	if securityKeyRequired(apiErr.Body) {
		return consoleSecurityKeyRefusal, true
	}
	if credential.IsSecretCredential() && humanOnlyAPIKeyRefusal(apiErr.Body) {
		return humanOnlyAPIKeyMessage + "\nThis command needs a human sign-in (`runos login`). The credential itself was not judged.", true
	}
	return "", false
}

// applyRefusalHint is the one gate executor.Execute calls: rewritten
// only for a person (review finding 1555). Under --json the envelope
// must keep the typed *APIError so BuildAPIErrorEnvelope can forward
// `code`/`reason` (or `requirement`) to a machine consumer; conductor's
// bare sentence is what a script branches on, not this CLI's prose.
func applyRefusalHint(err error, credential auth.CredentialKind, jsonOutput bool) error {
	if jsonOutput {
		return err
	}
	if msg, ok := formatRefusalHint(err, credential); ok {
		return fmt.Errorf("%s", msg)
	}
	return err
}

func securityKeyRequired(body []byte) bool {
	var flat struct {
		Reason      string `json:"reason"`
		Requirement string `json:"requirement"`
	}
	_ = json.Unmarshal(body, &flat)
	if flat.Reason == securityKeyRequiredReason || flat.Requirement == securityKeyRequiredReason {
		return true
	}
	var nested struct {
		Error struct {
			Reason      string `json:"reason"`
			Requirement string `json:"requirement"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &nested)
	return nested.Error.Reason == securityKeyRequiredReason || nested.Error.Requirement == securityKeyRequiredReason
}

func humanOnlyAPIKeyRefusal(body []byte) bool {
	var flat struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &flat)
	return flat.Error == humanOnlyAPIKeyMessage
}
