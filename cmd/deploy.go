package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/jobs"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy an app from the current directory",
	Long: `Deploy an application to a RunOS cluster.

This command reads a runos.yaml configuration file from the current directory,
creates a tarball of the project files, and deploys it to the specified cluster.

The runos.yaml file should contain at minimum:
  app: "My App Name"
  port: 8080

Optional fields include dockerfile, resource limits, and service dependencies.`,
	RunE: runDeploy,
}

var deploySyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync local config with deployed app state",
	Long: `Sync the local runos.yaml config file with the deployed application state.

This command fetches the app ID and dependency IDs from the deployed application
and updates the local config file. Use this to:
- Link an existing config to a deployed app
- Refresh IDs after deployment
- Restore IDs that were accidentally removed`,
	RunE: runDeploySync,
}

func init() {
	deployCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file")
	deployCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	deployCmd.Flags().BoolP("follow", "f", false, "follow job progress until completion")
	deployCmd.Flags().BoolP("json", "j", false, "output response as JSON")

	// Add sync subcommand
	deploySyncCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file")
	deploySyncCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	deployCmd.AddCommand(deploySyncCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	// Load CLI config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get auth token
	token, err := getDeployAuthToken(cfg)
	if err != nil {
		return fmt.Errorf("authentication required: run 'runos login' first")
	}

	// Get cluster ID
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if cid == "" {
		return fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
	}

	// Get account ID
	if cfg.AccountID == "" {
		return fmt.Errorf("account ID not set: run 'runos login' first")
	}

	// Load deploy config
	configPath, _ := cmd.Flags().GetString("config")
	if !filepath.IsAbs(configPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		configPath = filepath.Join(cwd, configPath)
	}

	deployConfig, err := deploy.LoadConfig(configPath)
	if err != nil {
		return err
	}

	// Validate AID
	if err := deploy.ValidateAID(deployConfig.AID, cfg.AccountID); err != nil {
		return err
	}

	// Load custom env vars from .runos.{CID}.env if it exists
	customEnvVars, err := deploy.LoadEnvFile(filepath.Dir(configPath), cid)
	if err != nil {
		return fmt.Errorf("failed to load env file: %w", err)
	}
	if customEnvVars != nil {
		deployConfig.CustomEnvVars = customEnvVars
	}

	// Create deploy service
	svc := deploy.NewService(cfg.GetConductorURL(), token, cid, cfg.AccountID)

	// Check if app already exists but config has no ID
	if deployConfig.ID == "" {
		existingApp, err := svc.FindAppByName(deployConfig.App)
		if err == nil && existingApp != nil {
			fmt.Printf("An app named '%s' already exists (ID: %s).\n", deployConfig.App, existingApp.ID)
			fmt.Println("Run 'runos deploy sync' to link to existing app, or rename the app in runos.yaml.")
			return fmt.Errorf("app already exists - sync or rename required")
		}
	}

	// Pre-deploy sync: catch any console-side changes before deploying
	if err := preDeploySync(svc, deployConfig, configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy sync failed: %v\n", err)
	}

	fmt.Printf("Deploying %s...\n", deployConfig.App)

	// Prepare deployment
	fmt.Println("Preparing deployment...")
	prepResp, err := svc.PrepareDeployment(deployConfig)
	if err != nil {
		return fmt.Errorf("failed to prepare deployment: %w", err)
	}

	// Immediately sync config with IDs from prepare response (services are pre-generated)
	if err := syncConfigFromPrepareResponse(deployConfig, configPath, prepResp, cid, cfg.AccountID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update config file: %v\n", err)
	}

	// Create tarball
	fmt.Println("Creating archive...")
	configDir := filepath.Dir(configPath)
	tarball, err := deploy.CreateTarball(configDir)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}

	fmt.Printf("Archive size: %d bytes\n", tarball.Len())

	// Upload tarball
	fmt.Println("Uploading archive...")
	if err := svc.UploadTarball(prepResp.UploadURL, prepResp.Token, tarball); err != nil {
		return fmt.Errorf("failed to upload archive: %w", err)
	}

	fmt.Println("Upload complete.")

	// Output response
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		output, err := json.MarshalIndent(prepResp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal response: %w", err)
		}
		fmt.Println(string(output))
	} else {
		fmt.Printf("\nDeployment initiated:\n")
		fmt.Printf("  Job ID: %s\n", prepResp.JobID)
		fmt.Printf("  App ID: %s\n", prepResp.AppID)
	}

	// Follow job if requested
	follow, _ := cmd.Flags().GetBool("follow")
	if follow {
		fmt.Println("\nFollowing job progress...")
		if err := jobs.FollowJob(prepResp.JobID); err != nil {
			return fmt.Errorf("deployment failed: %w", err)
		}
		fmt.Println("\nDeployment completed successfully!")

		// Extract app ID for network access lookup
		appID := prepResp.AppID
		if appID == "" {
			appID = prepResp.OSID
		}

		// Fetch and display public URL
		networkAccess, err := svc.GetNetworkAccess(appID)
		if err == nil {
			for _, access := range networkAccess {
				if strings.HasPrefix(access.Type, "RUNOS_PUBLIC") {
					fmt.Printf("\nApp available at: %s\n", access.Link)
					break
				}
			}
		}
	}

	return nil
}

// syncConfigFromPrepareResponse updates the config file with IDs from the prepare response
func syncConfigFromPrepareResponse(deployConfig *deploy.DeployConfig, configPath string, prepResp *deploy.PrepareResponse, cid, aid string) error {
	// Update config with app ID
	deployConfig.ID = prepResp.AppID
	if deployConfig.ID == "" {
		deployConfig.ID = prepResp.OSID // Fallback to OSID if appId not set
	}
	deployConfig.CID = cid
	deployConfig.AID = aid

	// Update service IDs from the pre-generated services array
	if prepResp.Services != nil && deployConfig.Requires != nil {
		for _, svc := range prepResp.Services {
			if req, ok := deployConfig.Requires[svc.Alias]; ok {
				req.ID = svc.ID
				deployConfig.Requires[svc.Alias] = req
			}
		}
	}

	// Save config
	return deploy.SaveConfig(configPath, deployConfig)
}

// preDeploySync syncs config from deployed app state before deploying (catches console-side changes)
func preDeploySync(svc *deploy.Service, deployConfig *deploy.DeployConfig, configPath string) error {
	if deployConfig.ID == "" {
		return nil // No existing app, nothing to sync
	}

	// Fetch current dependencies from deployed app
	deps, err := svc.GetAppDependencies(deployConfig.ID)
	if err != nil {
		return nil // Non-fatal, just skip
	}

	changed := false

	// Update requires block with any changes from console
	if deps != nil && deployConfig.Requires != nil {
		for _, dep := range deps {
			if req, ok := deployConfig.Requires[dep.Name]; ok {
				if req.Type == dep.Type && req.ID != dep.ID {
					req.ID = dep.ID
					deployConfig.Requires[dep.Name] = req
					changed = true
				}
			}
		}
	}

	if changed {
		if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
			return err
		}
		fmt.Println("Config synced with deployed app state.")
	}

	return nil
}

func getDeployAuthToken(cfg *config.Config) (string, error) {
	if cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}
	return auth.GetIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
}

func runDeploySync(cmd *cobra.Command, args []string) error {
	// Load CLI config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get auth token
	token, err := getDeployAuthToken(cfg)
	if err != nil {
		return fmt.Errorf("authentication required: run 'runos login' first")
	}

	// Get cluster ID
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if cid == "" {
		return fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
	}

	// Get account ID
	if cfg.AccountID == "" {
		return fmt.Errorf("account ID not set: run 'runos login' first")
	}

	// Load deploy config
	configPath, _ := cmd.Flags().GetString("config")
	if !filepath.IsAbs(configPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		configPath = filepath.Join(cwd, configPath)
	}

	deployConfig, err := deploy.LoadConfig(configPath)
	if err != nil {
		return err
	}

	// Validate AID
	if err := deploy.ValidateAID(deployConfig.AID, cfg.AccountID); err != nil {
		return err
	}

	// Create deploy service
	svc := deploy.NewService(cfg.GetConductorURL(), token, cid, cfg.AccountID)

	// Find the app by name
	fmt.Printf("Looking up app '%s' on cluster %s...\n", deployConfig.App, cid)
	app, err := svc.FindAppByName(deployConfig.App)
	if err != nil {
		return fmt.Errorf("failed to find app: %w", err)
	}
	if app == nil {
		return fmt.Errorf("app '%s' not found on cluster %s. Run 'runos deploy' first", deployConfig.App, cid)
	}

	// Fetch dependencies
	fmt.Println("Fetching app dependencies...")
	deps, err := svc.GetAppDependencies(app.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch dependencies: %v\n", err)
		deps = nil
	}

	// Update config
	deployConfig.ID = app.ID
	deployConfig.CID = cid
	deployConfig.AID = cfg.AccountID

	// Match dependencies to requires block
	if deps != nil && deployConfig.Requires != nil {
		for _, dep := range deps {
			if req, ok := deployConfig.Requires[dep.Name]; ok {
				if req.Type == dep.Type {
					req.ID = dep.ID
					deployConfig.Requires[dep.Name] = req
				}
			}
		}
	}

	// Save config
	if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Print summary
	fmt.Printf("\nConfig file updated:\n")
	fmt.Printf("  App ID: %s\n", deployConfig.ID)
	fmt.Printf("  Cluster: %s\n", deployConfig.CID)
	fmt.Printf("  Account: %s\n", deployConfig.AID)
	if len(deps) > 0 {
		fmt.Println("  Dependencies:")
		for _, dep := range deps {
			fmt.Printf("    %s (%s): %s\n", dep.Name, dep.Type, dep.ID)
		}
	}

	return nil
}
