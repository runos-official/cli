package dynacmd

import (
	"encoding/json"
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

// securityKeyRequiredReason is the reason Roid's typed-credential create
// routes send when the caller holds no registered security key: 403,
// code authz.needs, reason security_key_required (objective plan
// FPL403 ruling R7-6; handbook topic api-keys). Conductor relays Roid's
// typed-credential refusals as-is, so the body may carry this flat, in
// conductor's own {error,code,reason} shape, or nested under "error" in
// Roid's own {error:{code,reason}} shape; securityKeyRequired checks
// both rather than assuming one.
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

func securityKeyRequired(body []byte) bool {
	var flat struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &flat)
	if flat.Reason == securityKeyRequiredReason {
		return true
	}
	var nested struct {
		Error struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &nested)
	return nested.Error.Reason == securityKeyRequiredReason
}

func humanOnlyAPIKeyRefusal(body []byte) bool {
	var flat struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &flat)
	return flat.Error == humanOnlyAPIKeyMessage
}
