package vmconsole

import (
	"fmt"
	"strings"
)

// ProxyHost is the name ssh is pointed at.
//
// It is a placeholder, not a destination: the ProxyCommand supplies the actual transport, so
// ssh never resolves or dials anything. A VM has no address a client could use anyway, which is
// the whole reason this exists.
const ProxyHost = "vm"

// SSHRequest is everything needed to build an ssh invocation for a VM.
type SSHRequest struct {
	// Self is the path to this executable, used as the ProxyCommand.
	Self string
	VMID string
	// User is the platform account RunOS manages a key for, not the image's default login.
	User    string
	KeyPath string
	AID     string
	CID     string
	APIURL  string
	// Insecure is passed through to the proxy so a session against a local or self-signed
	// endpoint behaves the same as the command that opened it.
	Insecure bool
}

// BuildSSHArgs assembles the argv for ssh, with an optional remote command.
//
// A PROXYCOMMAND rather than a local listening port, and the difference matters. A port needs
// one chosen (so it can collide), needs cleaning up if the process dies, and is open to
// everything else on the machine for as long as it exists. A ProxyCommand has none of that: ssh
// spawns it, talks to it over a pipe, and it dies with the session. It also means `scp`, `sftp`
// and `rsync` work through exactly the same route with no extra machinery.
//
// Host key checking is off and known_hosts points at /dev/null, deliberately. Every VM answers
// as the same placeholder host through its own proxy, so recording keys would file one VM's key
// under the next VM's name and start printing a man-in-the-middle warning for ordinary use. The
// transport is already authenticated end to end: a single-use ticket, then conductor's mTLS to
// the cluster, so the host key would be confirming something already established.
func BuildSSHArgs(req SSHRequest, command []string) []string {
	args := []string{
		"-o", "ProxyCommand=" + proxyCommand(req),
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		// The banner about an unknown host is noise here for the same reason checking is off.
		"-o", "LogLevel=ERROR",
		"-i", req.KeyPath,
		req.User + "@" + ProxyHost,
	}
	return append(args, command...)
}

// proxyCommand builds the shell line ssh runs to get a byte stream to the guest.
//
// ssh hands this to /bin/sh, so every value that could contain a space is single-quoted. None of
// them can today (they are RunOS ids and a URL), which is exactly why it is worth doing now:
// the first one that can will not announce itself.
func proxyCommand(req SSHRequest) string {
	parts := []string{
		shellQuote(req.Self), "vms", "proxy", shellQuote(req.VMID),
		"--aid", shellQuote(req.AID),
		"--cid", shellQuote(req.CID),
		"--api-url", shellQuote(req.APIURL),
	}
	if req.Insecure {
		parts = append(parts, "--insecure")
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps a value so /bin/sh reads it as one word, whatever is in it.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// DescribeSSHFailure turns ssh's exit into something that names the likely cause.
//
// ssh exits 255 for every one of its own failures, so the code alone says nothing. The two that
// matter here are worth separating because the remedies are opposite: a guest that refused the
// key is a VM-side problem, and a proxy that never connected is a platform-side one.
func DescribeSSHFailure(exitCode int, stderr string) string {
	switch {
	case strings.Contains(stderr, "Permission denied"):
		return "The guest refused the platform key. Rotate it with `runos vms rotate-ssh-key`, " +
			"or check that the runos-admin account still exists inside the VM."
	case strings.Contains(stderr, "not valid") || strings.Contains(stderr, "ticket"):
		return "The session ticket was refused. Tickets are single use and expire in about a " +
			"minute, so this usually means one was reused."
	case exitCode == 255:
		return "ssh could not open the session. " + strings.TrimSpace(stderr)
	default:
		return fmt.Sprintf("The remote command exited %d.", exitCode)
	}
}
