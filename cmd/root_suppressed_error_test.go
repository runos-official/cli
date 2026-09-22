package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// Story 279: Execute prints the error itself exactly when cobra
// suppressed its own "Error:" line (root silenced at init because the
// manifest failed to load). When cobra already printed, Execute prints
// nothing extra. When a leaf already surfaced its own output (drift
// gates, --json envelopes), Execute stays quiet too.
func TestShouldPrintSuppressedError(t *testing.T) {
	root := &cobra.Command{}
	leaf := &cobra.Command{}
	silencedLeaf := &cobra.Command{SilenceErrors: true}

	cases := []struct {
		name         string
		rootSilenced bool
		executed     *cobra.Command
		want         bool
	}{
		{"normal path prints nothing extra", false, leaf, false},
		{"normal path with silenced leaf prints nothing extra", false, silencedLeaf, false},
		{"suppressed with no executed command prints", true, nil, true},
		{"suppressed resolving to root prints", true, root, true},
		{"suppressed plain leaf prints", true, leaf, true},
		{"suppressed self-printed leaf stays quiet", true, silencedLeaf, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPrintSuppressedError(tt.rootSilenced, tt.executed, root); got != tt.want {
				t.Errorf("shouldPrintSuppressedError(%v, %v) = %v, want %v",
					tt.rootSilenced, tt.executed, got, tt.want)
			}
		})
	}
}
