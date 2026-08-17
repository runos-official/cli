// Package cmd implements CLI commands for the RunOS CLI.
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/update"
	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// manifestFileName is the on-disk filename for the cached CLI manifest
// inside ~/.runos/. Mirrors the constant used inside internal/manifest;
// duplicated here so root.go can probe its existence without exporting
// an extra helper from the package.
const manifestFileName = "manifest.json"

// dynamicCmdsUnavailable records, at init time, that the manifest failed
// to load and so the dynacmd-driven subtree (apps show, services list,
// integrations *, account *, ...) is absent from cobra's tree for this
// invocation. PersistentPreRunE reads it to decide what to surface to the
// user, keyed on the real auth state rather than the init-time error.
var dynamicCmdsUnavailable bool

// loginNudgeApplies reports whether an unauthenticated invocation of the
// named command should print the one-line "run runos login" nudge when
// dynamic commands are unavailable. Excluded: the bare root command (it
// shows the fuller welcome banner instead) and commands that are part of
// getting signed in or render their own guidance. Pure for testability.
func loginNudgeApplies(cmdName string) bool {
	switch cmdName {
	case "runos", "login", "logout", "config", "env", "version", "help", "update", "mcp", "desktop":
		return false
	}
	return true
}

// printWelcome writes the first-run welcome shown when a user who is not
// signed in runs bare `runos`. Points only at `runos login`: environment
// selection is a local-dev affordance and must never surface here.
func printWelcome(w io.Writer) {
	fmt.Fprintln(w, "Welcome to RunOS.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "You're not signed in yet. To get started:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  runos login     Sign in with your browser")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Once signed in, run 'runos --help' to see everything you can do.")
}

// shouldBootstrapManifest reports whether PersistentPreRunE should
// attempt to fetch the manifest on this command invocation. Returns
// true when the local cache is missing AND the command is one that
// might use the manifest (i.e. not version/help/config/update/manifest
// itself). Pure function so the skip-list contract is testable.
//
// V6 fix: pre-fix, no first-run bootstrap existed, so manifest-driven
// commands like `runos services sync` hard-failed on a fresh CI install
// while `runos deploy` (statically defined) only soft-warned. Bootstrap
// here unifies both paths.
func shouldBootstrapManifest(cmdName, parentName string, manifestPresent bool) bool {
	if manifestPresent {
		return false
	}
	switch cmdName {
	case "config", "env", "version", "help", "update":
		return false
	}
	// `runos manifest update` does its own fetch. Skip the bootstrap to
	// avoid double-fetching.
	if parentName == "manifest" {
		return false
	}
	return true
}

var rootCmd = &cobra.Command{
	Use:     "runos",
	Short:   "CLI for interacting with RunOS clusters",
	Long:    `RunOS CLI allows you to manage your RunOS clusters, provision services, and interact with your self-hosted cloud infrastructure.`,
	Version: version.Version,
	// Bare `runos`: greet a not-signed-in user with a welcome pointing at
	// `runos login`; otherwise fall back to the normal help output.
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _ := config.Load()
		if !auth.HasCredentials(cfg) {
			printWelcome(cmd.OutOrStdout())
			return nil
		}
		return cmd.Help()
	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip config check for these commands
		cmdName := cmd.Name()
		if cmdName == "config" || cmdName == "env" || cmdName == "version" || cmdName == "help" || cmdName == "update" || (cmd.Parent() != nil && cmd.Parent().Name() == "desktop") {
			return nil
		}
		// The VPN daemon runs as root under launchd: its home is /var/root, it never needs the
		// manifest, and a CDN/config fetch on every boot would write a stray /var/root/.runos.
		// `sudo runos vpn install|uninstall` run as root too and touch only the OS service, so
		// they skip as well (measured: they printed "You're not signed in" for root's empty home).
		if cmd.Parent() != nil && cmd.Parent().Name() == "vpn" &&
			(cmdName == "daemon" || cmdName == "install" || cmdName == "uninstall") {
			return nil
		}
		// Also skip for parent commands that have their own subcommands
		if cmd.Parent() != nil && cmd.Parent().Name() == "config" {
			return nil
		}

		// I25-G / I25-J: refuse explicitly-empty PAT-shape env vars before
		// any auth path falls through to cached Firebase credentials or
		// the on-disk config.json AccountID. Set-but-empty almost always
		// indicates a CI secret-store expansion typo; surfacing it here
		// beats unexpected success using the developer's stored token.
		if err := auth.ValidateAuthEnvVars(os.LookupEnv); err != nil {
			return err
		}

		if !config.Exists() {
			if _, err := config.InitFromRemote(); err != nil {
				return fmt.Errorf("failed to initialize config: %w\nRun 'runos config env <environment>' to set up manually", err)
			}
		}

		// V6: bootstrap the manifest on first run when the cache file is
		// missing, parallel to the config bootstrap above. Soft-warn on
		// failure so a transient network blip doesn't block the run; the
		// dependent command (apps_pull, services_*) will surface its own
		// "(run 'runos manifest update'?)" hint with the wrapped error.
		if home, err := os.UserHomeDir(); err == nil {
			configDir := filepath.Join(home, ".runos")
			manifestPath := filepath.Join(configDir, manifestFileName)
			_, statErr := os.Stat(manifestPath)
			parentName := ""
			if cmd.Parent() != nil {
				parentName = cmd.Parent().Name()
			}
			if shouldBootstrapManifest(cmd.Name(), parentName, statErr == nil) {
				cfg, cfgErr := config.Load()
				if cfgErr == nil {
					loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
					// Not-authenticated is the expected pre-login state, not a
					// first-run failure: stay quiet so a brand-new user isn't
					// told the manifest "failed" before they've even signed in.
					if _, err := loader.Load(); err != nil && !errors.Is(err, auth.ErrNotAuthenticated) && term.IsTerminal(int(os.Stderr.Fd())) {
						fmt.Fprintf(os.Stderr, "Note: failed to fetch CLI manifest on first run (%v). Run 'runos manifest update' to retry.\n", err)
					}
				}
			}
		}

		// When dynamic commands couldn't load at init time, decide what to
		// tell the user based on the real auth state now (post-bootstrap).
		// The bare-root welcome (RunE) and the sign-in commands handle their
		// own messaging, so loginNudgeApplies filters those out.
		if dynamicCmdsUnavailable {
			cfg, _ := config.Load()
			switch {
			case !auth.HasCredentials(cfg):
				// New install or signed-out: nudge toward login instead of
				// leaving cobra silent (errors are suppressed at init).
				if loginNudgeApplies(cmd.Name()) {
					fmt.Fprintln(os.Stderr, "You're not signed in. Run 'runos login' to get started.")
				}
			case term.IsTerminal(int(os.Stderr.Fd())):
				// Signed in but the manifest still didn't load: a genuine
				// problem worth the loud recovery diagnostic.
				fmt.Fprintln(os.Stderr, "Unable to load manifest: dynamic commands (apps show / services list / integrations * / account * ...) are unavailable in this invocation.")
				fmt.Fprintln(os.Stderr, "  Recovery: run 'runos manifest update', or verify RUNOS_API_URL + RUNOS_API_KEY are correct for the target environment.")
			}
		}

		// Check for CLI updates (cached, runs at most once per hour)
		// Only show update notice when stderr is a terminal (not in scripts/CI)
		if term.IsTerminal(int(os.Stderr.Fd())) {
			if latestVersion := update.CheckForUpdate(); latestVersion != "" {
				fmt.Fprintf(os.Stderr, "\nUpdate available: %s (current: %s)\n", latestVersion, update.CurrentVersion())
				fmt.Fprintln(os.Stderr, "Run 'runos update' to install the latest version.")
			}
		}

		return nil
	},
}

// Execute runs the root command and exits with the appropriate code:
// 0 on success, the wrapped ExitCode() on errors that carry one
// (e.g. `runos run` propagating a container's real exit code), or 1
// otherwise. The ExitCode() unwrap lets commands signal a specific
// non-zero code without bypassing cobra's error-formatting flow.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// An unknown command is very often a stale cached command list rather than a command
		// that does not exist, and the two are indistinguishable from the error alone. This
		// says which, and refreshes the cache when it is the cause (goal 21, O10). It never
		// changes the exit code: the command still failed.
		explainPossiblyStaleManifest(err)
		// The next case along: the command WAS found and dispatched, and
		// conductor refused it with a 4xx. That looks the same whether the
		// cached command list is current or months behind, so compare the
		// two once and say so when they differ (goal 21, B7).
		explainManifestDriftOn4xx(err)
		// Same shape, different cause: a cluster-not-found is often a stale CONFIGURED DEFAULT
		// rather than a broken cluster, and the bare message points at the wrong one (O2).
		explainStaleDefaultCluster(err)
		// `--flag null` is the RIGHT guess for a nullable numeric and pflag refuses it, while the
		// wrong guess (`--flag 0`) parses and does the opposite. Name the route that works (O11).
		explainNullNotAcceptedByFlag(err)
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}

func init() {
	// I24-Q: align cobra's auto-generated `runos --version` / `runos -v`
	// output with the bare format the explicit `runos version` subcommand
	// emits. Pre-fix cobra rendered "runos version <X>" while the
	// subcommand printed just "<X>"; CI gates piping through one shape
	// stripped a prefix that the other shape didn't have. Both forms now
	// emit the bare version string + trailing newline.
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	// Static commands - always available
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(followCmd)
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(pullCmd)
	rootCmd.AddCommand(manifestCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(accountCmd)
	rootCmd.AddCommand(desktopCmd)

	// Static commands that will be merged with dynamic commands from manifest
	// These commands have static subcommands (e.g., "clusters default") that coexist
	// with dynamic subcommands from the manifest (e.g., "clusters list", "clusters show")
	rootCmd.AddCommand(clustersCmd)
	rootCmd.AddCommand(servicesCmd)
	rootCmd.AddCommand(appsCmd)
	rootCmd.AddCommand(jobsCmd)
	// Static parent so `vms ssh` and `vms proxy` can sit beside the manifest-driven verbs; the
	// dynamic builder merges its own `vms` children onto this one rather than making a second.
	rootCmd.AddCommand(vmsCmd)

	// Attach the hand-written services/harbor subtree BEFORE the dynamic
	// builder runs. The conductor manifest carries a services/harbor/build-image
	// entry, but the verb needs to tarball + upload a local build context
	// (filesystem state the manifest can't express), so the real command is
	// the hand-written one. Wiring it here means the builder's leaf-collision
	// guard sees `build-image` and skips the manifest duplicate, while still
	// reusing this harbor parent for the manifest-driven harbor verbs.
	wireStaticHarborSubtree()

	// Dynamic commands from manifest
	if err := registerDynamicCommands(); err != nil {
		// I25-E: when the manifest can't load at init time, dynacmd-
		// driven commands (apps show, services list, integrations *,
		// account *, etc.) silently vanish from cobra's tree, and the
		// downstream user-visible failure is a misleading "unknown flag:
		// --cid". Record the gap here; PersistentPreRunE decides what (if
		// anything) to tell the user, because only there do we know the
		// command being run AND the real auth state. A fresh install /
		// signed-out user gets a friendly welcome or login nudge instead
		// of recovery jargon; an authenticated-but-broken setup still gets
		// the loud manifest-recovery diagnostic. The next run retries the
		// fetch (see V6) and picks up dynamic commands once resolved.
		dynamicCmdsUnavailable = true
		// Suppress cobra's downstream "unknown flag: --cid" + 16-line
		// Usage block when the user invokes a vanished dynacmd subcommand.
		// The non-zero exit code is preserved so CI gates still trip.
		rootCmd.SilenceUsage = true
		rootCmd.SilenceErrors = true
	}

	// Apply after every static and dynamic AddCommand so the silent-help
	// exit 0 bug is closed for the whole tree (issue #11). Must run last
	// because dynacmd creates intermediate parents (nodes, jobs, account,
	// integrations, services/<type>, ...) inline during BuildCommands.
	strictenParentExitCodes(rootCmd)
}

func registerDynamicCommands() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Get config directory for manifest storage
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(home, ".runos")

	// Load manifest from Conductor API
	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return err
	}

	// Build and register commands
	// Pass existing commands that have static subcommands so dynamic commands merge with them.
	// Static `config` and `deploy` commands also need to be registered as existing so the
	// dynamic builder merges any manifest-side definitions instead of producing duplicate
	// top-level entries (which used to render `config` and `deploy` twice in `runos --help`).
	executor := dynacmd.NewExecutor(cfg.GetAPIURL())
	builder := dynacmd.NewBuilder(m, executor).
		WithExistingCommands(clustersCmd, servicesCmd, appsCmd, jobsCmd, configCmd, deployCmd, mcpCmd, vmsCmd, vpnCmd, accountCmd)

	for _, cmd := range builder.BuildCommands() {
		rootCmd.AddCommand(cmd)
	}

	return nil
}
