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

// TestGetEnv is the goal-21 B3 regression. `runos config get` reported
// the stored `env` label whatever the URLs said, so a config pointed at
// dev by `config set api-url` still answered "beta". The label is only
// true while the URLs are the ones `config env <name>` wrote.
func TestGetEnv(t *testing.T) {
	t.Run("stored env with untouched URLs", func(t *testing.T) {
		t.Setenv("RUNOS_API_URL", "")
		c := &Config{Env: "beta", ConductorURL: "https://api.beta.example"}
		if got := c.GetEnv(); got != "beta" {
			t.Errorf("GetEnv() = %q, want beta", got)
		}
	})
	t.Run("an api-url override reports custom", func(t *testing.T) {
		t.Setenv("RUNOS_API_URL", "http://localhost:3025")
		c := &Config{Env: "beta", ConductorURL: "https://api.beta.example"}
		if got := c.GetEnv(); got != EnvCustom {
			t.Errorf("GetEnv() = %q, want %q", got, EnvCustom)
		}
	})
	t.Run("an override equal to the stored URL is not custom", func(t *testing.T) {
		t.Setenv("RUNOS_API_URL", "https://api.beta.example")
		c := &Config{Env: "beta", ConductorURL: "https://api.beta.example"}
		if got := c.GetEnv(); got != "beta" {
			t.Errorf("GetEnv() = %q, want beta", got)
		}
	})
	t.Run("no stored env at all", func(t *testing.T) {
		t.Setenv("RUNOS_API_URL", "")
		c := &Config{ConductorURL: "https://api.beta.example"}
		if got := c.GetEnv(); got != EnvCustom {
			t.Errorf("GetEnv() = %q, want %q", got, EnvCustom)
		}
	})
	t.Run("nil receiver", func(t *testing.T) {
		t.Setenv("RUNOS_API_URL", "")
		var c *Config
		if got := c.GetEnv(); got != "" {
			t.Errorf("GetEnv() on nil = %q, want empty", got)
		}
	})
}

// SetAPIURL is how `config set api-url` writes the URL: the stored env
// label stops being true the moment the URL diverges from what
// `config env <name>` wrote, so setting one clears the other.
func TestSetAPIURL(t *testing.T) {
	c := &Config{Env: "beta", ConductorURL: "https://api.beta.example"}
	c.SetAPIURL("http://localhost:3025")
	if c.Env != EnvCustom {
		t.Errorf("Env = %q after diverging from the env's URL, want %q", c.Env, EnvCustom)
	}
	if c.ConductorURL != "http://localhost:3025" {
		t.Errorf("ConductorURL = %q", c.ConductorURL)
	}

	// Re-setting exactly what the environment wrote keeps the label: the
	// URL is still that environment's.
	same := &Config{Env: "beta", ConductorURL: "https://api.beta.example", EnvAPIURL: "https://api.beta.example"}
	same.SetAPIURL("https://api.beta.example")
	if same.Env != "beta" {
		t.Errorf("re-setting the environment's own URL keeps the label, got %q", same.Env)
	}

	// A config with no recorded environment URL cannot support the label,
	// so any explicit set makes it custom. This is the heal path for a
	// config written before EnvAPIURL existed.
	legacy := &Config{Env: "beta", ConductorURL: "https://api.dev.example"}
	legacy.SetAPIURL("https://api.dev.example")
	if legacy.Env != EnvCustom {
		t.Errorf("a legacy config heals to %q on an explicit set, got %q", EnvCustom, legacy.Env)
	}
}

// The stored label is only true while the URL the environment wrote is
// still the URL in force. Recording that URL is what makes the check
// exact rather than a guess.
func TestGetEnvAgainstTheRecordedEnvironmentURL(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")

	t.Run("URL still matches the environment", func(t *testing.T) {
		c := &Config{Env: "beta", ConductorURL: "https://api.beta.example", EnvAPIURL: "https://api.beta.example"}
		if got := c.GetEnv(); got != "beta" {
			t.Errorf("GetEnv() = %q, want beta", got)
		}
	})
	t.Run("URL was changed away from the environment", func(t *testing.T) {
		c := &Config{Env: "beta", ConductorURL: "https://api.dev.example", EnvAPIURL: "https://api.beta.example"}
		if got := c.GetEnv(); got != EnvCustom {
			t.Errorf("GetEnv() = %q, want %q", got, EnvCustom)
		}
	})
	t.Run("legacy config with nothing recorded reports the stored label", func(t *testing.T) {
		c := &Config{Env: "beta", ConductorURL: "https://api.dev.example"}
		if got := c.GetEnv(); got != "beta" {
			t.Errorf("with nothing to compare against the stored label stands, got %q", got)
		}
	})
	t.Run("ApplyEnvironment records the pair", func(t *testing.T) {
		c := &Config{Env: "beta", ConductorURL: "https://api.dev.example"}
		c.ApplyEnvironment("prod", "https://console.example", "https://api.example")
		if c.Env != "prod" || c.ConductorURL != "https://api.example" || c.EnvAPIURL != "https://api.example" {
			t.Errorf("ApplyEnvironment left %+v", c)
		}
		if got := c.GetEnv(); got != "prod" {
			t.Errorf("GetEnv() = %q right after applying prod", got)
		}
	})
	t.Run("SetAPIURL clears the recorded environment URL", func(t *testing.T) {
		c := &Config{Env: "beta", ConductorURL: "https://api.beta.example", EnvAPIURL: "https://api.beta.example"}
		c.SetAPIURL("http://localhost:3025")
		if c.EnvAPIURL != "" {
			t.Errorf("EnvAPIURL should be cleared, got %q", c.EnvAPIURL)
		}
		if got := c.GetEnv(); got != EnvCustom {
			t.Errorf("GetEnv() = %q, want %q", got, EnvCustom)
		}
	})
}
