package vpn

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// restartServiceHint names the command that restarts the VPN service, for the version-skew
// error below. One wrapped command on every OS (it maps to launchctl/systemctl/SCM inside
// `vpn restart`), so the error never leaks platform plumbing; reinstall is only the fallback.
func restartServiceHint() string {
	if runtime.GOOS == "windows" {
		return "  runos vpn restart   (from an elevated prompt)\nIf that fails, re-run `runos vpn install` the same way."
	}
	return "  sudo runos vpn restart\nIf that fails, re-run `sudo runos vpn install`."
}

// The control socket between the CLI (a person's process) and the daemon (root). One JSON request
// per line, one JSON response per line, then the connection closes. The socket file is group
// readable so the installing user's CLI can reach it without sudo; a stricter peer-credential
// check can be layered on later (the ops that matter server-side already require a session token
// the CLI must fetch through a sign-in).

// Serve listens on the socket and dispatches each connection to the daemon until ctx-style stop.
// It replaces a stale socket file left by a crashed daemon. The returned listener is closed by
// the caller to stop serving.
func Serve(d *Daemon, socketPath, socketGroup string, groupExplicit bool) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	// Remove a stale socket from a previous daemon; a fresh bind fails otherwise.
	if _, err := os.Stat(socketPath); err == nil {
		_ = os.Remove(socketPath)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	// The installing user's CLI must reach the socket without admin rights: on unix that is 0660
	// plus their group (found on real hardware: without the chown the socket is root:root and every
	// non-root `runos vpn` call is a permission error); on Windows it is an ACL grant to Users.
	if err := grantSocketAccess(socketPath, socketGroup, groupExplicit); err != nil {
		_ = listener.Close()
		return nil, err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener closed
			}
			go serveConn(d, conn)
		}
	}()
	return listener, nil
}

func serveConn(d *Daemon, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	resp := Response{}
	if err := json.Unmarshal(line, &req); err != nil {
		resp.Error = "malformed request: " + err.Error()
	} else {
		resp = d.Handle(req)
	}
	data, _ := json.Marshal(resp)
	_, _ = conn.Write(append(data, '\n'))
}

// Client talks to the daemon over the socket. It is what `cmd/vpn.go` uses.
type Client struct {
	socketPath string
}

// NewClient returns a socket client for the default (or given) path.
func NewClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = SocketPath
	}
	return &Client{socketPath: socketPath}
}

// Call sends one request and returns the daemon's response. A connection failure is reported as a
// distinct error so the CLI can say "the VPN service is not running; run 'runos vpn install'".
func (c *Client) Call(req Request) (*Response, error) {
	conn, err := net.DialTimeout("unix", c.socketPath, 3*time.Second)
	if err != nil {
		// PERMISSION DENIED IS THE OPPOSITE OF NOT RUNNING, and saying the wrong one sent two
		// people round a loop that could not end (see socket_permission_test.go). EACCES means
		// something IS listening and this user cannot reach it; reinstalling recreates the same
		// socket, so the ordinary advice is not merely unhelpful, it is a dead end.
		if errors.Is(err, fs.ErrPermission) {
			return nil, &PermissionError{Path: c.socketPath, Group: socketGroupName(c.socketPath), Err: err}
		}
		return nil, &NotRunningError{Path: c.socketPath, Err: err}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(120 * time.Second))

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if resp.Error != "" {
		// "unknown op" is the daemon telling us it predates this CLI: the service process was
		// started from an older build (the binary on disk may already be new, since the
		// LaunchDaemon/systemd unit runs the same file the CLI updates). Measured 2026-08-15:
		// a key revoke left `vpn up` failing with the bare `unknown op "rotate-key"`, which
		// names the internal protocol and no way forward. Say what actually fixes it.
		if strings.HasPrefix(resp.Error, "unknown op") {
			return &resp, fmt.Errorf(
				"the RunOS VPN service on this machine is running an older build than this CLI (%s).\n"+
					"Restart it to pick up the current binary:\n%s",
				resp.Error, restartServiceHint())
		}
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

// NotRunningError means the daemon socket could not be reached: the service is not installed or
// not running. The CLI turns this into an actionable message.
type NotRunningError struct {
	Path string
	Err  error
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("the RunOS VPN service is not running (%s). Run 'sudo runos vpn install' first.", e.Path)
}

func (e *NotRunningError) Unwrap() error { return e.Err }

/*
PermissionError means the control socket is there and this user may not open it.

Distinct from NotRunningError because the remedies have nothing in common: this one is fixed by the
socket's group, and reinstalling recreates the same socket. It carries the group read off the file
rather than a guess, because that is the fact the reader needs and the one a message written in
advance cannot know.
*/
type PermissionError struct {
	Path  string
	Group string
	Err   error
}

func (e *PermissionError) Error() string {
	group := strconv.Quote(e.Group)
	if e.Group == "" {
		group = "a group you are not in"
	}
	/*
	 THE REPAIR IS ONLY PROMISED WHERE IT HAPPENS.

	 The daemon heals exactly one case: a derived group that is GID 0, on darwin. Everywhere else,
	 and for every other group, it leaves the configured group alone. Telling a Linux user that a
	 restart repairs the group would send them round a restart that changes nothing, which is the
	 same shape as the defect this whole message exists to remove.
	*/
	if e.healable() {
		return fmt.Sprintf(
			"permission denied opening the RunOS VPN control socket (%s): it belongs to %s.\n"+
				"The service IS running, so this is not an install problem. The daemon repairs the "+
				"socket's group when it starts:\n  %s",
			e.Path, group, restartServiceHint())
	}
	return fmt.Sprintf(
		"permission denied opening the RunOS VPN control socket (%s): it belongs to %s.\n"+
			"The service IS running, so this is not an install problem. Hand the socket to a group "+
			"you are in:\n  sudo runos vpn install --socket-group <group>",
		e.Path, group)
}

// healable reports whether the daemon would actually put this socket right on its next start. Only
// a GID 0 group on darwin is healed; see usableSocketGroup.
func (e *PermissionError) healable() bool {
	if runtime.GOOS != "darwin" || e.Group == "" {
		return false
	}
	gid, ok := groupGID(e.Group)
	return ok && gid == rootGID
}

func (e *PermissionError) Unwrap() error { return e.Err }
