package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"unicode"
)

/*
What ended a call to Google, as a fact the caller can branch on (FCR160).

The remedy for a failed sign-in depends entirely on WHAT failed, and the three cases have nothing
in common:

  - ErrNetworkUnreachable. Nothing judged the credential. The remedy is the network and the
    sign-in is untouched. Usually over by the next poll.
  - ErrCredentialRefused. Google judged the credential and will not take it. The remedy is a
    browser.
  - ErrClientMisconfigured. Google will not take this MACHINE's Firebase settings, whoever is
    signed in. Signing in again cannot fix it, so offering that is a loop with no exit.

This package is the only layer that sees the response, so it is the only layer that can tell them
apart. It used to return a sentence and leave `cmd` to guess from the text, which worked for a dead
link (a `*url.Error`) and failed for everything that ANSWERS. See failure_kind_test.go for the
measurements this is built on.
*/
var (
	ErrNetworkUnreachable  = errors.New("could not reach the sign-in service")
	ErrCredentialRefused   = errors.New("the sign-in was refused")
	ErrClientMisconfigured = errors.New("this machine's sign-in configuration was refused")
)

/*
ErrInterceptedReply marks the sub-case where something answered and it was not Google: a wifi
portal, a proxy, a filtering appliance.

Wrapped ALONGSIDE ErrNetworkUnreachable, never instead of it, so a caller that only cares about the
remedy class keeps working and one that wants to name the cause can. The advice differs: "check
your connection" is no use to somebody sitting on a hotel network that wants them to accept its
terms first.
*/
var ErrInterceptedReply = errors.New("something other than the sign-in service answered")

/*
googleErrorEnvelope is the body Google puts on every refusal, MEASURED 2026-08-31 against the live
endpoints:

	{"error":{"code":400,"message":"INVALID_REFRESH_TOKEN","status":"INVALID_ARGUMENT"}}

Its presence is the evidence that Google itself answered. A status code is not: a portal can send
any code it likes, and Google sends 400 both for a refused credential and for a bad API key.
*/
type googleErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

// googleFailure decides which of the three happened, from the reply itself. `what` names the call
// for the reader ("the token refresh"), because the error is read in a menu bar with no other
// context around it.
func googleFailure(what string, resp *http.Response, body []byte) error {
	var envelope googleErrorEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Error.Message == "" {
		return interceptedFailure(what, resp, body)
	}
	if isAPIKeyFault(envelope) {
		return fmt.Errorf("%w: Google refused this machine's Firebase API key on %s (%s)",
			ErrClientMisconfigured, what, oneLine(envelope.Error.Message, 120))
	}
	// Google answered with a verdict. Only the client-error codes ARE a verdict: a 5xx is Google
	// being unwell and says nothing about the credential, and treating it as a sign-out would send
	// somebody to a browser to fix an outage they cannot reach.
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s: %s", ErrCredentialRefused, what, oneLine(envelope.Error.Message, 120))
	default:
		return fmt.Errorf("%w: %s returned HTTP %d: %s",
			ErrNetworkUnreachable, what, resp.StatusCode, oneLine(envelope.Error.Message, 120))
	}
}

/*
isAPIKeyFault reports whether Google rejected the KEY rather than the credential.

Google's machine-readable reason is in `details[].reason` and the whole family starts API_KEY_
(API_KEY_INVALID, API_KEY_SERVICE_BLOCKED, API_KEY_HTTP_REFERRER_BLOCKED, API_KEY_IP_ADDRESS_
BLOCKED). The message prefix is the fallback for the endpoints that answer without details, and it
is a fallback rather than the rule because a message is prose and gets reworded.
*/
func isAPIKeyFault(envelope googleErrorEnvelope) bool {
	for _, detail := range envelope.Error.Details {
		if strings.HasPrefix(detail.Reason, "API_KEY") {
			return true
		}
	}
	return strings.HasPrefix(envelope.Error.Message, "API key not valid")
}

/*
interceptedFailure describes a reply that did not come from Google.

It DESCRIBES rather than reproduces. A portal's body is a web page and can be a hundred kilobytes
of it; this text ends up in `runos status --json`, which gets pasted into chat windows, and in a
menu bar, which has one line. What a reader needs is the status, the media type and enough of the
opening to recognise an interception.
*/
func interceptedFailure(what string, resp *http.Response, body []byte) error {
	description := oneLine(string(body), 80)
	if description == "" {
		description = "an empty reply"
	} else {
		description = `starting "` + description + `"`
	}
	return fmt.Errorf("%w: %w: %s got HTTP %d and %s, %s",
		ErrNetworkUnreachable, ErrInterceptedReply, what, resp.StatusCode, mediaType(resp), description)
}

// mediaType is the reply's declared type with its parameters dropped, or a stand-in when it did
// not declare one. Google always answers application/json, so anything else is itself the finding.
func mediaType(resp *http.Response) string {
	raw := resp.Header.Get("Content-Type")
	if raw == "" {
		return "no declared content type"
	}
	parsed, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "an unreadable content type"
	}
	return parsed
}

// oneLine collapses a reply to a single short run of printable text. Newlines and control
// characters go because the caller renders this on one line; the length cap goes on because the
// caller may be a menu bar.
func oneLine(text string, limit int) string {
	var b strings.Builder
	space := true // leading whitespace is dropped rather than turned into a space
	for _, r := range text {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			if !space && b.Len() < limit {
				b.WriteRune(' ')
				space = true
			}
			continue
		}
		if b.Len() >= limit {
			return strings.TrimSpace(b.String()) + "…"
		}
		b.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(b.String())
}

// transportFailure is the case where nothing answered at all: a dead link, DNS, a refused
// connection, a timeout. It reached no server, so there is nothing to describe but the cause.
func transportFailure(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrNetworkUnreachable, what, err)
}
