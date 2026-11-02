package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/kubeaccess"

	"github.com/spf13/cobra"
)

var kubeCmd = &cobra.Command{
	Use:   "kube",
	Short: "Use kubectl with your own RunOS identity",
	Long: `Use kubectl on a RunOS cluster as yourself, with the role RunOS gave you,
instead of with the cluster admin kubeconfig.

'runos kube access' adds a cluster to your kubeconfig. The entry holds no
secret: each time kubectl needs a credential it asks the RunOS CLI, which asks
the identity service for a short token (5 to 15 minutes) for your sign-in.`,
}

var kubeAccessCmd = &cobra.Command{
	Use:   "access",
	Short: "Add a cluster to your kubeconfig, signed in as yourself",
	Long: `Add a cluster, a user and a context to your kubeconfig. The user runs
'runos kube credential', so kubectl gets a short token for you from the RunOS
identity service each time it needs one. The kubeconfig holds no token.

Other entries in the file stay as they are. An entry with the same name is
replaced. The current context changes only if the file has none, or with
--use-context.

The command needs the cluster's API server address and the certificate
authority that signs its certificate. Both are public. Pass them with --server
and --ca-file.

You must be signed in with 'runos login' (a browser sign-in) when kubectl runs.
A personal access token or API key does not work for Kubernetes access.

Examples:
  runos kube access --cluster mycluster --server https://203.0.113.10:6443 --ca-file ca.crt
  kubectl --context runos-mycluster get namespaces`,
	Args: cobra.NoArgs,
	RunE: runKubeAccessCmd,
}

func init() {
	f := kubeAccessCmd.Flags()
	f.String("account", "", "Account id (default: the current account)")
	f.String("cluster", "", "Cluster id (default: the default cluster)")
	f.String("server", "", "API server address, an https URL (required)")
	f.String("ca-file", "", "PEM file with the certificate authority of the API server (required)")
	f.String("kubeconfig", "", "Kubeconfig file to update (default: the first path in $KUBECONFIG, else ~/.kube/config)")
	f.String("name", "", "Name of the cluster, user and context entries (default: runos-<cluster>)")
	f.Bool("use-context", false, "Make this the current context")
	f.String("command", "runos", "The RunOS CLI as kubectl should run it; use a full path if kubectl cannot find it")
	kubeCmd.AddCommand(kubeAccessCmd, kubeCredentialCmd)
	// Like 'access', the command is useless where sign-in is not through the identity service.
	cfg, err := config.LoadLocal() // no network at start-up
	kubeCmd.Hidden = err != nil || cfg.AuthIssuer == ""
}

// kubeAccessOptions are the inputs of 'runos kube access', after defaults.
type kubeAccessOptions struct {
	Account    string
	Cluster    string
	Server     string
	CAFile     string
	Kubeconfig string
	Name       string
	UseContext bool
	Command    string
}

func runKubeAccessCmd(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.AuthIssuer == "" {
		return errNoRoidEnvironment
	}
	str := func(n string) string { v, _ := cmd.Flags().GetString(n); return v }
	o := kubeAccessOptions{
		Account: str("account"), Cluster: str("cluster"), Server: str("server"), CAFile: str("ca-file"),
		Kubeconfig: str("kubeconfig"), Name: str("name"), Command: str("command"),
	}
	o.UseContext, _ = cmd.Flags().GetBool("use-context")
	if o.Account == "" {
		o.Account = cfg.GetAccountID()
	}
	if o.Cluster == "" {
		o.Cluster = cfg.GetDefaultClusterID()
	}
	return runKubeAccess(cmd.OutOrStdout(), o)
}

var errNoRoidEnvironment = errors.New("this environment does not sign in through the RunOS identity service, so Kubernetes access through RunOS is not available here")

// runKubeAccess validates the inputs, merges the entry into the kubeconfig
// and tells the person how to use it. It writes nothing when an input is bad.
func runKubeAccess(w io.Writer, o kubeAccessOptions) error {
	if o.Server == "" || o.CAFile == "" {
		return errors.New("pass --server (the API server's https address) and --ca-file (the PEM file of its certificate authority)")
	}
	ca, err := os.ReadFile(o.CAFile)
	if err != nil {
		return fmt.Errorf("cannot read the CA file: %w", err)
	}
	if o.Name == "" {
		o.Name = "runos-" + o.Cluster
	}
	e := kubeaccess.Entry{Name: o.Name, Server: o.Server, CAPEM: ca, Account: o.Account, Cluster: o.Cluster, Command: o.Command}
	if err := e.Validate(); err != nil {
		return err
	}
	path := o.Kubeconfig
	if path == "" {
		home, _ := os.UserHomeDir()
		if path, err = kubeaccess.DefaultPath(os.Getenv, home); err != nil {
			return err
		}
	}
	path = filepath.Clean(path)
	if err := kubeaccess.WriteFile(path, e, o.UseContext); err != nil {
		return err
	}
	fmt.Fprintf(w, "Added context %q to %s.\n", o.Name, path)
	fmt.Fprintf(w, "Sign in with 'runos login' if you have not, then try:\n  kubectl --context %s get namespaces\n", o.Name)
	return nil
}
