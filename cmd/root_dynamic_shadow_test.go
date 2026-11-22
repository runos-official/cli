package cmd

import (
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
)

// T1 (FPL573): a manifest carrying a top-level command with the same
// name as a static command must never produce a second top-level
// command of that name. Before filterShadowedCommands existed, this
// test failed with two "whoami" entries under root and cobra running
// whichever was added first.
func TestFilterShadowedCommandsKeepsTheStaticCommand(t *testing.T) {
	m := &manifest.Manifest{
		Commands: []manifest.Command{
			{Command: "whoami", Endpoint: "/whoami", Method: "GET"},
		},
	}
	executor := dynacmd.NewExecutor("http://127.0.0.1:0") // never dialed: the colliding command must be filtered before dispatch
	builder := dynacmd.NewBuilder(m, executor)
	dynamicCmds := builder.BuildCommands()
	if len(dynamicCmds) != 1 || dynamicCmds[0].Name() != "whoami" {
		t.Fatalf("builder did not produce one colliding whoami command: %v", dynamicCmds)
	}

	root := &cobra.Command{Use: "runos"}
	ran := false
	root.AddCommand(&cobra.Command{
		Use: "whoami",
		RunE: func(cmd *cobra.Command, args []string) error {
			ran = true
			return nil
		},
	})

	for _, cmd := range filterShadowedCommands(root, dynamicCmds) {
		root.AddCommand(cmd)
	}

	var whoamis int
	for _, cmd := range root.Commands() {
		if cmd.Name() == "whoami" {
			whoamis++
		}
	}
	if whoamis != 1 {
		t.Fatalf("root has %d commands named whoami, want exactly 1", whoamis)
	}

	root.SetArgs([]string{"whoami"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ran {
		t.Fatal("the static whoami command did not run")
	}
}

func TestFilterShadowedCommandsKeepsNonCollidingCommands(t *testing.T) {
	root := &cobra.Command{Use: "runos"}
	root.AddCommand(&cobra.Command{Use: "whoami"})

	dynamicCmds := []*cobra.Command{
		{Use: "whoami"},
		{Use: "integrations"},
	}
	kept := filterShadowedCommands(root, dynamicCmds)
	if len(kept) != 1 || kept[0].Name() != "integrations" {
		t.Fatalf("kept = %v, want only the non-colliding command", kept)
	}
}
