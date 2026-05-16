package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
)

var servicesDiffCmd = &cobra.Command{
	Use:   "diff [yaml-file]",
	Short: "Compare a local service yaml against the cluster",
	Long: `Compare a local runos.service.<cid>.<sid>.yaml against the cluster.

Output is the same unified-diff style as 'runos apps diff'. The diff is
read-only and never writes to disk; use 'runos services sync' to push
local edits back, or 'runos services pull --force' to overwrite local
with current server state.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runServicesDiff,
}

func init() {
	servicesDiffCmd.Flags().String("cid", "", "cluster ID (optional; defaults to the yaml's cid: field, cross-checked against the yaml when set)")
	servicesDiffCmd.Flags().BoolP("json", "j", false, "output diff as JSON")
}

func runServicesDiff(cmd *cobra.Command, args []string) (rerr error) {
	jsonOut, _ := cmd.Flags().GetBool("json")
	// I4-G: route errors through the JSON envelope when --json is set
	// so CI parsers don't have to special-case the failure path.
	defer func() {
		if jsonOut && rerr != nil {
			rerr = emitJSONError(cmd, rerr)
		}
	}()

	ctx, err := prepareServicesCmd(cmd)
	if err != nil {
		return err
	}

	if len(args) != 1 {
		return fmt.Errorf("pass the path to a runos.service.<cid>.<sid>.yaml file")
	}
	yamlPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve yaml path: %w", err)
	}

	local, err := services.Load(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("yaml file %q not found", yamlPath)
		}
		return fmt.Errorf("read yaml: %w", err)
	}
	if local.AID != ctx.cfg.AccountID {
		return fmt.Errorf("yaml is for account %q but you're logged in as %q", local.AID, ctx.cfg.AccountID)
	}
	if err := ctx.bindToYAML(local.CID); err != nil {
		return err
	}
	if local.ID == "" {
		// No id means the service hasn't been provisioned yet; the
		// "diff" against an empty server side is trivially everything
		// the local yaml contains. Render a placeholder rather than
		// fail, so users can use diff to preview a fresh sync.
		if jsonOut {
			return emitJSON(map[string]any{
				"path":   yamlPath,
				"status": services.StatusLocalMissing,
				"note":   "service not yet provisioned (no id in yaml); 'runos services sync' will create it",
			})
		}
		fmt.Printf("Local yaml has no id; service has not been provisioned. Run 'runos services sync %s' to create it.\n", yamlPath)
		return nil
	}

	server, err := services.Pull(ctx.exec, ctx.manifest, local.Type, ctx.cid, ctx.cfg.AccountID, local.ID)
	if err != nil {
		return err
	}
	diff, err := services.ComputeDiff(yamlPath, server)
	if err != nil {
		return err
	}

	if jsonOut {
		if err := emitJSON(diff); err != nil {
			return err
		}
	} else {
		switch diff.Status {
		case services.StatusInSync:
			fmt.Printf("%s/%s on cluster %s: in sync\n", local.Type, local.ID, ctx.cid)
		case services.StatusDrift:
			fmt.Printf("%s/%s on cluster %s: drift\n\n", local.Type, local.ID, ctx.cid)
			fmt.Println(diff.UnifiedDiff)
		case services.StatusLocalMissing:
			fmt.Printf("%s/%s on cluster %s: local file missing (%s)\n", local.Type, local.ID, ctx.cid, diff.Path)
		}
	}
	// Mirror apps_diff's exit-2-on-drift contract so CI gates work the same
	// way for services as they do for apps. The exit-code branch must run
	// for BOTH human and --json output: pre-fix this was after an early
	// `return emitJSON(diff)`, so `services_diff --json` always exited 0
	// even on drift. The MCP wrapper detects exit 2 specifically and
	// surfaces the drift report as a successful result instead of a
	// red-error block, so this matches apps_diff's contract verbatim.
	if diff.Status == services.StatusDrift || diff.Status == services.StatusLocalMissing {
		os.Exit(2)
	}
	return nil
}
