package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
)

var servicesPullCmd = &cobra.Command{
	Use:   "pull [yaml-file]",
	Short: "Download running service config to a local YAML file",
	Long: `Pull a service's config from a cluster to a local YAML file.

Modes (precedence order):

  1. <yaml-file> positional:    re-pull the named yaml in place. Type/cid/id
                                are read from the file.
  2. --type + --id:             first-time pull. Writes
                                runos.service.<cid>.<sid>.yaml in --out (or cwd).

The yaml schema is derived at runtime from the conductor manifest, so
adding a new service type or field on conductor side flows through here
without a CLI change. Run 'runos manifest update' if you've recently
upgraded conductor.

Examples:
  runos services pull --type postgresql --id mjn1d --cid mycluster3
  runos services pull runos.service.mycluster3.mjn1d.yaml`,
	Args: cobra.MaximumNArgs(1),
	RunE: runServicesPull,
}

func init() {
	servicesPullCmd.Flags().String("cid", "", "cluster ID (required for --type+--id mode; sourced from the yaml's cid: field when re-pulling a yaml positional)")
	servicesPullCmd.Flags().String("type", "", "service type (e.g. postgresql, valkey, mysql)")
	servicesPullCmd.Flags().String("id", "", "service id (5-char identifier)")
	servicesPullCmd.Flags().StringP("out", "o", "", "output directory (defaults to cwd; ignored when re-pulling a yaml-file positional)")
	servicesPullCmd.Flags().BoolP("force", "f", false, "overwrite local file even when it has diverged from the server")
	servicesPullCmd.Flags().BoolP("json", "j", false, "output pull summary as JSON")
}

// servicePullSummary mirrors the apps_pull JSON shape: one entry for the
// pulled service, optional skip reasons. Kept simple for v1; expand if
// services_pull --all is added later.
type servicePullSummary struct {
	CID  string                `json:"cid"`
	Path string                `json:"path,omitempty"`
	Type string                `json:"type,omitempty"`
	ID   string                `json:"id,omitempty"`
	InSync bool                `json:"inSync"`
	Drifted bool               `json:"drifted,omitempty"`
}

func runServicesPull(cmd *cobra.Command, args []string) error {
	ctx, err := prepareServicesCmd(cmd)
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")
	jsonOut, _ := cmd.Flags().GetBool("json")
	outFlag, _ := cmd.Flags().GetString("out")
	typeFlag, _ := cmd.Flags().GetString("type")
	sidFlag, _ := cmd.Flags().GetString("id")

	// Resolve target: yaml positional re-pull, or --type+--service-id first-time.
	var (
		serviceType, sid, destPath string
	)
	switch {
	case len(args) == 1:
		yamlPath, err := filepath.Abs(args[0])
		if err != nil {
			return fmt.Errorf("resolve yaml path: %w", err)
		}
		existing, err := services.Load(yamlPath)
		if err != nil {
			return fmt.Errorf("read yaml: %w", err)
		}
		if existing.AID != ctx.cfg.AccountID {
			return fmt.Errorf("yaml is for account %q but you're logged in as %q", existing.AID, ctx.cfg.AccountID)
		}
		if err := ctx.bindToYAML(existing.CID); err != nil {
			return err
		}
		if existing.ID == "" {
			return fmt.Errorf("yaml at %q has no id, run 'runos services sync' to provision first", yamlPath)
		}
		serviceType, sid, destPath = existing.Type, existing.ID, yamlPath
	case typeFlag != "" && sidFlag != "":
		if err := ctx.requireCID(); err != nil {
			return err
		}
		serviceType, sid = typeFlag, sidFlag
		dir := outFlag
		if dir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get cwd: %w", err)
			}
			dir = cwd
		}
		// If a service yaml for this id already exists in the
		// destination dir under any name (canonical or user-renamed),
		// re-pull into THAT path rather than creating a duplicate
		// under the canonical filename.
		if existing, ferr := services.FindByID(dir, ctx.cid, sid); ferr == nil && existing != "" {
			destPath = existing
		} else {
			destPath = services.FilenameFor(dir, ctx.cid, serviceType, sid)
		}
	default:
		return fmt.Errorf("pass a yaml file, or provide --type and --id for a first-time pull")
	}

	pulled, err := services.Pull(ctx.exec, ctx.manifest, serviceType, ctx.cid, ctx.cfg.AccountID, sid)
	if err != nil {
		return err
	}

	// Drift gate: refuse to overwrite local edits unless --force.
	if _, statErr := os.Stat(destPath); statErr == nil {
		diff, err := services.ComputeDiff(destPath, pulled)
		if err != nil {
			return fmt.Errorf("compare local against server: %w", err)
		}
		if diff.Status == services.StatusDrift && !force {
			if !jsonOut {
				fmt.Printf("\n%s/%s on cluster %s, local edits would be lost. Refusing to overwrite.\n\n", serviceType, sid, ctx.cid)
				fmt.Println(diff.UnifiedDiff)
				fmt.Printf("Review:    runos services diff %s\n", destPath)
				fmt.Printf("Overwrite: runos services pull %s --force\n", destPath)
			} else {
				_ = emitJSON(servicePullSummary{
					CID: ctx.cid, Path: destPath, Type: serviceType, ID: sid, Drifted: true,
				})
			}
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return fmt.Errorf("local drift; re-run with --force to overwrite")
		}
	}

	// Ensure parent directory exists, then save.
	if dir := filepath.Dir(destPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := services.Save(destPath, pulled); err != nil {
		return err
	}

	if jsonOut {
		return emitJSON(servicePullSummary{
			CID: ctx.cid, Path: destPath, Type: serviceType, ID: sid, InSync: true,
		})
	}
	fmt.Printf("Pulled %s/%s into %s\n", serviceType, sid, destPath)
	return nil
}

