package vmconsole

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildSSHArgs(t *testing.T) {
	base := SSHRequest{
		Self:     "/usr/local/bin/runos",
		VMID:     "vm001",
		User:     "runos-admin",
		KeyPath:  "/tmp/k",
		AID:      "acct1",
		CID:      "clus1",
		APIURL:   "https://api.example.com",
		Insecure: false,
	}

	t.Run("tunnels through this CLI rather than a local port", func(t *testing.T) {
		// A ProxyCommand needs no listener, so there is no port to collide, nothing to clean up
		// if the process dies, and no window where a local port is open to anything on the box.
		args := BuildSSHArgs(base, nil)
		proxy := optionValue(t, args, "ProxyCommand")
		// Quoted, because the whole line is handed to /bin/sh. A separate case pins that.
		if !strings.Contains(proxy, "'/usr/local/bin/runos'") || !strings.Contains(proxy, "vms proxy 'vm001'") {
			t.Fatalf("ProxyCommand = %q, want it to invoke this CLI's proxy for the VM", proxy)
		}
	})

	t.Run("passes the account and cluster the ticket was minted against", func(t *testing.T) {
		// Otherwise the proxy re-reads whatever the config happens to say, and a session opened
		// against one cluster could tunnel to a VM on another.
		proxy := optionValue(t, BuildSSHArgs(base, nil), "ProxyCommand")
		for _, want := range []string{"--aid 'acct1'", "--cid 'clus1'"} {
			if !strings.Contains(proxy, want) {
				t.Fatalf("ProxyCommand = %q, want it to carry %q", proxy, want)
			}
		}
	})

	t.Run("uses the platform key and nothing else", func(t *testing.T) {
		args := BuildSSHArgs(base, nil)
		if got := optionValue(t, args, "IdentitiesOnly"); got != "yes" {
			t.Fatalf("IdentitiesOnly = %q, want yes", got)
		}
		if !slices.Contains(args, "/tmp/k") {
			t.Fatalf("args = %v, want the key path", args)
		}
	})

	t.Run("keeps the guest out of the caller's known_hosts", func(t *testing.T) {
		// Every VM answers on 127.0.0.1 through a proxy, so recording host keys would collide
		// one VM's key with the next and start printing a MITM warning for ordinary use.
		if got := optionValue(t, BuildSSHArgs(base, nil), "UserKnownHostsFile"); got != "/dev/null" {
			t.Fatalf("UserKnownHostsFile = %q, want /dev/null", got)
		}
		if got := optionValue(t, BuildSSHArgs(base, nil), "StrictHostKeyChecking"); got != "no" {
			t.Fatalf("StrictHostKeyChecking = %q, want no", got)
		}
	})

	t.Run("connects as the platform account at the proxied host", func(t *testing.T) {
		args := BuildSSHArgs(base, nil)
		if !slices.Contains(args, "runos-admin@"+ProxyHost) {
			t.Fatalf("args = %v, want runos-admin@%s", args, ProxyHost)
		}
	})

	t.Run("appends a command after the destination, where ssh expects it", func(t *testing.T) {
		args := BuildSSHArgs(base, []string{"uptime", "-p"})
		dest := slices.Index(args, "runos-admin@"+ProxyHost)
		if dest < 0 || dest != len(args)-3 {
			t.Fatalf("args = %v, want the destination immediately before the command", args)
		}
		if args[len(args)-2] != "uptime" || args[len(args)-1] != "-p" {
			t.Fatalf("args = %v, want the command last and unquoted", args)
		}
	})

	t.Run("asks for no command when none was given, so the session is interactive", func(t *testing.T) {
		args := BuildSSHArgs(base, nil)
		if args[len(args)-1] != "runos-admin@"+ProxyHost {
			t.Fatalf("args = %v, want the destination last", args)
		}
	})

	t.Run("never puts the api url in a place a shell would re-split it", func(t *testing.T) {
		// The ProxyCommand is handed to /bin/sh by ssh, so anything in it that is not quoted is
		// word-split. A URL with no spaces survives today; quoting means it still survives if
		// one ever appears.
		proxy := optionValue(t, BuildSSHArgs(base, nil), "ProxyCommand")
		if !strings.Contains(proxy, "'https://api.example.com'") {
			t.Fatalf("ProxyCommand = %q, want the api url single-quoted", proxy)
		}
	})

	t.Run("carries the insecure flag only when it was asked for", func(t *testing.T) {
		if strings.Contains(optionValue(t, BuildSSHArgs(base, nil), "ProxyCommand"), "--insecure") {
			t.Fatal("did not ask for --insecure, but the proxy command carries it")
		}
		insecure := base
		insecure.Insecure = true
		if !strings.Contains(optionValue(t, BuildSSHArgs(insecure, nil), "ProxyCommand"), "--insecure") {
			t.Fatal("asked for --insecure, but the proxy command does not carry it")
		}
	})
}

// optionValue returns the value of an `-o Name=Value` pair.
func optionValue(t *testing.T, args []string, name string) string {
	t.Helper()
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && strings.HasPrefix(args[i+1], name+"=") {
			return strings.TrimPrefix(args[i+1], name+"=")
		}
	}
	t.Fatalf("args = %v, want an -o %s=... pair", args, name)
	return ""
}
