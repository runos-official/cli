package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	shellAID string
	shellCID string
)

var shellCmd = &cobra.Command{
	Use:   "shell [name] [-- command...]",
	Short: "Open a shell in your workspace on a cluster",
	Long: `Open a shell in your own workspace on a cluster, the same one the console's terminal shows.

Run it with no name to see which shells your workspace offers.

THE CONNECTION GOES STRAIGHT TO YOUR CLUSTER, not through the RunOS API. The API is asked for the
key and nothing else, so keystrokes take one hop rather than two. That means it needs the cluster's
own address to be reachable from here, which is the same thing the console's terminal needs.

WHAT YOU TYPE AFTER -- IS WHAT RUNS, argument for argument, the same as ` + "`kubectl exec --`" + `.
Your quoting is kept, so ` + "`-- grep \"error 500\" app.log`" + ` searches for the whole phrase.
Pipes, redirects and semicolons are shell syntax rather than arguments, so ask for a shell when you
want them: ` + "`-- bash -lc 'a | b'`" + `.

A one-shot returns the command's own exit code, so it can be used in a script. The one exception is
a command that ends the shell itself, such as a bare ` + "`exit`" + `, which returns 0.`,
	Example: `  runos shell                                     # which shells are available
  runos shell devops                              # open the cluster-tooling shell
  runos shell devops -- kubectl get nodes         # run one thing and exit
  runos shell devops -- bash -lc 'kubectl get po | wc -l'`,
	Args: cobra.ArbitraryArgs,
	RunE: runShell,
}

func init() {
	shellCmd.Flags().StringVar(&shellAID, "aid", "", "Account id (defaults to the configured account)")
	shellCmd.Flags().StringVar(&shellCID, "cid", "", "Cluster id (defaults to the configured cluster)")
	rootCmd.AddCommand(shellCmd)
}

// splitShellArgs separates the shell's name from an optional one-shot command.
//
// EVERYTHING AFTER `--` IS THE COMMAND. The name, if given, is the single argument before it.
// A second bare argument is refused rather than ignored, because silently dropping it would open
// an interactive shell when the caller asked for something specific.
func splitShellArgs(cmd *cobra.Command, args []string) (name string, command string, err error) {
	dash := cmd.ArgsLenAtDash()
	before := args
	if dash >= 0 {
		before = args[:dash]
		command = workspace.QuoteCommand(args[dash:])
	}
	switch len(before) {
	case 0:
	case 1:
		name = before[0]
	default:
		return "", "", fmt.Errorf(
			"runos shell takes one shell name. To run something in it, put the command after --, as in: runos shell %s -- %s",
			before[0], strings.Join(before[1:], " "))
	}
	return name, command, nil
}

func runShell(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	name, oneShot, err := splitShellArgs(cmd, args)
	if err != nil {
		return err
	}
	if name == "" {
		// No name is a REQUEST FOR THE LIST, not a default. Guessing here would mean that adding a
		// second kind of shell later silently changed what an existing command opens.
		fmt.Fprintln(cmd.OutOrStdout(), "Your workspace offers these shells:")
		for _, sh := range workspace.Offered {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-8s %s\n", sh.Name, sh.What)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nOpen one with: runos shell %s\n", workspace.Offered[0].Name)
		return nil
	}
	shell, err := workspace.ResolveShell(name)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return err
	}
	aid, cid, err := vmTarget(cfg, shellAID, shellCID)
	if err != nil {
		return err
	}

	// The workspace is named by the caller's own id, the same one the console uses, so both
	// surfaces open the SAME workspace rather than two that look alike.
	uid := auth.ExtractFirebaseUID(token)
	if uid == "" {
		return errors.New("runos shell needs a browser sign-in, because a workspace belongs to a person rather than to a key. Run 'runos login' and try again; an API key cannot open one")
	}

	// Read the width BEFORE connecting, so a one-shot can carry it and render correctly from its
	// first line rather than after a round trip.
	oneShotCols, oneShotRows := 0, 0
	if oneShot != "" && term.IsTerminal(int(os.Stdout.Fd())) {
		oneShotCols, oneShotRows, _ = term.GetSize(int(os.Stdout.Fd()))
	}

	client := api.NewClient(cfg.GetAPIURL())
	ws, err := resolveWorkspace(client, token, aid, cid, uid)
	if err != nil {
		return err
	}

	target := workspace.Target{
		Host:    ws.URL,
		Key:     ws.PSK,
		User:    shell.Name,
		Command: workspace.OneShot(oneShot, oneShotCols, oneShotRows),
	}
	dialURL, err := workspace.DialURL(target)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, resp, err := websocket.Dial(ctx, dialURL, &websocket.DialOptions{
		Subprotocols: workspace.Subprotocols(ws.PSK),
	})
	if err != nil {
		// The status is the whole diagnosis for the two commonest failures: 401 means the key is
		// stale, and anything DNS-shaped means this machine cannot see the cluster's own address.
		if resp != nil {
			return fmt.Errorf("your workspace refused the connection (HTTP %d)", resp.StatusCode)
		}
		return fmt.Errorf("could not reach your workspace at %s: %w", ws.URL, err)
	}
	defer conn.CloseNow()

	return pipeShell(ctx, conn, oneShot != "")
}

type workspaceAccess struct {
	PSK string `json:"psk"`
	URL string `json:"url"`
}

// resolveWorkspace gets the key for this caller's workspace, creating it if they have never opened
// one. The console creates on first use too, so a person who has only ever used the CLI is not
// told to go and click something in a browser first.
func resolveWorkspace(client *api.Client, token, aid, cid, uid string) (*workspaceAccess, error) {
	// A KEY IS NOT A RUNNING POD. The key lives in a Secret created by the same apply that creates
	// the deployment, so it answers the instant the manifest lands, minutes before anything serves.
	// Taking that as "ready" meant the wait below only ever ran on the very first invocation: press
	// ctrl-C during it, or create the workspace from the console, or have the pod restart, and
	// every later attempt dialled a starting pod and called it "refused".
	access, missing, err := readWorkspacePSK(client, token, aid, cid, uid)
	if err != nil && !missing {
		// A READ THAT FAILED IS NOT AN ABSENT WORKSPACE. Treating every failure as "you have none"
		// meant a network blip, an expired sign-in or a 500 announced "Setting up your workspace"
		// and posted a create for something that already exists.
		return nil, err
	}
	if err == nil {
		if up, _ := ready(client, token, aid, cid, uid); up {
			return access, nil
		}
		fmt.Fprintln(os.Stderr, "Your workspace is still starting.")
		return waitForWorkspace(client, token, aid, cid, uid)
	}

	fmt.Fprintln(os.Stderr, "Setting up your workspace on this cluster. The first one pulls a large image, so give it a few minutes.")
	created, createErr := client.Do("POST", fmt.Sprintf("/%s/%s/runostty/%s", aid, cid, uid), token, nil)
	if createErr != nil {
		return nil, createErr
	}
	if !created.OK() {
		if message := created.ErrorMessage(); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, fmt.Errorf("your workspace could not be created (HTTP %d)", created.StatusCode)
	}

	return waitForWorkspace(client, token, aid, cid, uid)
}

// waitForWorkspace blocks until the pod is serving, saying what it is waiting for.
//
// TEN MINUTES, because the first workspace on a cluster pulls a large image over whatever
// connection that cluster has. Measured on a home lab: serving in about four minutes, so the
// three-minute limit this used to carry gave up on a workspace that was fine.
func waitForWorkspace(client *api.Client, token, aid, cid, uid string) (*workspaceAccess, error) {
	deadline := time.Now().Add(10 * time.Minute)
	lastDetail := ""
	for time.Now().Before(deadline) {
		ok, detail := ready(client, token, aid, cid, uid)
		if ok {
			access, _, err := readWorkspacePSK(client, token, aid, cid, uid)
			return access, err
		}
		if detail != "" && detail != lastDetail {
			// Say what it is doing rather than printing dots. A pull that is going to fail says so
			// here, instead of looking identical to a slow one until the timeout.
			fmt.Fprintf(os.Stderr, "  %s\n", detail)
			lastDetail = detail
		}
		time.Sleep(3 * time.Second)
	}
	if lastDetail != "" {
		return nil, fmt.Errorf("your workspace is still not ready after ten minutes (%s)", lastDetail)
	}
	return nil, errors.New("your workspace is still not ready after ten minutes")
}

// readWorkspacePSK returns the key, and says whether the workspace is genuinely ABSENT as opposed
// to unreadable. Only an absence may lead to a create.
func readWorkspacePSK(client *api.Client, token, aid, cid, uid string) (*workspaceAccess, bool, error) {
	result, err := client.Do("GET", fmt.Sprintf("/%s/%s/runostty/%s/psk", aid, cid, uid), token, nil)
	if err != nil {
		return nil, false, err
	}
	if result.StatusCode == 404 {
		return nil, true, errors.New("no workspace on this cluster yet")
	}
	if !result.OK() {
		if message := result.ErrorMessage(); message != "" {
			return nil, false, fmt.Errorf("%s", message)
		}
		return nil, false, fmt.Errorf("could not read your workspace (HTTP %d)", result.StatusCode)
	}
	var access workspaceAccess
	if err := result.Decode(&access); err != nil {
		return nil, false, err
	}
	if access.PSK == "" || access.URL == "" {
		return nil, false, errors.New("the workspace returned no address or key")
	}
	return &access, false, nil
}

// ready asks whether the workspace is up.
//
// IT READS `state`, THE SAME FIELD THE CONSOLE READS. The first version of this invented a
// `ready` boolean that the API has never returned, so it was false forever and the wait could
// only ever time out. The endpoint answers a state plus a replica count; `healthy` is the one
// value that means the pod is serving.
func ready(client *api.Client, token, aid, cid, uid string) (bool, string) {
	result, err := client.Do("GET", fmt.Sprintf("/%s/%s/runostty/%s/status", aid, cid, uid), token, nil)
	if err != nil || !result.OK() {
		return false, ""
	}
	var status struct {
		State    string `json:"state"`
		Message  string `json:"message"`
		Replicas struct {
			Desired int `json:"desired"`
			Ready   int `json:"ready"`
		} `json:"replicas"`
	}
	if err := result.Decode(&status); err != nil {
		return false, ""
	}
	detail := status.State
	if status.Message != "" {
		detail = status.State + ": " + status.Message
	}
	return status.State == "healthy" && status.Replicas.Ready >= 1, detail
}

// pipeShell puts the local terminal into raw mode and moves bytes until either side finishes.
//
// RAW MODE IS WHAT MAKES IT A TERMINAL. Without it the local line discipline eats control keys, so
// ctrl-C kills this process instead of the command running in the shell, and an editor is unusable.
// It is restored on every exit path, including a panic, because leaving a terminal in raw mode
// leaves the user with a shell that does not echo.
func pipeShell(ctx context.Context, conn *websocket.Conn, oneShot bool) error {
	// TWO DIFFERENT QUESTIONS, and conflating them cost every redirected one-shot its width.
	// Raw mode is about the INPUT: can this terminal stop eating control keys. The size is about
	// the OUTPUT: how wide is the thing being drawn on. `runos shell -- kubectl get nodes -o wide
	// < /dev/null` from a 200-column terminal used to get 80, because the whole resize block was
	// gated on stdin being a terminal and the size was read from stdin too.
	inFd, outFd := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	canRaw := term.IsTerminal(inFd)
	canSize := term.IsTerminal(outFd)

	// A one-shot's output passes through a scanner that lifts the exit code off it. An interactive
	// session gets no wrapper and therefore no marker, so it writes straight through.
	var out io.Writer = os.Stdout
	var scanner *workspace.ExitScanner
	if oneShot {
		scanner = workspace.NewExitScanner(os.Stdout)
		out = scanner
	}

	// TAKE DELIVERY OF SIGPIPE, or `runos shell devops -- kubectl get pods -A | head -20` kills this
	// process the moment `head` exits, the deferred restore below never runs, and the caller is left
	// in a terminal with no echo, no line editing and a dead ctrl-C. MEASURED 2026-08-21: exit
	// status 141, no restore, pty still raw. Notified and never read, so the write simply returns
	// EPIPE and the normal ending path runs.
	pipeSig := make(chan os.Signal, 1)
	signal.Notify(pipeSig, syscall.SIGPIPE)
	defer signal.Stop(pipeSig)

	if canRaw {
		state, err := term.MakeRaw(inFd)
		if err != nil {
			return fmt.Errorf("could not put this terminal into raw mode: %w", err)
		}
		defer func() { _ = term.Restore(inFd, state) }()
	}

	if canSize {
		// Tell the far end the size now, and again whenever the window changes. Without the first
		// one the shell believes it is 80x24 whatever the window is, so anything full-screen draws
		// in the wrong place.
		sendSize(ctx, conn, outFd)
		stopWatching := watchWindowSize(ctx, func() { sendSize(ctx, conn, outFd) })
		defer stopWatching()
	}

	// The reader is what ends the session, never the writer. MEASURED 2026-08-21 against a live
	// workspace: with stdin closed (a pipe, a script, anything not a keyboard) the old code took
	// EOF on stdin as "we are done" and tore the connection down BEFORE a single byte of output
	// arrived. `runos shell -- whoami` printed nothing and exited 0. EOF on stdin means the INPUT
	// is finished; it says nothing about whether the far end has finished answering.
	done := make(chan error, 1)

	// Local to remote. Its own goroutine because a blocking read on stdin cannot be cancelled.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				// BINARY, and that is not a detail. MEASURED 2026-08-21: Node's websocket library
				// VALIDATES text frames and fails the connection with 1007 on the first byte that
				// is not valid UTF-8. A multi-byte character split across this 32 KiB read
				// boundary is enough, so any paste or pipe over 32 KiB containing one accented
				// character killed the session mid-command. The far end reads the message as a
				// Buffer either way, so binary costs nothing and removes the whole class.
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					done <- werr
					return
				}
			}
			if err != nil {
				// Tell the remote shell its input is over, the way a closing pipe tells a local
				// one. A PTY turns EOT into end-of-file, so a shell fed a script exits here
				// instead of waiting for a prompt nobody will answer. Then stop writing and let
				// the reader decide when the session is finished.
				_ = conn.Write(ctx, websocket.MessageText, []byte{0x04})
				return
			}
		}
	}()

	// Remote to local. This one owns the ending.
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			if _, werr := out.Write(data); werr != nil {
				done <- werr
				return
			}
		}
	}()

	err := <-done
	if scanner != nil {
		// Whatever was held back is ordinary output when no marker ever came. Losing it would
		// silently truncate the last line of every short command.
		_ = scanner.Flush()
	}
	if isNormalEnd(err) {
		if scanner != nil && scanner.Found && scanner.Code != 0 {
			// The command failed, so this must fail too, or a script cannot tell. The far end has
			// already printed whatever it wanted to say, so nothing is added here.
			os.Exit(scanner.Code)
		}
		return nil
	}
	// A STALE KEY DOES NOT FAIL THE DIAL. The far end accepts the upgrade and only then closes with
	// 4401, so the handshake succeeds and the refusal arrives here instead, where a raw library
	// string ("failed to get reader: received close frame") tells the user nothing.
	if websocket.CloseStatus(err) == 4401 {
		return errors.New("your workspace refused the key. It rotates regularly, so try again; if it keeps happening, open the terminal in the console once to reset it")
	}
	return err
}

func sendSize(ctx context.Context, conn *websocket.Conn, fd int) {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return
	}
	frame, ok := workspace.ResizeFrame(cols, rows)
	if !ok {
		return
	}
	_ = conn.Write(ctx, websocket.MessageText, frame)
}

// isNormalEnd says whether the session simply ended. Typing `exit` is not an error and must not
// print one, or every clean logout looks like a fault.
func isNormalEnd(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	status := websocket.CloseStatus(err)
	if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
		return true
	}
	// A CLOSE FRAME WITH NO STATUS IS STILL A CLEAN END, and it is the one this far end actually
	// sends. MEASURED 2026-08-21: typing exit, or a one-shot finishing, closes the socket from
	// Node with no code, which arrives here as StatusNoStatusRcvd. Without this every clean logout
	// printed "Error: failed to get reader: received close frame" and exited non-zero, which
	// teaches people to ignore the error line.
	if status == websocket.StatusNoStatusRcvd {
		return true
	}
	// A BROKEN PIPE IS AN ORDINARY ENDING: the caller piped this into something that has finished
	// reading, `| head` being the obvious case.
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	// AND NOTHING ELSE IS. This used to end with a substring match on "EOF", which was reasoning
	// rather than measurement: a TCP connection dropped with no close frame surfaces as "failed to
	// read frame header: EOF", so a broken session exited 0 and looked like a clean logout.
	// MEASURED 2026-08-21 against a server that drops the socket. The clean-logout case it was
	// meant to cover is already handled above by StatusNoStatusRcvd, and stdin reaching EOF no
	// longer ends the session at all, so it was catching nothing legitimate.
	return false
}
