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

	// Create deploy service
	configDir := filepath.Dir(configPath)
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
	if _, err := syncAppState(svc, deployConfig, configPath, cid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy sync failed: %v\n", err)
	}

	// Load env vars from env file AFTER sync so remote changes are included
	envPath, envConfigChanged := deploy.ResolveEnvPath(configDir, deployConfig, cid)
	if envConfigChanged {
		if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update config with env path: %v\n", err)
		}
	}
	customEnvVars, err := deploy.LoadEnvFile(envPath)
	if err != nil {
		return fmt.Errorf("failed to load env file: %w", err)
	}
	if customEnvVars != nil {
		deployConfig.CustomEnvVars = customEnvVars
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

	// Post-deploy sync: pick up env vars from newly provisioned services (also covers first deploy)
	if _, err := syncAppState(svc, deployConfig, configPath, cid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-deploy sync failed: %v\n", err)
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

// syncResult holds what changed during a sync operation
type syncResult struct {
	deps    []deploy.AppDependency
	envVars map[string]string
}

// syncAppState syncs dependencies and env vars from the deployed app state.
// It updates the config and env file in place. Returns a result for summary printing.
func syncAppState(svc *deploy.Service, deployConfig *deploy.DeployConfig, configPath, cid string) (*syncResult, error) {
	if deployConfig.ID == "" {
		return nil, nil
	}

	configDir := filepath.Dir(configPath)
	result := &syncResult{}

	// Fetch and sync dependencies
	deps, err := svc.GetAppDependencies(deployConfig.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch dependencies: %v\n", err)
	} else {
		result.deps = deps
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
	}

	// Fetch and sync env vars
	envVars, err := svc.GetAppEnvVars(deployConfig.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch env vars: %v\n", err)
	} else {
		result.envVars = envVars
	}

	if len(envVars) > 0 {
		envPath, _ := deploy.ResolveEnvPath(configDir, deployConfig, cid)
		if envPath == "" {
			deployConfig.Env = deploy.DefaultEnvFilename(cid, deployConfig.ID)
			envPath = filepath.Join(configDir, deployConfig.Env)
		}

		localEnvVars, err := deploy.LoadEnvFile(envPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read existing env file: %v\n", err)
		}

		// Check for conflicts: same key, different value
		var conflicts []string
		for key, localVal := range localEnvVars {
			if remoteVal, exists := envVars[key]; exists && localVal != remoteVal {
				conflicts = append(conflicts, key)
			}
		}
		if len(conflicts) > 0 {
			fmt.Fprintf(os.Stderr, "\nEnv var conflicts detected (local value differs from remote):\n")
			for _, key := range conflicts {
				fmt.Fprintf(os.Stderr, "  %s\n    local:  %s\n    remote: %s\n", key, localEnvVars[key], envVars[key])
			}
			return result, fmt.Errorf("resolve env var conflicts in %s before syncing", deployConfig.Env)
		}

		// Merge: start with remote vars, add any local-only vars
		merged := make(map[string]string, len(envVars)+len(localEnvVars))
		for k, v := range envVars {
			merged[k] = v
		}
		for k, v := range localEnvVars {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}

		if err := deploy.SaveEnvFile(envPath, merged); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save env file: %v\n", err)
		}
	}

	// Save config
	if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
		return result, fmt.Errorf("failed to save config: %w", err)
	}

	return result, nil
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

	// Find the app by name and set core IDs
	fmt.Printf("Looking up app '%s' on cluster %s...\n", deployConfig.App, cid)
	app, err := svc.FindAppByName(deployConfig.App)
	if err != nil {
		return fmt.Errorf("failed to find app: %w", err)
	}
	if app == nil {
		return fmt.Errorf("app '%s' not found on cluster %s. Run 'runos deploy' first", deployConfig.App, cid)
	}

	deployConfig.ID = app.ID
	deployConfig.CID = cid
	deployConfig.AID = cfg.AccountID

	// Sync dependencies and env vars
	fmt.Println("Syncing app state...")
	result, err := syncAppState(svc, deployConfig, configPath, cid)
	if err != nil {
		return err
	}

	// Print summary
	fmt.Printf("\nConfig file updated:\n")
	fmt.Printf("  App ID: %s\n", deployConfig.ID)
	fmt.Printf("  Cluster: %s\n", deployConfig.CID)
	fmt.Printf("  Account: %s\n", deployConfig.AID)
	if result != nil && len(result.deps) > 0 {
		fmt.Println("  Dependencies:")
		for _, dep := range result.deps {
			fmt.Printf("    %s (%s): %s\n", dep.Name, dep.Type, dep.ID)
		}
	}
	if result != nil && len(result.envVars) > 0 {
		fmt.Printf("  Env vars: %d synced to %s\n", len(result.envVars), deployConfig.Env)
	}

	return nil
}
