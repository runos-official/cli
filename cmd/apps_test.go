package cmd

import (
	"strings"
	"testing"
)

// Regression test for the `runos apps <unknown>` exit-0 bug.
//
// Pre-fix, `runos apps typoo` printed the apps help and exited 0, while
// `runos doesnotexist` (root-level) correctly errored with exit 1. The
// asymmetry let CI gates like `runos apps typoo && next-step` silently
// run next-step. Cobra's legacyArgs only fires on the root
// (`!HasParent()` branch), and a non-runnable parent short-circuits to
// help BEFORE ValidateArgs runs (cobra v1.10 command.go:955). Two pieces
// are required for the fix to work: Args must be set, AND the parent
// must be runnable (RunE non-nil) so cobra reaches ValidateArgs.
func TestAppsCmd_RejectsUnknownSubcommand(t *testing.T) {
	if appsCmd.RunE == nil {
		t.Fatal("appsCmd.RunE is nil; cobra short-circuits non-runnable parents to help before ValidateArgs, so Args is never consulted")
	}
	if appsCmd.Args == nil {
		t.Fatal("appsCmd.Args is nil; without an Args validator cobra accepts any unknown subcommand silently")
	}

	t.Run("unknown subcommand returns error", func(t *testing.T) {
		err := appsCmd.Args(appsCmd, []string{"typoo"})
		if err == nil {
			t.Fatal("expected error for unknown subcommand, got nil")
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("error %q should mention \"unknown command\"", err.Error())
		}
	})

	t.Run("bare apps passes Args validation", func(t *testing.T) {
		if err := appsCmd.Args(appsCmd, []string{}); err != nil {
			t.Errorf("bare `apps` should not error, got %v", err)
		}
	})
}

// Regression for issue 83: when --cid wasn't explicitly passed,
// the yaml's cid wins silently. An explicit --cid mismatch still
// refuses (cross-cluster-push guard).
func TestReconcileCIDWithYAML_Issue83(t *testing.T) {
	cases := []struct {
		name        string
		ctxCID      string
		cidExplicit bool
		yamlCID     string
		want        string
		wantErr     string
	}{
		{"empty ctx adopts yaml", "", false, "mycluster3", "mycluster3", ""},
		{"matching ctx kept", "mycluster3", true, "mycluster3", "mycluster3", ""},
		{"matching ctx (implicit) kept", "mycluster3", false, "mycluster3", "mycluster3", ""},
		{"explicit --cid mismatch refuses", "mycluster2", true, "mycluster3", "", "cluster mismatch"},
		{"implicit default mismatch adopts yaml", "mycluster2", false, "mycluster3", "mycluster3", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reconcileCIDWithYAML(tc.ctxCID, tc.cidExplicit, tc.yamlCID)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got cid=%q, want %q", got, tc.want)
			}
		})
	}
}
