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
// sentToken is the bearer this CLI sent on the request that produced raw.
// A reported id is refused when it equals, contains, or is contained in
// that secret, on top of the published PublicId grammar and bearer-secret
// shape check validatePublicCredentialID already enforces for `access
// explain --credential` (cmd/access_explain.go): a type without a usable
// id, or an id without a usable type, is never printed (all or nothing).
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
	if sentToken != "" && (ref.CredentialID == sentToken ||
		strings.Contains(sentToken, ref.CredentialID) ||
		strings.Contains(ref.CredentialID, sentToken)) {
		return "", "", credentialReferenceRefused
	}
	return ref.CredentialType, ref.CredentialID, credentialReferenceNamed
}
