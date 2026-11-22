package cmd

import "github.com/spf13/cobra"

// filterShadowedCommands drops a dynamic (manifest-built) top-level
// command whose name collides with a static command already on root.
// The static command always wins.
//
// Story 427 / panel 61 finding 4: registerDynamicCommands added every
// command BuildCommands returned, even one sharing a name with a static
// command never passed to WithExistingCommands (the deliberate merge
// list for commands like `clusters` that mix static and manifest
// subcommands). An unmerged collision — a manifest `whoami`, say — gave
// root two commands named "whoami"; cobra resolves whichever was added
// first for `runos whoami`, and `runos --help` lists the name twice.
// This closes that silently rather than special-casing one name: no
// manifest command may ever shadow a static one.
func filterShadowedCommands(root *cobra.Command, dynamicCmds []*cobra.Command) []*cobra.Command {
	kept := make([]*cobra.Command, 0, len(dynamicCmds))
	for _, cmd := range dynamicCmds {
		if hasCommandNamed(root, cmd.Name()) {
			continue
		}
		kept = append(kept, cmd)
	}
	return kept
}

func hasCommandNamed(root *cobra.Command, name string) bool {
	for _, existing := range root.Commands() {
		if existing.Name() == name {
			return true
		}
	}
	return false
}
