package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/desktop"
	"github.com/spf13/cobra"
)

func TestDesktopHumanOutputUsesGenericProductCopy(t *testing.T) {
	command := &cobra.Command{}
	command.Flags().Bool("json", false, "")
	var output bytes.Buffer
	command.SetOut(&output)
	result := &desktop.Result{
		Installed: true,
		Version:   "0.1.0-rc.1",
		Path:      "/Applications/RunOS Desktop.app",
		Unsigned:  true,
		Message:   "Installed RunOS Desktop.",
	}

	if err := emitDesktopResult(command, result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Developer ID") || strings.Contains(output.String(), "ad hoc") {
		t.Fatalf("human output leads with signature details: %q", output.String())
	}
}
