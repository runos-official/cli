package config

import "os"

// The `env` label was reported as fact when it was only a memory (goal 21, B3).
//
// `runos config env <name>` writes the environment's URLs and stamps its name into `env`.
// Nothing after that kept the two in step: `runos config set api-url http://localhost:3025`
// changed where every call went and left the label saying `beta`, and so did an RUNOS_API_URL
// override. `runos config get` then reported an environment name beside URLs belonging to a
// different one, which is worse than reporting nothing, because an operator checking which
// environment they are pointed at reads that line and believes it.
//
// The label is only true while the URLs are the ones the environment wrote, so the two are
// kept together: writing a diverging URL clears the label to `custom`, and an env-var override
// reports `custom` without touching the file.

// EnvCustom is the environment label for URLs that no `runos config env
// <name>` wrote: a local development endpoint, an override, or a URL set
// by hand.
const EnvCustom = "custom"

// GetEnv returns the environment label that matches the URLs actually in
// force, rather than the last one `config env` stamped in.
//
// Three ways the label stops being true, all caught here:
//
//   - RUNOS_API_URL overrides the stored URL for this process,
//   - the stored URL is not the one the environment wrote (EnvAPIURL),
//   - no environment was ever applied.
//
// One case it cannot judge: a config written before EnvAPIURL was
// recorded has nothing to compare against, so the stored label is
// reported as-is. The next `config env <name>` or `config set api-url`
// records the pair and the check becomes exact.
func (c *Config) GetEnv() string {
	if c == nil {
		return ""
	}
	if envURL := os.Getenv("RUNOS_API_URL"); envURL != "" && envURL != c.ConductorURL {
		return EnvCustom
	}
	if c.Env == "" {
		return EnvCustom
	}
	if c.EnvAPIURL != "" && c.EnvAPIURL != c.ConductorURL {
		return EnvCustom
	}
	return c.Env
}

// SetAPIURL records a new API URL, clearing the environment label when
// the URL diverges from the one the named environment wrote. Callers use
// this instead of assigning ConductorURL directly, so the label cannot be
// left describing an environment the CLI no longer talks to.
func (c *Config) SetAPIURL(url string) {
	if url != c.ConductorURL {
		c.Env = EnvCustom
		c.EnvAPIURL = ""
	}
	c.ConductorURL = url
}

// ApplyEnvironment records the URLs a named environment wrote, so a
// later divergence is detectable. Used by applyRemoteEnv.
func (c *Config) ApplyEnvironment(name, consoleURL, apiURL string) {
	c.Env = name
	c.ConsoleURL = consoleURL
	c.ConductorURL = apiURL
	c.EnvAPIURL = apiURL
}
