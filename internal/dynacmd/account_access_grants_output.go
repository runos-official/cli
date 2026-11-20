package dynacmd

// Finding 1194 (CLI story 344 criterion 5): "account/access/grants"
// mints no bearer of its own (add and show return a grant, never a
// credential), so the zero-value credentialFamily registered below is
// correct -- no secretField, no secretPattern, no issuanceVerbs, which
// means GuardCredentialResponse's one-shot exemption never applies and
// printOneShotBanner never fires for this family. Registering it anyway
// is what makes InjectETagIntoCredentialResponse stop excluding show
// and add's response ETag (Conductor's accountAccessGrants.manifest.ts:
// the ETag response header, never a body field, is the documented
// source of withdraw's ifMatch), and it puts every grant response under
// the same trailing-data/duplicate-key/secret-shaped-value scan as every
// other registered family, which is strictly defensive here since a
// grant response never legitimately carries a credential secret.
//
// Added through init() in a file of its own, rather than editing
// credentialCommandFamilies' literal in credential_output.go directly:
// that file already sits at 727 lines, over this repo's 700-line hard
// cap (cli-go-file-budget-and-one-concern-per-file), and the AI-first
// rule is to split a file before extending it, never grow an
// already-over-budget file further. This keeps that file's line count
// untouched.
func init() {
	credentialCommandFamilies["account/access/grants"] = credentialFamily{}
}
