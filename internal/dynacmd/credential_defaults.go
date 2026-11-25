package dynacmd

import (
	"fmt"
	"time"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// defaultAPIKeyExpiryDays is the CLI-side fallback for the `expiresAt`
// field on `account/api-keys/add` when the user omits `--expires-at`.
// Matches the api-keys topic's recommended rotation cadence and the
// conductor's documented 90d default for `limited` keys. Pure constant
// so the regression test can assert the exact derivation.
const defaultAPIKeyExpiryDays = 90

// applyCLIDefaults injects CLI-side fallback values into the cobra
// command before the missing-required-arg gate runs. Each entry is a
// targeted carve-out for a specific (command, field) pair where the
// conductor's `createPatCommand` / setupInstructions shape omits a
// required field for usability reasons. Currently scoped to
// `account/api-keys/add.expiresAt` (I25-AP). Explicit user input
// (positional, flag, or `-f` file) wins over any default applied here.
//
// foreman #132: when the user passes --expires-at explicitly, also
// validate that the value carries a timezone. Bare RFC 3339 like
// "2026-08-01T00:00:00" was silently interpreted as local time by the
// server, producing keys that expired up to 14 hours off the user's
// intent on non-UTC operators. Refuse client-side with a message that
// names the two accepted shapes.
func applyCLIDefaults(c *cobra.Command, cmdDef manifest.Command) error {
	if cmdDef.Command != "account/api-keys/add" {
		return nil
	}
	const flagName = "expires-at"
	if c.Flags().Changed(flagName) {
		raw, _ := c.Flags().GetString(flagName)
		if err := validateExpiresAt(raw); err != nil {
			return err
		}
		return nil
	}
	bodyFilePath, _ := c.Flags().GetString("file")
	if bodyFilePath != "" && bodyFilePresentsField(bodyFilePath, "expiresAt") {
		return nil
	}
	value := time.Now().UTC().Add(time.Duration(defaultAPIKeyExpiryDays) * 24 * time.Hour).Format(time.RFC3339)
	if err := c.Flags().Set(flagName, value); err != nil {
		return fmt.Errorf("apply default --%s: %w", flagName, err)
	}
	return nil
}

// validateExpiresAt refuses RFC 3339 timestamps without a timezone
// designator. time.Parse(time.RFC3339, ...) already requires a Z or
// numeric offset, so a successful parse is the contract. The error
// message names both accepted shapes so the user knows the recovery
// path. Regression target: foreman #132.
func validateExpiresAt(raw string) error {
	if raw == "" {
		return fmt.Errorf("--expires-at: empty value (use RFC 3339 with timezone, e.g. 2026-08-01T00:00:00Z or 2026-08-01T00:00:00+02:00)")
	}
	if _, err := time.Parse(time.RFC3339, raw); err != nil {
		return fmt.Errorf("--expires-at %q: must be RFC 3339 with a timezone (e.g. 2026-08-01T00:00:00Z or 2026-08-01T00:00:00+02:00); bare local-time values are refused so the key doesn't silently expire on a different wall clock", raw)
	}
	return nil
}
