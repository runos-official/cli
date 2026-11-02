package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/kubeaccess"
	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

// kubeCredentialCmd is what kubectl runs for the kubeconfig that
// 'runos kube access' writes (an exec credential plugin). stdout carries the
// ExecCredential and nothing else; every message goes to stderr.
var kubeCredentialCmd = &cobra.Command{
	Use:   "credential",
	Short: "Print a short Kubernetes token for kubectl (run by kubectl, not by hand)",
	Long: `Print a client.authentication.k8s.io/v1 ExecCredential for one cluster. kubectl
runs this from the kubeconfig that 'runos kube access' writes.

It uses your 'runos login' browser sign-in. It keeps the token in memory for
this run only and writes nothing to disk.`,
	Hidden: true,
	Args:   cobra.NoArgs,
	// kubectl runs this before many commands and waits for it. The root
	// PersistentPreRunE fetches config and manifest on a first run and may print an
	// update notice, none of which belongs here. Cobra runs only the closest
	// PersistentPreRunE, so an empty one replaces the root's for this command.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE:              runKubeCredential,
	SilenceErrors:     true, // the message below is the whole error output
}

func init() {
	kubeCredentialCmd.Flags().String("account", "", "Account id (default: the current account)")
	kubeCredentialCmd.Flags().String("cluster", "", "Cluster id")
}

// isKubeCredentialRun reports whether argv is kubectl asking for a token.
// Start-up then skips the manifest load, which can reach the network.
func isKubeCredentialRun(argv []string) bool {
	return len(argv) >= 3 && argv[1] == "kube" && argv[2] == "credential"
}

func runKubeCredential(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	account, _ := cmd.Flags().GetString("account")
	cluster, _ := cmd.Flags().GetString("cluster")
	cfg, err := config.LoadLocal() // never reach the network before the token call
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "You are not signed in to RunOS. Run 'runos login', then try again.")
		return errors.New("no credential")
	}
	f, err := credentialFetcher(cfg, account, cluster)
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
		return errors.New("no credential")
	}
	src := &kubeaccess.Source{Fetcher: f, Now: time.Now}
	return emitCredential(cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Getenv("KUBERNETES_EXEC_INFO"), src)
}

// emitCredential writes the ExecCredential to out, or one plain line to errw
// and a non-nil error (exit 1) when it cannot.
func emitCredential(out, errw io.Writer, execInfo string, src *kubeaccess.Source) error {
	if err := kubeaccess.CheckExecInfo(execInfo); err != nil {
		fmt.Fprintln(errw, err.Error())
		return err
	}
	iss, err := src.Token()
	if err != nil {
		fmt.Fprintln(errw, kubeaccess.PlainMessage(err))
		return err
	}
	return kubeaccess.WriteCredential(out, iss)
}

// credentialFetcher builds the token call from the stored sign-in. Kubernetes
// access needs a person who signed in through the identity service with a
// browser: a personal access token or API key has no person behind it, so
// roid refuses it. A person with both is served from the sign-in.
func credentialFetcher(cfg *config.Config, account, cluster string) (*kubeaccess.Fetcher, error) {
	// The token route is a rapi route: roid answers it only behind the API host, never on the
	// sign-in host (roidapi.GatewayURL). The proof hosts of the session hold both hosts.
	base, err := roidapi.GatewayURL(cfg)
	if err != nil {
		return nil, errNoRoidEnvironment
	}
	if !cfg.HasRoidSignIn() {
		if auth.Kind(cfg).IsPAT() {
			return nil, errors.New("Kubernetes access needs a signed-in person. A personal access token or API key does not work. Run 'runos login' and sign in with your browser.")
		}
		return nil, errors.New("You are not signed in to RunOS. Run 'runos login', then try again.")
	}
	if account == "" {
		account = cfg.GetAccountID()
	}
	if account == "" {
		return nil, errors.New("No account selected: pass --account or run 'runos account switch'.")
	}
	if cluster == "" {
		return nil, errors.New("No cluster given: pass --cluster. 'runos kube access' writes it into the kubeconfig.")
	}
	bearer := func() (string, error) {
		s, err := auth.RoidSession(cfg)
		if err != nil {
			return "", err
		}
		return s.Bearer()
	}
	return &kubeaccess.Fetcher{BaseURL: base, Account: account, Cluster: cluster, Bearer: bearer}, nil
}
