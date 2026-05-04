package config

import (
	"testing"
)

func TestGetAccountID_EnvVarTakesPrecedence(t *testing.T) {
	t.Setenv("RUNOS_ACCOUNT_ID", "acct-from-env")
	c := &Config{AccountID: "acct-from-file"}
	if got := c.GetAccountID(); got != "acct-from-env" {
		t.Errorf("expected env value to win, got %q", got)
	}
}

func TestGetAccountID_FallsBackToConfigField(t *testing.T) {
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	c := &Config{AccountID: "acct-from-file"}
	if got := c.GetAccountID(); got != "acct-from-file" {
		t.Errorf("expected file value, got %q", got)
	}
}

func TestGetAccountID_NilReceiver(t *testing.T) {
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	var c *Config
	if got := c.GetAccountID(); got != "" {
		t.Errorf("nil receiver should return empty, got %q", got)
	}
	t.Setenv("RUNOS_ACCOUNT_ID", "from-env")
	if got := c.GetAccountID(); got != "from-env" {
		t.Errorf("nil receiver should still pick up env, got %q", got)
	}
}
