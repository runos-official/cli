package api

import (
	"fmt"
	"net/http"
	"net/url"
)

// An account switches capabilities on and off as MODULES (FPL31). A
// module governs one whole capability across the Console, the CLI command
// tree, the MCP tool list and the API itself, so a command missing from
// this account's manifest is very often a module that is off rather than
// a command that does not exist.

// AccountModule is one row of the account's module catalogue.
//
// Tier is `base` (on unless the account switched it off) or `premium`
// (off until the account switches it on). Enabled is the effective state
// after the account's own choice, which is the only field a caller
// deciding what to say should read.
type AccountModule struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Tier        string `json:"tier"`
	SortOrder   int    `json:"sortOrder"`
	Enabled     bool   `json:"enabled"`
}

// AccountModules lists the module catalogue for one account, with each
// module's effective enabled state.
//
// GET /:aid/modules. A catalogue row conductor has no code for is absent
// from the answer, so every row here names something the CLI can act on.
func (c *Client) AccountModules(accountID, token string) ([]AccountModule, error) {
	if accountID == "" {
		return nil, fmt.Errorf("account ID not set")
	}
	result, err := c.Do(http.MethodGet, "/"+url.PathEscape(accountID)+"/modules", token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		if msg := result.ErrorMessage(); msg != "" {
			return nil, fmt.Errorf("%s (HTTP %d)", msg, result.StatusCode)
		}
		return nil, fmt.Errorf("could not read the module list (HTTP %d)", result.StatusCode)
	}
	var envelope struct {
		Modules []AccountModule `json:"modules"`
	}
	if err := result.Decode(&envelope); err != nil {
		return nil, err
	}
	return envelope.Modules, nil
}
