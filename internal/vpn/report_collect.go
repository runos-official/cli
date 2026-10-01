package vpn

import "errors"

// ProbeDaemon asks the daemon for its status and classifies the outcome, so the report can say why
// it has no status instead of failing. A person who cannot reach the daemon is the person who most
// needs this report.
func ProbeDaemon(c *Client) (SocketProbe, *Status) {
	resp, err := c.Call(Request{Op: OpStatus})
	if err == nil {
		return SocketProbe{State: "reachable"}, resp.Status
	}
	var perm *PermissionError
	var down *NotRunningError
	switch {
	case errors.As(err, &perm):
		detail := "owned by group " + perm.Group
		if perm.Group == "" {
			detail = "owned by a group this user is not in"
		}
		return SocketProbe{State: "permission-denied", Detail: detail}, nil
	case errors.As(err, &down):
		return SocketProbe{State: "not-running", Detail: down.Path}, nil
	default:
		return SocketProbe{State: "error", Detail: err.Error()}, nil
	}
}
