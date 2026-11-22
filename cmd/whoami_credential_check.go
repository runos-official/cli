package cmd

import (
	"encoding/json"
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
//
// Contract C-4 (story 439): an accepted 200 body may also carry an
// optional "credential" field naming the caller's own verified
// credential type and public id, and an optional "accountsOmitted"
// field when conductor deliberately looked no accounts up for a typed
// credential. Both are decoded here, never assumed: a "credential"
// value this CLI cannot safely print leaves credential_type "unknown",
// and "accounts" stays the only account list truth, with
// accounts_omitted/accounts_note saying when it was not looked up.
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
			Accounts        *[]api.UserAccount `json:"accounts"`
			AccountsOmitted string             `json:"accountsOmitted"`
			Credential      json.RawMessage    `json:"credential"`
		}
		if err := result.Decode(&envelope); err != nil || envelope.Accounts == nil {
			return map[string]any{
				"verified":        false,
				"verified_reason": verifiedUnreachable,
				"note":            serverUnreachableNote,
			}, nil
		}
		fields := map[string]any{
			"verified":        true,
			"verified_reason": verifiedAccepted,
			"accounts":        *envelope.Accounts,
		}
		// Contract C-4, ruling R8-5: for a typed credential conductor
		// names only the credential's own verified identity and looks
		// nothing up, so accounts is deliberately empty. accounts_omitted
		// (and the plain sentence in accounts_note) says so, so the empty
		// list never reads as "member of no account".
		if envelope.AccountsOmitted != "" {
			fields["accounts_omitted"] = envelope.AccountsOmitted
			fields["accounts_note"] = "The account list is not shown for this credential type."
		}
		switch credType, credID, state := verifiedCredentialReference(envelope.Credential, token); state {
		case credentialReferenceNamed:
			fields["credential_type"] = credType
			fields["credential_id"] = credID
		case credentialReferenceRefused:
			fields["credential_type_note"] = "the server named this credential in a form that this CLI does not print"
		default: // credentialReferenceAbsent
			fields["credential_type_note"] = "no endpoint names the type or the public id of the credential in use"
		}
		return fields, nil
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
