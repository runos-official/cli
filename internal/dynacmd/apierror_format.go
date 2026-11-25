package dynacmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
)

// formatAuthError checks whether err is an *APIError carrying a 401
// auth refusal from conductor and returns a friendly multi-line
// rendering plus true; (_, false) for any other error. I25-H / I25-I:
// pre-fix, every 401 surfaced as a bare `API error (401):
// {"error":"Invalid token"}` with no actionable hint.
//
// Conductor 14.9.0 added a `reason: 'revoked' | 'expired'` field plus
// the matching `revokedAt` / `expiredAt` timestamp when conductor has
// the data AND the bearer parses as a known secret credential. The
// `error: "Invalid token"` string stays for backwards compatibility. We
// render the reason + timestamp distinctly so CI logs spell out
// "revoked at <ts>" vs "token doesn't parse" without the user having to
// dig.
//
// credential is the SOURCE question only (auth.Kind(cfg)), never a
// semantic-type claim: conductor's 401 body carries no verified
// credentialType today (FPL421/story 331 adds that classification only
// to the api-keys list/get responses, not to the auth-refusal path), so
// this function must not call the secret a Personal Access Token. Story
// 343 / FCR2030: a prior version printed a PAT-specific checklist for
// every secret credential, including an Account API Key, which is the
// exact defect this signature exists to prevent. Recovery stays generic
// until a trusted response names a type.
//
// Review finding 1107: the source still has to be told apart between
// CredentialEnvKey and CredentialStoredKey, not just collapsed into "a
// secret credential", because RUNOS_API_KEY OVERRIDES any stored
// credential (auth.ResolveToken's priority order). Telling an env-key
// holder to "mint a new one" without saying the env var must be updated
// too sends them to retry with the exact same bearer. The checklist also
// no longer names `account api-keys add/list` as THE fix: those are the
// legacy Conductor commands only, and pointing a typed-key holder at
// them would mint an unrelated legacy credential that does nothing for
// the one that is actually broken.
func formatAuthError(err error, credential auth.CredentialKind) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusUnauthorized {
		return "", false
	}
	return FormatAuthRefusal(apiErr.Body, credential), true
}

// FormatAuthRefusal renders a 401 auth-refusal body as the CLI's
// standing guidance for the credential kind that sent the request.
// Exported so `runos whoami`'s secret-credential check
// (cmd/whoami_credential_check.go) renders the identical guidance the
// dynamic executor does for every other command, rather than a second,
// driftable copy of this text (story 427, criterion 3).
func FormatAuthRefusal(body []byte, credential auth.CredentialKind) string {
	var parsed struct {
		// Error is raw because conductor's own 401 shape carries a
		// string here, but a 401 the seat saw on panel 61 (sender
		// unknown; neither of conductor's two shapes) carried a body
		// with no string "error" at all. A nested {code,reason} object,
		// or an empty body, must decode without tripping a type error
		// that would also blank out the sibling Reason/Code fields
		// below (Planner ruling, FPL573 Q2/2.3).
		Error     json.RawMessage `json:"error"`
		Code      string          `json:"code"`
		Reason    string          `json:"reason"`
		RevokedAt string          `json:"revokedAt"`
		ExpiredAt string          `json:"expiredAt"`
	}
	_ = json.Unmarshal(body, &parsed)

	/*
	 A session that simply ran out gets ONE line and no checklist.

	 The three hints below are about personal access tokens: is the env var pointing at a current
	 PAT, was it revoked, was it minted on another environment. None of that applies to a browser
	 sign-in that aged out, and printing it sends someone to audit API keys they are not using. The
	 remedy is one command and it is already in the sentence.
	*/
	if parsed.Code == api.SessionExpiredCode {
		return "your session has expired. Run `runos login` to sign in again."
	}

	// A string "error" is conductor's own shape; anything else (a
	// nested object, or none at all) carries no message this CLI
	// trusts to be a server-written sentence, so fall back to a nested
	// "code" when the body names one, and otherwise the generic word.
	// Either way the body did not hand us a STRING message, so the
	// revocation guess below must not be asserted as one either.
	var msg string
	var hasStringError bool
	if len(parsed.Error) > 0 {
		var s string
		if json.Unmarshal(parsed.Error, &s) == nil {
			msg, hasStringError = s, true
		}
	}
	if !hasStringError {
		var nested struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(parsed.Error, &nested) == nil && nested.Code != "" {
			msg = nested.Code
		}
	}
	if msg == "" {
		msg = "unauthorized"
	}

	var sb strings.Builder
	sb.WriteString("authentication refused (401): ")
	sb.WriteString(msg)
	switch parsed.Reason {
	case "revoked":
		sb.WriteString(" [revoked")
		if parsed.RevokedAt != "" {
			sb.WriteString(" at ")
			sb.WriteString(parsed.RevokedAt)
		}
		sb.WriteString("]")
	case "expired":
		sb.WriteString(" [expired")
		if parsed.ExpiredAt != "" {
			sb.WriteString(" at ")
			sb.WriteString(parsed.ExpiredAt)
		}
		sb.WriteString("]")
	case "":
		// no structured signal; nothing extra
	default:
		sb.WriteString(" [")
		sb.WriteString(parsed.Reason)
		sb.WriteString("]")
	}
	sb.WriteString("\n")
	/*
	 THE CHECKLIST HAS TO MATCH THE CREDENTIAL IN USE, MUST NOT NAME A TYPE THAT WAS NEVER
	 VERIFIED, AND MUST NOT SEND A RETRY BACK TO THE SAME BEARER.

	 These lines are about a secret credential (a Personal Access Token OR an Account API Key;
	 conductor's 401 body does not say which), and they were printed for every 401 regardless.
	 Measured on a live machine 2026-08-31: an operator on a browser sign-in, pointed at a conductor
	 that would not accept their token, was told to audit `RUNOS_API_KEY` and `runos account
	 api-keys list`. They had no secret credential. Every line of advice was for a credential they
	 were not using, and none of them named the thing that would have fixed it.

	 Story 343 / FCR2030 fixes a second instance of the same defect: the checklist used to call the
	 secret a "PAT" unconditionally, which is itself wrong advice for an Account API Key (a machine
	 credential that never signs in with a browser and has no "PAT" to mint), and it named
	 `account api-keys add`/`list` -- the LEGACY Conductor commands only -- as THE fix, which mints
	 an unrelated legacy credential and does nothing for a typed key that is actually broken.
	 Nothing on this path has a trusted credentialType, so the wording stays type-neutral; it must
	 not guess from a prefix.

	 Review finding 1107, a THIRD defect in the same checklist: RUNOS_API_KEY overrides any stored
	 credential (auth.ResolveToken's priority order), so telling an env-key holder only to "mint a
	 new one" sends their very next command to retry with the exact same stale bearer, because the
	 env var is still set. The env-key and stored-key branches now say so explicitly and name the
	 different remedy each source actually needs.

	 A browser sign-in has exactly one remedy and one common cause, so it gets two lines rather than
	 three borrowed ones.

	 Story 427, panel 61 finding 4: the third bullet below used to ASSERT "was it revoked, rotated
	 or expired?" for every 401 regardless of what the server said. A credential that works on every
	 other command and gets refused here only because the ROUTE needs a human sign-in is not
	 revoked, rotated or expired, and the server never said it was; conductor has no stable code for
	 "this route is human-only" to branch on instead (FCR 2720), so the bullet below now only
	 ASSERTS revocation when `reason` said so, and otherwise names the human-sign-in possibility
	 without claiming it.
	*/
	sb.WriteString("Check:\n")
	switch credential {
	case auth.CredentialEnvKey:
		sb.WriteString("  - RUNOS_API_KEY overrides every other credential: a replacement must be set there, or the next command retries this exact bearer\n")
		sb.WriteString("  - is RUNOS_API_URL pointing at the same environment this credential was issued on?\n")
		sb.WriteString(revocationCheckLine(parsed.Reason))
	case auth.CredentialStoredKey:
		sb.WriteString("  - is the credential stored by `runos login --api-key` still current? `runos logout` clears it, or `runos login --api-key <new> --account-id <id>` replaces it\n")
		sb.WriteString("  - is RUNOS_API_URL pointing at the same environment this credential was issued on?\n")
		sb.WriteString(revocationCheckLine(parsed.Reason))
	default:
		sb.WriteString("  - sign in again: `runos login`\n")
		sb.WriteString("  - are you signed in to the environment you are talking to? `runos status` shows both")
	}
	return sb.String()
}

// revocationCheckLine is the final checklist bullet for a secret
// credential's 401. It asserts revocation, rotation or expiry only when
// the server's body actually said so (reason "revoked" or "expired");
// any other reason, including none at all, gets a line that names a
// human-sign-in route as a live possibility instead of asserting a
// cause the server never gave.
func revocationCheckLine(reason string) string {
	const remedy = "If it is a legacy key, `runos account api-keys list` shows its state; otherwise obtain a replacement the way this one was issued"
	if reason == "revoked" || reason == "expired" {
		return "  - was it revoked, rotated or expired? " + remedy
	}
	return "  - the server did not say why. If this credential works on other commands, this command may need a human sign-in (`runos login`); the credential itself was not judged. Otherwise check whether it was revoked, rotated or expired: " + remedy
}

// formatDependentsError checks whether err is an *APIError carrying a
// 409 with a structured dependents body (the shape conductor's services
// delete handler returns when other apps/services reference the target).
// Returns a friendly multi-line rendering plus true; (_, false) for any
// other error so callers can fall back to the default formatting.
//
// This is a generic helper; nothing about it is services-specific. Any
// future endpoint that surfaces a 409+dependents body gets the same
// treatment automatically.
func formatDependentsError(err error) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusConflict {
		return "", false
	}
	var body struct {
		Error      string `json:"error"`
		Dependents []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Alias string `json:"alias"`
		} `json:"dependents"`
	}
	if err := json.Unmarshal(apiErr.Body, &body); err != nil {
		return "", false
	}
	if len(body.Dependents) == 0 {
		return "", false
	}
	var sb strings.Builder
	if body.Error != "" {
		sb.WriteString("refused: ")
		sb.WriteString(body.Error)
		sb.WriteString("\n")
	} else {
		sb.WriteString("refused: this resource has dependents\n")
	}
	sb.WriteString("dependents:\n")
	for _, d := range body.Dependents {
		switch {
		case d.Alias != "" && d.Name != "":
			sb.WriteString(fmt.Sprintf("  - %s %s (%s), alias %q\n", d.Type, d.Name, d.ID, d.Alias))
		case d.Alias != "":
			sb.WriteString(fmt.Sprintf("  - %s (%s), alias %q\n", d.Type, d.ID, d.Alias))
		case d.Name != "":
			sb.WriteString(fmt.Sprintf("  - %s %s (%s)\n", d.Type, d.Name, d.ID))
		default:
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", d.Type, d.ID))
		}
	}
	sb.WriteString("Reconcile each dependent (e.g. update its requires: to point elsewhere, or delete it first) and re-run.")
	return sb.String(), true
}

// ModuleNotEnabledCode is conductor's machine-readable reason for a route
// whose module this account has switched off (FPL31 D3). A caller
// branches on this rather than on the sentence, which is written for a
// person and is expected to be reworded.
const ModuleNotEnabledCode = "module.not_enabled"

// formatModuleNotEnabledError renders a 403 from a module this account
// has switched off as one line that names the module and the command
// that switches it on.
//
// WHY IT NEEDS ITS OWN SENTENCE. Conductor's message is accurate and
// still leaves the reader stuck: a 403 reads as "you are not allowed",
// which sends an operator to audit roles and an agent to give up and
// reach for the raw API. The real state is that the capability is
// switched off for this account and one command switches it back on.
// That command is what has to be in the line.
//
// Fires only on a body carrying BOTH the code and a non-empty module, so
// a 403 this rule does not understand keeps describeAPIError's rendering
// rather than a sentence naming an empty module.
func formatModuleNotEnabledError(err error) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusForbidden {
		return "", false
	}
	var body struct {
		Code   string `json:"code"`
		Module string `json:"module"`
	}
	if json.Unmarshal(apiErr.Body, &body) != nil {
		return "", false
	}
	if body.Code != ModuleNotEnabledCode || body.Module == "" {
		return "", false
	}
	return fmt.Sprintf(
		"the %s module is not enabled for this account. Run `runos account modules enable %s` to switch it on. (HTTP %d, %s)",
		body.Module, body.Module, apiErr.StatusCode, body.Code), true
}
