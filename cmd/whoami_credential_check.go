package cmd

import (
	"fmt"
	"net/http"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
)

// The three truthful, exit-0 states `runos whoami` can report for a
// secret credential (env or stored key), named in --json by
// verified_reason. A fourth state, a 401 refusal, is returned as an
// error instead (exit 1): the server actually judged the credential and
// refused it, which is a different fact from "nothing judged it"
// (story 427, criterion 3; FPL573 Q2 ruling).
const (
	verifiedAccepted            = "accepted"
	verifiedHumanSignInRequired = "human_sign_in_required"
	verifiedUnreachable         = "unreachable"
)

const serverUnreachableNote = "The server was not reached. The credential itself was not judged."

// checkSecretCredential asks GET /user/accounts whether conductor
// accepts the secret credential (RUNOS_API_KEY or a stored key)
// runWhoami resolved for kind, and returns the fields whoamiReport
// should add to its answer. A 401 is returned as an error (the
// credential was refused); every other answer returns fields only,
// because the command answered truthfully even when the server could
// not be reached or the route needed a human sign-in instead of judging
// the credential at all.
//
// This is the one network call `runos whoami` makes for a secret
// credential; earlier, it made none (see the CHANGELOG entry this
// story adds). An unreachable server still answers and exits 0, so
// offline and scripted use keeps working.
func checkSecretCredential(cfg *config.Config, kind auth.CredentialKind) (map[string]any, error) {
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil, err
	}
	result, err := api.NewClient(cfg.GetAPIURL()).Do(http.MethodGet, "/user/accounts", token, nil)
	if err != nil {
		return map[string]any{
			"verified":        false,
			"verified_reason": verifiedUnreachable,
			"note":            serverUnreachableNote,
		}, nil
	}
	switch {
	case result.StatusCode == http.StatusOK:
		// Review finding 1557: a 200 is not proof the body is the
		// accounts envelope (a transparent proxy or a misrouted
		// RUNOS_API_URL can answer 200 with something else entirely).
		// A nil Accounts pointer is "the field is missing", not "no
		// accounts" (the same guard internal/api.UserAccounts applies
		// to this exact endpoint); either way, nothing judged the
		// credential, so this is the unreachable state, not accepted.
		var envelope struct {
			Accounts *[]api.UserAccount `json:"accounts"`
		}
		if err := result.Decode(&envelope); err != nil || envelope.Accounts == nil {
			return map[string]any{
				"verified":        false,
				"verified_reason": verifiedUnreachable,
				"note":            serverUnreachableNote,
			}, nil
		}
		return map[string]any{
			"verified":             true,
			"verified_reason":      verifiedAccepted,
			"accounts":             *envelope.Accounts,
			"credential_type_note": "no endpoint names the type or the public id of the credential in use",
		}, nil
	case result.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%s", dynacmd.FormatAuthRefusal(result.Body, kind))
	case result.StatusCode >= http.StatusInternalServerError:
		return map[string]any{
			"verified":        false,
			"verified_reason": verifiedUnreachable,
			"note":            serverUnreachableNote,
		}, nil
	default:
		return map[string]any{
			"verified":        false,
			"verified_reason": verifiedHumanSignInRequired,
			"note":            "whoami needs a human sign-in for this check. The credential itself was not judged.",
		}, nil
	}
}
