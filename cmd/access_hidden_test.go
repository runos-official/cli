package cmd

import (
	"testing"

	"github.com/runos-official/cli/internal/config"
)

// 'runos access' only works with a roid sign-in, so an environment that signs
// in through Firebase must not list it in help.
func TestAccessIsHiddenUnlessTheEnvironmentNamesAnIssuer(t *testing.T) {
	cases := []struct {
		name   string
		write  bool
		issuer string
		hidden bool
	}{
		{"no config file", false, "", true},
		{"Firebase environment", true, "", true},
		{"roid environment", true, "https://id.example.com", false},
	}
	t.Cleanup(func() { accessCmd.Hidden = false })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfigEnv(t)
			if tc.write {
				cfg := &config.Config{AuthIssuer: tc.issuer}
				cfg.ApplyEnvironment("dev", "https://console.example.com", "https://api.example.com")
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
			}
			accessCmd.Hidden = !tc.hidden
			hideAccessUnlessRoid()
			if accessCmd.Hidden != tc.hidden {
				t.Fatalf("Hidden = %v, want %v", accessCmd.Hidden, tc.hidden)
			}
		})
	}
}
