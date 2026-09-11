package config

import (
	"fmt"
	"os"
	"strings"
)

// GetAPIURL returns the selected API URL and warns about an insecure scheme.
func (c *Config) GetAPIURL() string {
	u := c.GetAPIURLQuiet()
	if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") {
		fmt.Fprintf(os.Stderr, "Warning: API URL uses non-HTTPS scheme: %s\n", u)
	}
	return u
}

// GetAPIURLQuiet selects the same URL for advisory reads that must print no warnings.
func (c *Config) GetAPIURLQuiet() string {
	if envURL := os.Getenv("RUNOS_API_URL"); envURL != "" {
		return envURL
	}
	return c.ConductorURL
}
