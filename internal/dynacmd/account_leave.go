package dynacmd

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/runos-official/cli/internal/config"
)

// accountLeaveCommand ends the signed-in login's membership of the account the request addresses.
const accountLeaveCommand = "account/leave"

/*
applyAccountLeave makes the local config follow a successful `account leave`.

Conductor ends the membership and answers {left, defaultAccount, remainingAccounts}. Before this the
CLI stopped there: the left account stayed active with its stored sign-in, and the next command was
refused. config.LeaveAccount owns the change. This reads the answer, saves, and tells the person on
notice, so stdout stays the response body under --json.

A missing `left` falls back to the account the request addressed, because conductor acted on that one.
*/
func applyAccountLeave(cfg *config.Config, respBody []byte, notice io.Writer) error {
	var response struct {
		Left           string  `json:"left"`
		DefaultAccount *string `json:"defaultAccount"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("you left the account, but the CLI could not read conductor's answer to update its local config: %w", err)
	}
	left := response.Left
	if left == "" {
		left = cfg.GetAccountID()
	}
	defaultAccount := ""
	if response.DefaultAccount != nil {
		defaultAccount = *response.DefaultAccount
	}
	outcome := cfg.LeaveAccount(left, defaultAccount, time.Now().UTC().Format(time.RFC3339))
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("you left account %s, but the CLI could not save its local config: %w. Run 'runos account forget %s --yes' to remove it", left, err, left)
	}
	switch outcome {
	case config.LeaveMovedToDefault:
		fmt.Fprintf(notice, "Left account %s. Active account: %s (your default account, with the same sign-in).\n", left, defaultAccount)
	case config.LeaveSignedOut:
		fmt.Fprintf(notice, "Left account %s. This login has no default account left, so the CLI signed you out. Run 'runos login' to sign in again.\n", left)
	}
	return nil
}
