package cmd

import "testing"

func TestAccountParentContainsStaticCommands(t *testing.T) {
	for _, path := range []string{"list", "add", "switch", "forget"} {
		if command, _, err := accountCmd.Find([]string{path}); err != nil || command == accountCmd {
			t.Fatalf("account %s is not registered", path)
		}
	}
}
