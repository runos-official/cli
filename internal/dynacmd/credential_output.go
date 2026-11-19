package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// runosSecretPattern matches a RunOS bearer secret of any currently
// supported shape: runos_pat_<id>.<secret> (a Personal Access Token,
// and every legacy account-scoped key minted through the existing
// /account/api-keys routes) and runos_key_<id>.<secret> (a newly
// issued Account API Key). Mirrors the credential shape
// scripts/leakcheck.py and scripts/release.sh enforce on repository
// text; this copy runs against a LIVE RESPONSE BODY instead.
var runosSecretPattern = regexp.MustCompile(`\brunos_(?:pat|key)_[A-Za-z0-9]{6,}\.[A-Za-z0-9]{20,}\b`)

// oneShotSecretFields are the only field names a response may
// legitimately carry a bearer secret under: the one-time issuance
// envelope for `account/api-keys/add`-style commands (`token`) and for
// `account/notify-keys/add` (`keyValue`). FPL435's design keeps "the
// one-shot bearer visible only in the successful issuance response",
// so every future typed personal-access-tokens/account-api-keys
// creation and rotation command is expected to reuse one of these two
// names; until this file is extended to recognise a new name, a
// response carrying its secret elsewhere is refused, which is the
// correct fail-closed default.
var oneShotSecretFields = map[string]bool{"token": true, "keyValue": true}

// guardCredentialResponse refuses to let a secret-shaped bearer value
// reach the terminal or a --json consumer from anywhere other than the
// one-shot issuance field on a single JSON object.
//
// It is deliberately NOT keyed on the command name. Story 342 (FPL434)
// has not yet settled the public command spellings for the new typed
// personal-access-tokens/account-api-keys commands, and keying this
// guard on today's provisional names would either miss the eventual
// commands silently or need to be re-landed the moment they are
// renamed. Keying on the response's own shape instead makes the guard
// correct for every current and future credential command without
// modification: a bearer secret may appear ONLY as a scalar `token` or
// `keyValue` field on a single JSON object, never inside an array
// element (an inventory listing, by construction, is never a single-
// credential issuance) and never under any other field name (a secret
// repeated under a second key, or nested inside an object, is exactly
// the case FPL435 means by "a response that contains a bearer-secret
// field which the inventory contract forbids").
//
// Returns nil (nothing to guard) for a body that is not a JSON object
// at all: a non-object top level is never a one-shot issuance envelope,
// so it is scanned with no exemption.
func guardCredentialResponse(respBody []byte) error {
	trimmed := bytes.TrimSpace(respBody)
	if len(trimmed) == 0 {
		return nil
	}
	var probe any
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil // not JSON; nothing this guard can check
	}
	obj, isObject := probe.(map[string]any)
	if !isObject {
		if found := findSecretValue(probe, "response"); found != "" {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in %s, which the inventory contract forbids", found)
		}
		return nil
	}
	for key, value := range obj {
		if oneShotSecretFields[key] {
			if s, ok := value.(string); ok && runosSecretPattern.MatchString(s) {
				continue // the one legitimate carrier of a one-shot secret
			}
		}
		if found := findSecretValue(value, key); found != "" {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in field %q, which the inventory contract forbids", found)
		}
	}
	return nil
}

// findSecretValue walks v looking for a string matching
// runosSecretPattern. path names the current field for the error
// message.
func findSecretValue(v any, path string) string {
	switch t := v.(type) {
	case string:
		if runosSecretPattern.MatchString(t) {
			return path
		}
	case map[string]any:
		for k, nested := range t {
			if found := findSecretValue(nested, path+"."+k); found != "" {
				return found
			}
		}
	case []any:
		for i, nested := range t {
			if found := findSecretValue(nested, fmt.Sprintf("%s[%d]", path, i)); found != "" {
				return found
			}
		}
	}
	return ""
}

// printApiKeyTokenBanner prints an ASCII-framed reminder under the
// formatter's table output for `account api-keys add`. Surfaces the
// one-shot token + warning so terminal users don't miss them in a busy
// session. Silently no-ops when the response body lacks a token (e.g.
// when the conductor returned a partial response shape) to avoid
// blowing up the call site. Regression target: I11-F.
func printApiKeyTokenBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	token, _ := item["token"].(string)
	if token == "" {
		return
	}
	warning, _ := item["warning"].(string)
	if warning == "" {
		warning = "This token will not be shown again. Store it in a secret manager."
	}
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  ONE-SHOT TOKEN - save it now, it cannot be retrieved")
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+token)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+warning)
	fmt.Fprintln(w, bar)
}

// printNotifyKeyBanner prints the same banner shape as
// printApiKeyTokenBanner for `account notify-keys add`. Notify keys are
// the symmetric notification-webhook credential and have the same
// store-the-hash, return-the-secret-once contract. Silently no-ops
// when the response body lacks `keyValue` so a partial conductor shape
// can't blow up the call site. Regression target: I12-H (mirror of
// I11-F for PATs).
func printNotifyKeyBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	keyValue, _ := item["keyValue"].(string)
	if keyValue == "" {
		return
	}
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  ONE-SHOT NOTIFY KEY - save it now, it cannot be retrieved")
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+keyValue)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  This key will not be shown again. Store it in a secret manager.")
	fmt.Fprintln(w, bar)
}
