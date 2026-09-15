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

// UserAccounts lists every account the caller's login is a member of.
//
// GET /user/accounts. An API key is scoped to one account, so an API key
// caller gets exactly that account back.
func (c *Client) UserAccounts(token string) ([]UserAccount, error) {
	result, err := c.Do(http.MethodGet, "/user/accounts", token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		if msg := result.ErrorMessage(); msg != "" {
			return nil, fmt.Errorf("%s (HTTP %d)", msg, result.StatusCode)
		}
		return nil, fmt.Errorf("could not read the account list (HTTP %d)", result.StatusCode)
	}
	var envelope struct {
		Accounts *[]UserAccount `json:"accounts"`
	}
	if err := result.Decode(&envelope); err != nil {
		return nil, err
	}
	// A 200 without the field is not "no accounts". Reading it as an empty
	// list would turn every membership check into a refusal.
	if envelope.Accounts == nil {
		return nil, fmt.Errorf("the account list response has no accounts field (HTTP %d)", result.StatusCode)
	}
	return *envelope.Accounts, nil
}
