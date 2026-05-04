package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/runos-official/cli/internal/apps"

	"github.com/spf13/cobra"
)

var appsListPreviousUploadsCmd = &cobra.Command{
	Use:   "list-previous-uploads <yaml-file>",
	Short: "List archived CLI deploy uploads for an app",
	Long: `Print every CLI-uploaded source archive recorded for the app
identified by <yaml-file>. Each row carries a cliUploadID you can pass to
"runos apps pull --code-version <id>" to restore that exact source
locally; running "runos deploy" from the per-app directory then redeploys
the older code.

Restore previous code:

  runos apps pull <yaml-file> --code-version <cliUploadID> --force
  cd runos.<cid>.<id>
  runos deploy

Examples:
  runos apps list-previous-uploads runos.mycluster3.appid4/runos.yaml
  runos apps list-previous-uploads runos.mycluster3.appid4/runos.yaml --json`,
	Args: cobra.ExactArgs(1),
	RunE: runAppsListPreviousUploads,
}

func init() {
	appsListPreviousUploadsCmd.Flags().String("cid", "", "cluster ID (context guard; cross-checked against the yaml)")
	appsListPreviousUploadsCmd.Flags().BoolP("json", "j", false, "emit archive list as JSON")
}

// listPreviousUploadsSummary is the JSON shape emitted by
// `apps list-previous-uploads --json`. Archives is sorted newest-first
// (matching the human table). CurrentCliUploadID lets the caller mark
// which row corresponds to the local source state.
type listPreviousUploadsSummary struct {
	CID      string            `json:"cid"`
	AppID    string            `json:"appId"`
	AppName  string            `json:"appName"`
	Archives []apps.CliArchive `json:"archives"`
	// CurrentCliUploadID is the cliUploadID recorded in the per-app
	// directory's sidecar (set by `apps pull --code` or by a successful
	// deploy). Empty when no sidecar is present. Lets callers (and the
	// AI) tell which row corresponds to the local source state.
	CurrentCliUploadID string `json:"currentCliUploadId,omitempty"`
}

func runAppsListPreviousUploads(cmd *cobra.Command, args []string) error {
	ctx, err := prepareAppsCmd(cmd)
	if err != nil {
		return err
	}

	yamlPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve yaml path: %w", err)
	}
	localApp, err := apps.LoadLocalApp(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("yaml file %q not found", yamlPath)
		}
		return fmt.Errorf("read yaml: %w", err)
	}
	if localApp.ID == "" || localApp.CID == "" || localApp.AID == "" {
		return fmt.Errorf("yaml at %q is missing id/cid/aid, pull again to refresh", yamlPath)
	}
	if localApp.AID != ctx.cfg.AccountID {
		return fmt.Errorf("yaml is for account %q but you're logged in as %q", localApp.AID, ctx.cfg.AccountID)
	}
	if err := ctx.bindToYAML(localApp.CID); err != nil {
		return err
	}

	archives, err := ctx.svc.ListCliArchives(localApp.ID)
	if err != nil {
		return fmt.Errorf("list archives: %w", err)
	}

	// Most-recent first.
	sort.Slice(archives, func(i, j int) bool { return archives[i].PushTime > archives[j].PushTime })

	// Read the sidecar so we can mark the row that matches the local
	// source state. Missing/unreadable sidecar -> just no marker.
	currentID, _ := apps.ReadSourceVersion(filepath.Dir(yamlPath), localApp.CID, localApp.ID)

	jsonOutput, _ := cmd.Flags().GetBool("json")
	summary := listPreviousUploadsSummary{
		CID:                ctx.cid,
		AppID:              localApp.ID,
		AppName:            localApp.App,
		Archives:           archives,
		CurrentCliUploadID: currentID,
	}

	if jsonOutput {
		return emitJSON(summary)
	}

	if len(archives) == 0 {
		fmt.Printf("No CLI uploads recorded for %s (%s) on cluster %s.\n", localApp.App, localApp.ID, ctx.cid)
		fmt.Println("Apps deployed via git/CI or before the agent upgrade have no CLI archives.")
		return nil
	}

	fmt.Printf("CLI uploads for %s (%s) on cluster %s:\n\n", localApp.App, localApp.ID, ctx.cid)
	fmt.Printf("    %-38s  %-22s  %s\n", "CLI UPLOAD ID", "PUSH TIME", "SIZE")
	for _, a := range archives {
		marker := "  "
		if a.CliUploadID == currentID {
			marker = "* "
		}
		fmt.Printf("  %s%-38s  %-22s  %s\n", marker, a.CliUploadID, a.PushTime, humanSize(a.Size))
	}
	if currentID != "" {
		fmt.Println("\n  * = current local source")
	}
	fmt.Printf("\nRestore an older version:\n")
	fmt.Printf("  runos apps pull %s --code-version <cliUploadID> --force\n", args[0])
	fmt.Printf("  cd %s && runos deploy\n", apps.DefaultBaseName(ctx.cid, localApp.ID))
	return nil
}
