package dynacmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/manifest"
)

// GrantSubjectInputFields names the account/access/grants manifest
// commands that can carry a human-supplied grant subject value, and the
// SET of body fields that carry it: add's nested subject.id and
// subject.kind (dotted fields, unflattened into the JSON body only
// later, inside doRequest -- at this point in dispatch the body still
// holds the flat "subject.id"/"subject.kind" keys) and list's free-form
// subject filter (forwarded into the query string). Finding 1431:
// internal/dynacmd guards credential SECRETS only on the RESPONSE side
// (credential_output.go, GuardCredentialResponse); nothing guarded the
// REQUEST side, so a bearer-shaped --subject.id or --subject reached
// Roid unchecked through this family's own POST body or GET query
// string, even though the static cmd package already refused the
// identical value (cmd/access_input.go, containsABearerSecretShape).
// Story 347 M6: subject.kind joins add's set so the MCP surface, which
// enforces no manifest enum, is scanned there too (the cobra path
// already refuses an off-enum subject.kind via validateEnumValues
// before dispatch, so this is defense in depth there and the real fix
// on the MCP surface).
//
// Driven as a map from manifest command to its set of subject-carrying
// field names, so a third subject-carrying command (or field) added to
// this family later with no entry here is simply not scanned -- the
// closure test drives its own rows from this same map, so a missing
// entry fails loudly there too, not silently in production.
//
// Scoped to this family only, not every manifest command (finding
// 1097's rationale for GuardCredentialResponse applies identically
// here): RefuseBearerSecretInGrantSubject returns nil immediately for
// any other command.
var GrantSubjectInputFields = map[string][]string{
	"account/access/grants/add":  {"subject.id", "subject.kind"},
	"account/access/grants/list": {"subject"},
}

// RefuseBearerSecretInGrantSubject refuses to send a request for this
// family when any of its subject-carrying fields holds a value matching
// any currently published RunOS/Roid bearer secret shape
// (runosSecretPattern, credential_output.go -- already unanchored by
// construction, so no second definition of the shapes and no
// token-splitting question arises the way cmd/access_input.go's generic
// kind:id parsing needed). Called at the top of dispatch (cobra) or
// before the endpoint and body are built (MCP's CommandExecutor.Execute),
// so a GET's query string is never even constructed with the secret in
// it, and no request of any kind reaches the network. The refusal never
// echoes the value.
//
// Exported for internal/mcp (story 347 M6): the MCP executor builds its
// own request body and query string and shares no dispatch with this
// package, so it calls this same entry point directly rather than a
// second copy of the pattern.
func RefuseBearerSecretInGrantSubject(cmdDef manifest.Command, body map[string]any) error {
	fields, ok := GrantSubjectInputFields[cmdDef.Command]
	if !ok {
		return nil
	}
	for _, field := range fields {
		v, _ := body[field].(string)
		if v == "" || !runosSecretPattern.MatchString(v) {
			continue
		}
		return fmt.Errorf("refusing to send request: %s is a bearer-secret-shaped value, which a grant subject never accepts -- use the key's public id instead", field)
	}
	return nil
}
