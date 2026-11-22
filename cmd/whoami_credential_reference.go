package cmd

import (
	"encoding/json"
	"regexp"
	"strings"
)

// credentialReferenceState names what verifiedCredentialReference found in
// a server-reported "credential" field (contract C-4, GET /user/accounts).
type credentialReferenceState int

const (
	// credentialReferenceAbsent means the field was missing or JSON null:
	// the server did not name the credential at all.
	credentialReferenceAbsent credentialReferenceState = iota
	// credentialReferenceNamed means credentialType and credentialId both
	// passed every check and are safe to print verbatim.
	credentialReferenceNamed
	// credentialReferenceRefused means the server sent a "credential"
	// value this CLI will not print: the wrong shape, or an id that looks
	// like, contains, or is contained in a bearer secret.
	credentialReferenceRefused
)

// credentialTypeShapePattern is a shape check only, not a list of known
// credential types (lesson dont-hardcode-lists-the-server-owns): the type
// list belongs to the server, so a new value prints verbatim without a CLI
// release.
var credentialTypeShapePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// verifiedCredentialReference decodes the optional "credential" field of
// GET /user/accounts' 200 body (contract C-4: { credentialType,
// credentialId }, present only for a key-authenticated caller) and decides
// whether runos whoami may print it.
//
// sentToken is the bearer this CLI sent on the request that produced raw. A
// RunOS typed bearer deliberately carries its own public id in the clear,
// before the final "." (runos_pat_<uuid>.<secret>, runos_key_<uuid>.<secret>;
// internal/dynacmd/credential_output.go): finding 1591 found that refusing
// on any substring relation between the whole bearer and the reported id
// refused that honest id in every typed-credential case, since the id IS a
// substring of the bearer by construction. The guard below checks only the
// SECRET half, the part after the final "." (or the whole token when it has
// no "."): a reported id is refused when it equals, contains, or is
// contained in that secret half, on top of the published PublicId grammar
// and bearer-secret shape check validatePublicCredentialID already enforces
// for `access explain --credential` (cmd/access_explain.go). A type without
// a usable id, or an id without a usable type, is never printed (all or
// nothing).
func verifiedCredentialReference(raw json.RawMessage, sentToken string) (credType, credID string, state credentialReferenceState) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", "", credentialReferenceAbsent
	}
	var ref struct {
		CredentialType string `json:"credentialType"`
		CredentialID   string `json:"credentialId"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return "", "", credentialReferenceRefused
	}
	if ref.CredentialType == "" || ref.CredentialID == "" {
		return "", "", credentialReferenceRefused
	}
	if !credentialTypeShapePattern.MatchString(ref.CredentialType) {
		return "", "", credentialReferenceRefused
	}
	if err := validatePublicCredentialID(ref.CredentialID); err != nil {
		return "", "", credentialReferenceRefused
	}
	if secretHalf := bearerSecretHalf(sentToken); secretHalf != "" &&
		(ref.CredentialID == secretHalf ||
			strings.Contains(ref.CredentialID, secretHalf) ||
			strings.Contains(secretHalf, ref.CredentialID)) {
		return "", "", credentialReferenceRefused
	}
	return ref.CredentialType, ref.CredentialID, credentialReferenceNamed
}

// bearerSecretHalf returns the part of a bearer that is actually secret: for
// the typed/legacy "<prefix>.<secret>" shape, everything after the FINAL
// ".", since the public id sits before it in the clear; for a bearer with
// no ".", the whole value (nothing is known to be safe to disclose).
func bearerSecretHalf(sentToken string) string {
	if idx := strings.LastIndex(sentToken, "."); idx >= 0 {
		return sentToken[idx+1:]
	}
	return sentToken
}
