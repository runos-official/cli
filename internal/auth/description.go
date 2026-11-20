package auth

// CredentialType names the verified SEMANTIC type of a credential, as
// established by a trusted server response. FCR2030 / Goal 109 draws a
// hard line between this and CredentialKind: CredentialKind says where
// the bearer value came from (env, disk, a sign-in); CredentialType
// says what the credential actually IS. Never set CredentialType from a
// prefix, a storage location, an environment variable name or any other
// local inference -- that is exactly the conflation this type exists to
// stop.
type CredentialType string

const (
	// CredentialTypeUnknown means no trusted server response has
	// established this credential's semantic type yet. This is the
	// zero value, and the only honest answer until a verified lifecycle
	// or inventory response names the type.
	CredentialTypeUnknown CredentialType = "unknown"
	// CredentialTypePersonalPAT is a user-bound Personal Access Token.
	// Matches runos.credential-reference.v1's credentialType
	// "personal_pat".
	CredentialTypePersonalPAT CredentialType = "personal_pat"
	// CredentialTypeAccountAPIKey is an independent machine credential
	// with one custodial account. Matches credentialType
	// "account_api_key".
	CredentialTypeAccountAPIKey CredentialType = "account_api_key"
	// CredentialTypeLegacy is an explicit legacy classification (for
	// example "conductor_personal" or "conductor_account_role"). A
	// legacy credential is never relabelled as personal_pat or
	// account_api_key because of its prefix: it keeps its own verified
	// legacy semantics.
	CredentialTypeLegacy CredentialType = "legacy"
)

// Description is the one shared display model for a credential: WHERE
// it came from (Source, a local, network-free fact) and WHAT it
// verifiably IS (Type and the fields below, populated only from a
// trusted server response). Every surface that talks about a credential
// to a person -- status, whoami, setup, inventory, recovery advice --
// builds one of these rather than inventing its own label, so "is this
// a Personal Access Token or an Account API Key" is answered in exactly
// one place, never guessed from a prefix.
//
// The zero value (Source=CredentialNone, Type=CredentialTypeUnknown) is
// an honest "nothing established yet", never a guess.
type Description struct {
	// Source is the transport path: env var, stored config, roid
	// sign-in, interactive session, or none. Always known locally,
	// without a network call. See CredentialKind.
	Source CredentialKind
	// Type is the verified semantic type. Stays CredentialTypeUnknown
	// until a trusted server response names it; never derived from
	// Source, a prefix, or the storage location.
	Type CredentialType
	// CredentialID is the server's stable public identifier for this
	// credential, never the bearer secret itself. Empty until a trusted
	// response supplies it.
	CredentialID string
	// LegacyClassification is the server's explicit legacy kind (for
	// example "conductor_personal") when Type is CredentialTypeLegacy.
	// Empty otherwise.
	LegacyClassification string
	// PermissionSource names where authority actually comes from, in
	// the server's own words (for example "current_user_authority" for
	// a Personal Access Token that follows the user's current role, or
	// "stored_account_role" for a legacy account-scoped key). Empty
	// until a trusted response supplies it.
	PermissionSource string
}

// DescribeSource builds the honest default Description for a source
// with no verified type yet: what ResolveToken will send, and nothing
// claimed about what it is. A caller that later obtains a trusted
// lifecycle or inventory response should build its own Description from
// that response's verified fields instead of guessing from this one.
func DescribeSource(source CredentialKind) Description {
	return Description{Source: source, Type: CredentialTypeUnknown}
}

// IsSecretCredential reports whether this description's source is a
// bearer secret (an env or stored key) rather than an interactively
// authenticated human, regardless of verified Type: a Personal Access
// Token and an Account API Key are both "evidence of possession, never
// of a person being present". See CredentialKind.IsSecretCredential.
func (d Description) IsSecretCredential() bool {
	return d.Source.IsSecretCredential()
}

// Label renders the one phrase every surface should use for this
// credential. Generic ("API credential") until a trusted response
// establishes Type; specific only then. Never says "Personal Access
// Token" or "Account API Key" for a credential whose type is not
// actually verified -- that false specificity is the defect this story
// exists to remove.
func (d Description) Label() string {
	switch d.Type {
	case CredentialTypePersonalPAT:
		return "Personal Access Token"
	case CredentialTypeAccountAPIKey:
		return "Account API Key"
	case CredentialTypeLegacy:
		if d.LegacyClassification != "" {
			return "legacy credential (" + d.LegacyClassification + ")"
		}
		return "legacy credential"
	default:
		switch d.Source {
		case CredentialEnvKey, CredentialStoredKey:
			return "API credential"
		case CredentialRoid, CredentialInteractive:
			return "signed-in session"
		default:
			return "credential"
		}
	}
}
