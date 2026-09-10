package output

import (
	"strings"
	"testing"
)

func TestTeardownRemedyRetainsGuidanceForSurvivingProviders(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"not_requested", "pending", "failed", "unknown", "future_value"} {
		t.Run(provider, func(t *testing.T) {
			remedy := "Check the surviving machine. Run runos uninstall locally.\nWait for the dispatch guard."
			if got := teardownRemedy(map[string]any{"providerState": provider, "remedy": remedy}); got != remedy {
				t.Fatalf("provider %s lost guidance: %q", provider, got)
			}
		})
	}
}

func TestTeardownRemedyPreservesIndependentRecovery(t *testing.T) {
	t.Parallel()
	for _, remedy := range []string{
		"Wait for the unresolved dispatch guard before another operation.",
		"Inspect the recovery status.\nContact support if tracking remains unavailable.",
		"Read https://example.test/recovery for the remaining steps.",
	} {
		if got := teardownRemedy(map[string]any{"providerState": "confirmed_destroyed", "remedy": remedy}); got != remedy {
			t.Errorf("independent recovery changed:\nwant %s\ngot %s", remedy, got)
		}
	}
}

func TestTeardownRemedySuppressesLocalLogin(t *testing.T) {
	t.Parallel()
	for _, remedy := range []string{
		"Log in to the machine and inspect its services.",
		"Use SSH to access the machine. Run runos uninstall locally.",
		"Check the host before running runos uninstall.",
	} {
		if got := teardownRemedy(map[string]any{"providerState": "confirmed_destroyed", "remedy": remedy}); strings.TrimSpace(got) != "" {
			t.Errorf("destroyed provider retained local advice: %s", got)
		}
	}
}
