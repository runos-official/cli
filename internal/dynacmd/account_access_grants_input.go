package dynacmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/manifest"
)

// grantSubjectInputFields names the account/access/grants manifest
// commands that can carry a human-supplied grant subject value, and
// which body field carries it: add's nested subject.id (a dotted field,
// unflattened into the JSON body only later, inside doRequest -- at
// this point in dispatch the body still holds the flat "subject.id"
// key) and list's free-form subject filter (forwarded into the query
// string). Finding 1431: internal/dynacmd guards credential SECRETS
// only on the RESPONSE side (credential_output.go, guardCredentialResponse);
// nothing guarded the REQUEST side, so a bearer-shaped --subject.id or
// --subject reached Roid unchecked through this family's own POST body
// or GET query string, even though the static cmd package already
// refused the identical value (cmd/access_input.go,
// containsABearerSecretShape).
//
// Driven as a map from manifest command to its one subject-carrying
// field name, so a third subject-carrying command added to this family
// later with no entry here is simply not scanned -- the closure test
// drives its own rows from this same map, so a missing entry fails
// loudly there too, not silently in production.
//
// Scoped to this family only, not every manifest command (finding
// 1097's rationale for guardCredentialResponse applies identically
// here): refuseBearerSecretInGrantSubject returns nil immediately for
// any other command.
var grantSubjectInputFields = map[string]string{
	"account/access/grants/add":  "subject.id",
	"account/access/grants/list": "subject",
}

// refuseBearerSecretInGrantSubject refuses to send a request for this
// family when the one subject-carrying field holds a value matching any
// currently published RunOS/Roid bearer secret shape (runosSecretPattern,
// credential_output.go -- already unanchored by construction, so no
// second definition of the shapes and no token-splitting question
// arises the way cmd/access_input.go's generic kind:id parsing needed).
// Called at the top of dispatch, before buildEndpoint ever runs, so a
// GET's query string is never even constructed with the secret in it,
// and no request of any kind reaches the network. The refusal never
// echoes the value.
//
// MCP's own executor (internal/mcp) has the same two instances and is
// explicitly out of scope here: it does not call this package's
// Execute/dispatch, only the exported QueryParams helper, and MCP
// executor parity is CLI story 347's criterion 5/6, not this story's.
func refuseBearerSecretInGrantSubject(cmdDef manifest.Command, body map[string]any) error {
	field, ok := grantSubjectInputFields[cmdDef.Command]
	if !ok {
		return nil
	}
	v, _ := body[field].(string)
	if v == "" || !runosSecretPattern.MatchString(v) {
		return nil
	}
	return fmt.Errorf("refusing to send request: %s is a bearer-secret-shaped value, which a grant subject never accepts -- use the key's public id instead", field)
}
