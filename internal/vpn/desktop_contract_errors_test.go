package vpn

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/desktopcontract"
)

/*
When a command fails, RunOS Desktop shows what it wrote to stderr. So the daemon-facing errors and
the private DNS sentences must be plain: no colour codes, no control characters, no stray whitespace,
and short (or wrapped under 120 characters a line). Desktop also decides "the VPN service is not
installed" by looking for the socket path or the words "vpn install" in that text, so the one error
that means exactly that must keep both.
*/

func TestDesktopContractDaemonFacingErrorsAreFitForADesktopMenu(t *testing.T) {
	const path = "/var/run/runos-vpn.sock"
	texts := map[string]string{
		"not running":                 (&NotRunningError{Path: path}).Error(),
		"permission denied":           (&PermissionError{Path: path, Group: "staff"}).Error(),
		"permission denied, no group": (&PermissionError{Path: path}).Error(),
		"older daemon":                "the RunOS VPN service on this machine is running an older build than this CLI (unknown op \"x\").\nRestart it to pick up the current binary:\n" + restartServiceHint(),
		"dns pending":                 dnsPendingText,
		"dns notice":                  DNSPendingNotice,
		"dns gave up":                 dnsGaveUpText,
		"dns down":                    dnsDownText,
		"dns not set up":              dnsNotSetUpText,
		"dns write":                   dnsWriteText,
		"dns remove":                  dnsRemoveText,
		"dns hand-authored":           dnsHandAuthoredText,
		"dns unreadable":              dnsUnreadableText,
	}
	for name, text := range texts {
		for _, problem := range desktopcontract.PlainErrorProblems(text) {
			t.Errorf("%s: %s\n%q", name, problem, text)
		}
	}
}

func TestDesktopContractNotRunningErrorKeepsTheWordsDesktopMatchesOn(t *testing.T) {
	text := (&NotRunningError{Path: SocketPath}).Error()
	if !strings.Contains(text, SocketPath) || !strings.Contains(strings.ToLower(text), "vpn install") {
		t.Errorf("Desktop recognises a missing service by the socket path and \"vpn install\", got %q", text)
	}
}

// The daemon's reply over the real socket, with the real Response encoding in the middle: what a
// `runos vpn status --json` caller receives after the CLI decodes it and prints it again.
func TestDesktopContractSocketStatusReplyMatchesTheDesktopContract(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{stateDir: dir, version: "1.22.2", state: state, platform: newPlatform(), pollInterval: PollInterval,
		doc: &Document{Clusters: []DocumentCluster{{CID: "c1", Name: "one"}}}}
	sock := shortSocketPath(t, "runos-vpn-contract.sock")
	listener, err := Serve(d, sock, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	resp, err := NewClient(sock).Call(Request{Op: OpStatus})
	if err != nil {
		t.Fatal(err)
	}
	out := encodeStatus(t, resp.Status)
	desktopcontract.Check(t, "status over the socket", out, desktopcontract.VPNStatus)
	if resp.Status.Version != "1.22.2" || len(resp.Status.Clusters) != 1 {
		t.Errorf("the reply lost facts on the way: %+v", resp.Status)
	}
}
