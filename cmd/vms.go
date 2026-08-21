package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vmconsole"

	"github.com/spf13/cobra"
)

// vmsCmd is a static parent so `ssh` and `proxy` can live beside the manifest-driven verbs.
// The dynamic builder merges its own `vms` children onto this one rather than making a second.
var vmsCmd = &cobra.Command{
	Use:   "vms",
	Short: "Manage virtual machines",
	Long:  `Manage RunOS virtual machines: create, inspect, power, and connect to them.`,
}

var (
	vmsSSHAID      string
	vmsSSHCID      string
	vmsProxyAID    string
	vmsProxyCID    string
	vmsProxyAPIURL string
	vmsProxyNoTLS  bool
)

var vmsSSHCmd = &cobra.Command{
	Use:   "ssh <vmid> [command...]",
	Short: "Open an SSH session on a virtual machine",
	Long: `Open an interactive SSH session on a virtual machine, or run one command on it.

RunOS never gives a VM a public address. This tunnels to the guest's SSH port through the
RunOS API, using the SSH key RunOS manages for that VM, so it works from anywhere the API is
reachable and needs nothing configured on the guest.

The key is fetched for this session, written to a private temporary file, and deleted when the
session ends. It is never added to your agent and never written to your SSH config.

Because the tunnel is an ssh ProxyCommand, scp and rsync work the same way:

  scp -o ProxyCommand="runos vms proxy myvm" file runos-admin@vm:/tmp/

Examples:
  runos vms ssh myvm                  # interactive shell
  runos vms ssh myvm uptime           # run one command
  runos vms ssh myvm -- df -h /       # anything after -- goes to the guest verbatim`,
	Args: cobra.MinimumNArgs(1),
	RunE: runVmsSSH,
}

// vmsProxyCmd is the transport half, and is hidden because it is not a thing to run by hand:
// it speaks a byte stream on stdin and stdout, so a human who runs it sees a terminal fill with
// SSH protocol. It is public in the sense that scp and rsync can be pointed at it.
var vmsProxyCmd = &cobra.Command{
	Use:    "proxy <vmid>",
	Short:  "Bridge stdin and stdout to a VM's SSH port (for ssh ProxyCommand)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runVmsProxy,
}

func init() {
	vmsSSHCmd.Flags().StringVar(&vmsSSHAID, "aid", "", "Account id (defaults to the configured account)")
	vmsSSHCmd.Flags().StringVar(&vmsSSHCID, "cid", "", "Cluster id (defaults to the configured cluster)")
	vmsProxyCmd.Flags().StringVar(&vmsProxyAID, "aid", "", "Account id")
	vmsProxyCmd.Flags().StringVar(&vmsProxyCID, "cid", "", "Cluster id")
	vmsProxyCmd.Flags().StringVar(&vmsProxyAPIURL, "api-url", "", "API base URL")
	vmsProxyCmd.Flags().BoolVar(&vmsProxyNoTLS, "insecure", false, "Allow a plaintext or self-signed endpoint")
	vmsCmd.AddCommand(vmsSSHCmd, vmsProxyCmd)
}

// vmTarget resolves which VM on which cluster, preferring explicit flags over configuration.
func vmTarget(cfg *config.Config, aidFlag, cidFlag string) (aid, cid string, err error) {
	aid = aidFlag
	if aid == "" {
		aid = cfg.GetAccountID()
	}
	cid = cidFlag
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if aid == "" {
		return "", "", errors.New("no account: run 'runos login', or pass --aid")
	}
	if cid == "" {
		return "", "", errors.New("no cluster: set one with 'runos clusters default <cid>', or pass --cid")
	}
	return aid, cid, nil
}

func runVmsProxy(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return err
	}
	aid, cid, err := vmTarget(cfg, vmsProxyAID, vmsProxyCID)
	if err != nil {
		return err
	}

	apiURL := vmsProxyAPIURL
	if apiURL == "" {
		apiURL = cfg.GetAPIURL()
	}

	// Minted here rather than passed in from `ssh`. A ticket is single use and lives about a
	// minute, so one handed down a command line would already be spent or expired, and ssh may
	// start this process more than once.
	ticket, err := vmconsole.MintTicket(api.NewClient(apiURL), token, aid, cid, args[0], vmconsole.KindSSH)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := vmconsole.Dial(ctx, ticket)
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	// stdin and stdout are the SSH transport itself here, so nothing else may print to them.
	// Every message this command produces goes to stderr, which ssh passes through.
	//
	// Refusal translates the gate's close codes. Without it a refused session reaches ssh as
	// "failed to get reader: received close frame", which ssh reports as a broken pipe, and the
	// person sees neither what was refused nor what to do about it.
	return vmconsole.Refusal(vmconsole.Pipe(ctx, conn, os.Stdin, os.Stdout))
}

func runVmsSSH(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return err
	}
	aid, cid, err := vmTarget(cfg, vmsSSHAID, vmsSSHCID)
	if err != nil {
		return err
	}
	vmid := args[0]

	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("no ssh client found on this machine; install OpenSSH and try again")
	}

	key, err := fetchVMKey(api.NewClient(cfg.GetAPIURL()), token, aid, cid, vmid)
	if err != nil {
		return err
	}
	keyPath, cleanup, err := writePrivateKey(vmid, key.PrivateKey)
	if err != nil {
		return err
	}
	defer cleanup()

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate this executable to tunnel through: %w", err)
	}

	sshArgs := vmconsole.BuildSSHArgs(vmconsole.SSHRequest{
		Self:    self,
		VMID:    vmid,
		User:    key.Username,
		KeyPath: keyPath,
		AID:     aid,
		CID:     cid,
		APIURL:  cfg.GetAPIURL(),
	}, args[1:])

	ssh := exec.Command("ssh", sshArgs...)
	ssh.Stdin, ssh.Stdout, ssh.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ssh.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// ssh has already printed its own diagnosis to stderr, so adding wording here would
			// only bury it. Exit with its code so a script sees what it would have seen.
			os.Exit(exit.ExitCode())
		}
		return err
	}
	return nil
}

type vmKey struct {
	Username   string `json:"username"`
	PrivateKey string `json:"privateKey"`
}

func fetchVMKey(client *api.Client, token, aid, cid, vmid string) (*vmKey, error) {
	result, err := client.Do("GET", fmt.Sprintf("/%s/%s/vms/%s/ssh-key", aid, cid, vmid), token, nil)
	if err != nil {
		return nil, err
	}
	if !result.OK() {
		if message := result.ErrorMessage(); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, fmt.Errorf("could not read the SSH key for %s (HTTP %d)", vmid, result.StatusCode)
	}
	var key vmKey
	if err := result.Decode(&key); err != nil {
		return nil, err
	}
	if key.PrivateKey == "" {
		return nil, fmt.Errorf("%s has no SSH key RunOS manages", vmid)
	}
	return &key, nil
}

// writePrivateKey puts the key somewhere ssh will accept it from, and takes it away afterwards.
//
// ssh REFUSES a key file other users can read, so the mode is not decoration. The file is
// created in a private temporary directory rather than the working directory, so a session
// interrupted before the cleanup runs leaves the key somewhere the OS clears rather than in a
// repository someone might commit.
func writePrivateKey(vmid, key string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "runos-vm-"+vmid+"-")
	if err != nil {
		return "", nil, fmt.Errorf("could not create a place for the key: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	path = filepath.Join(dir, "id")
	contents := key
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		// OpenSSH rejects a key whose final line has no newline.
		contents += "\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("could not write the key: %w", err)
	}
	return path, cleanup, nil
}
