package dynacmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/runos-official/cli/internal/api"
)

// formatAuthError checks whether err is an *APIError carrying a 401
// auth refusal from conductor and returns a friendly multi-line
// rendering plus true; (_, false) for any other error. I25-H / I25-I:
// pre-fix, every 401 surfaced as a bare `API error (401):
// {"error":"Invalid token"}` with no actionable hint.
//
// Conductor 14.9.0 added a `reason: 'revoked' | 'expired'` field plus
// the matching `revokedAt` / `expiredAt` timestamp when conductor has
// the data AND the bearer parses as a known PAT. The `error: "Invalid
// token"` string stays for backwards compatibility. We render the
// reason + timestamp distinctly so CI logs spell out "revoked at <ts>"
// vs "token doesn't parse" without the user having to dig.
func formatAuthError(err error, usingPAT bool) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusUnauthorized {
		return "", false
	}
	var body struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		Reason    string `json:"reason"`
		RevokedAt string `json:"revokedAt"`
		ExpiredAt string `json:"expiredAt"`
	}
	_ = json.Unmarshal(apiErr.Body, &body)
	msg := body.Error
	if msg == "" {
		msg = "unauthorized"
	}
	/*
	 A session that simply ran out gets ONE line and no checklist.

	 The three hints below are about personal access tokens: is the env var pointing at a current
	 PAT, was it revoked, was it minted on another environment. None of that applies to a browser
	 sign-in that aged out, and printing it sends someone to audit API keys they are not using. The
	 remedy is one command and it is already in the sentence.
	*/
	if body.Code == api.SessionExpiredCode {
		return "your session has expired. Run `runos login` to sign in again.", true
	}
	var sb strings.Builder
	sb.WriteString("authentication refused (401): ")
	sb.WriteString(msg)
	switch body.Reason {
	case "revoked":
		sb.WriteString(" [revoked")
		if body.RevokedAt != "" {
			sb.WriteString(" at ")
			sb.WriteString(body.RevokedAt)
		}
		sb.WriteString("]")
	case "expired":
		sb.WriteString(" [expired")
		if body.ExpiredAt != "" {
			sb.WriteString(" at ")
			sb.WriteString(body.ExpiredAt)
		}
		sb.WriteString("]")
	case "":
		// no structured signal; nothing extra
	default:
		sb.WriteString(" [")
		sb.WriteString(body.Reason)
		sb.WriteString("]")
	}
	sb.WriteString("\n")
	/*
	 THE CHECKLIST HAS TO MATCH THE CREDENTIAL IN USE.

	 These three lines are about personal access tokens, and they were printed for every 401
	 regardless. Measured on a live machine 2026-08-31: an operator on a browser sign-in, pointed at
	 a conductor that would not accept their token, was told to audit `RUNOS_API_KEY` and
	 `runos account api-keys list`. They had no PAT. Every line of advice was for a credential they
	 were not using, and none of them named the thing that would have fixed it.

	 A browser sign-in has exactly one remedy and one common cause, so it gets two lines rather than
	 three borrowed ones.
	*/
	sb.WriteString("Check:\n")
	if usingPAT {
		sb.WriteString("  - is RUNOS_API_KEY pointing at a current PAT? `runos account api-keys list`\n")
		sb.WriteString("  - is RUNOS_API_URL pointing at the same environment the PAT was minted on?\n")
		sb.WriteString("  - was the PAT revoked or rotated? mint a new one via `runos account api-keys add`")
	} else {
		sb.WriteString("  - sign in again: `runos login`\n")
		sb.WriteString("  - are you signed in to the environment you are talking to? `runos status` shows both")
	}
	return sb.String(), true
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
