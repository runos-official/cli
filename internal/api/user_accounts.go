package api

import (
	"fmt"
	"net/http"
)

// UserAccount is one account the signed-in login is a member of.
//
// One login can belong to several accounts, so the account named in a
// sign-in is only the login's DEFAULT landing account. IsDefault marks it.
// AccountRole is the login's role in THIS account, which differs per
// account.
type UserAccount struct {
	AID         string `json:"aid"`
	Name        string `json:"name"`
	CompanyName string `json:"companyName"`
	AccountRole string `json:"accountRole"`
	IsDefault   bool   `json:"isDefault"`
}

// Label is the name a person recognises the account by: the company name,
// then the account name. Empty when the account carries neither, because
// the caller already shows the id.
func (a UserAccount) Label() string {
	if a.CompanyName != "" {
		return a.CompanyName
	}
	return a.Name
}

// AccountsOmittedTypedCredential is the accountsOmitted value conductor
// sends when the caller authenticated with a typed credential (a
// Personal Access Token or an Account API Key): contract C-4, ruling
// R8-5. Conductor names only the credential's own verified identity for
// that caller and looks no accounts up, so UserAccounts' list is empty
// on purpose, not because the credential belongs to no account.
const AccountsOmittedTypedCredential = "typed_credential"

// UserAccounts lists every account the caller's login is a member of, and
// reports whether conductor omitted that lookup instead of actually
// finding none.
//
// GET /user/accounts. A legacy API key is scoped to one account, so a
// legacy key caller gets exactly that account back, with omitted empty.
// A typed credential (contract C-4) gets an empty list with omitted set
// to AccountsOmittedTypedCredential: that is NOT "a member of no
// account", and a caller of this function must not read it that way.
func (c *Client) UserAccounts(token string) (accounts []UserAccount, omitted string, err error) {
	result, err := c.Do(http.MethodGet, "/user/accounts", token, nil)
	if err != nil {
		return nil, "", err
	}
	if !result.OK() {
		if msg := result.ErrorMessage(); msg != "" {
			return nil, "", fmt.Errorf("%s (HTTP %d)", msg, result.StatusCode)
		}
		return nil, "", fmt.Errorf("could not read the account list (HTTP %d)", result.StatusCode)
	}
	var envelope struct {
		Accounts        *[]UserAccount `json:"accounts"`
		AccountsOmitted string         `json:"accountsOmitted"`
	}
	if err := result.Decode(&envelope); err != nil {
		return nil, "", err
	}
	// A 200 without the field is not "no accounts". Reading it as an empty
	// list would turn every membership check into a refusal.
	if envelope.Accounts == nil {
		return nil, "", fmt.Errorf("the account list response has no accounts field (HTTP %d)", result.StatusCode)
	}
	return *envelope.Accounts, envelope.AccountsOmitted, nil
}
