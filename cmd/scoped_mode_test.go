package cmd

import "testing"

// TestScopedModeDeclared pins the declared-mode rule story 346's
// acceptance criterion 6 names: scoped mode is on when --scoped is
// passed OR RUNOS_SCOPED=1 is set, and never from anything else (a
// different env value, an unset env, or a false flag with no env).
func TestScopedModeDeclared(t *testing.T) {
	cases := []struct {
		name string
		flag bool
		env  string
		want bool
	}{
		{"neither set", false, "", false},
		{"flag set, no env", true, "", true},
		{"env=1, no flag", false, "1", true},
		{"both set", true, "1", true},
		{"env=true is not 1", false, "true", false},
		{"env=0 is not 1", false, "0", false},
		{"env empty string with flag false", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopedModeDeclared(tc.flag, tc.env); got != tc.want {
				t.Errorf("scopedModeDeclared(%v, %q) = %v, want %v", tc.flag, tc.env, got, tc.want)
			}
		})
	}
}
