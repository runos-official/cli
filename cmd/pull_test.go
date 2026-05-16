package cmd

import (
	"sort"
	"testing"

	"github.com/spf13/pflag"
)

// Regression test for the "`runos pull` not hoisted to top level" UX bug.
// The top-level `pull` alias must be wired on rootCmd and must expose the
// same flag surface as `apps pull` (every flag the user reaches for via
// `apps pull` has to work via the alias too). If a future maintainer adds
// a flag to appsPullCmd but forgets the alias, this test fails loudly.
func TestPullAliasMirrorsAppsPullFlags(t *testing.T) {
	if _, _, err := rootCmd.Find([]string{"pull"}); err != nil {
		t.Fatalf("rootCmd.Find([\"pull\"]) = %v; pullCmd is not registered on rootCmd", err)
	}

	apps := flagNamesSorted(appsPullCmd.Flags())
	alias := flagNamesSorted(pullCmd.Flags())
	if len(apps) != len(alias) {
		t.Fatalf("flag count mismatch: apps pull has %d flags %v, pull alias has %d flags %v", len(apps), apps, len(alias), alias)
	}
	for i, name := range apps {
		if alias[i] != name {
			t.Errorf("flag set mismatch at index %d: apps pull = %q, alias = %q", i, name, alias[i])
		}
	}
}

func flagNamesSorted(fs *pflag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *pflag.Flag) {
		names = append(names, f.Name)
	})
	sort.Strings(names)
	return names
}
