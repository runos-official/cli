package cmd

import (
	"fmt"
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
	Args: cobra.MaximumNArgs(1),
	RunE: runServicesDiff,
}

func init() {
	servicesDiffCmd.Flags().String("cid", "", "cluster ID (overrides default; cross-checked against the yaml)")
	servicesDiffCmd.Flags().BoolP("json", "j", false, "output diff as JSON")
}

func runServicesDiff(cmd *cobra.Command, args []string) error {
	ctx, err := prepareServicesCmd(cmd)
	if err != nil {
		return err
	}
	jsonOut, _ := cmd.Flags().GetBool("json")

	if len(args) != 1 {
		return fmt.Errorf("pass the path to a runos.service.<cid>.<sid>.yaml file")
	}
	yamlPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve yaml path: %w", err)
	}

	local, err := services.Load(yamlPath)
	if err != nil {
		return fmt.Errorf("read yaml: %w", err)
	}
	if local.AID != ctx.cfg.AccountID {
		return fmt.Errorf("yaml is for account %q but you're logged in as %q", local.AID, ctx.cfg.AccountID)
	}
	if local.CID != ctx.cid {
		return fmt.Errorf("cluster mismatch: yaml is for %q but --cid (or default) is %q", local.CID, ctx.cid)
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
		return emitJSON(diff)
	}
	switch diff.Status {
	case services.StatusInSync:
		fmt.Printf("%s/%s on cluster %s: in sync\n", local.Type, local.ID, ctx.cid)
	case services.StatusDrift:
		fmt.Printf("%s/%s on cluster %s: drift\n\n", local.Type, local.ID, ctx.cid)
		fmt.Println(diff.UnifiedDiff)
	case services.StatusLocalMissing:
		fmt.Printf("%s/%s on cluster %s: local file missing (%s)\n", local.Type, local.ID, ctx.cid, diff.Path)
	}
	return nil
}
