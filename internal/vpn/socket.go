package vpn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// The control socket between the CLI (a person's process) and the daemon (root). One JSON request
// per line, one JSON response per line, then the connection closes. The socket file is group
// readable so the installing user's CLI can reach it without sudo; a stricter peer-credential
// check can be layered on later (the ops that matter server-side already require a session token
// the CLI must fetch through a sign-in).

// Serve listens on the socket and dispatches each connection to the daemon until ctx-style stop.
// It replaces a stale socket file left by a crashed daemon. The returned listener is closed by
// the caller to stop serving.
func Serve(d *Daemon, socketPath, socketGroup string) (net.Listener, error) {
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
	if err := grantSocketAccess(socketPath, socketGroup); err != nil {
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
