package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

/*
`runos vpn diagnose` has to work for exactly the person who cannot connect: no daemon, maybe not
signed in, no root. That is the whole point of it, so the tests run it in that condition.
*/

func runDiagnose(t *testing.T, socketPath string, extraArgs ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	old := vpnLogPathsForRead
	vpnLogPathsForRead = func() []string { return []string{filepath.Join(dir, "absent.log")} }
	t.Cleanup(func() { vpnLogPathsForRead = old })

	var out strings.Builder
	cmd := &cobra.Command{Use: "diagnose", RunE: runVPNDiagnose}
	cmd.Flags().BoolP("json", "j", false, "")
	cmd.Flags().Int("lines", 40, "")
	cmd.Flags().String("socket", "", "")
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs(append([]string{"--socket", socketPath}, extraArgs...))
	err := cmd.Execute()
	return out.String(), err
}

func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	return home
}

func TestDiagnoseWorksWithNoDaemonNoSignInAndNoConfig(t *testing.T) {
	isolatedHome(t)
	out, err := runDiagnose(t, filepath.Join(t.TempDir(), "no.sock"))
	if err != nil {
		t.Fatalf("a diagnostic that fails when things are broken is useless: %v\n%s", err, out)
	}
	for _, want := range []string{"signed in: no", "not-running", "RunOS VPN report"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestDiagnoseJSONIsOneParseableDocument(t *testing.T) {
	isolatedHome(t)
	out, err := runDiagnose(t, filepath.Join(t.TempDir(), "no.sock"), "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	if doc["schemaVersion"] == nil || doc["daemon"] == nil || doc["findings"] == nil {
		t.Errorf("missing fields: %v", doc)
	}
}

func TestDiagnoseReadsTheLastUpAttemptFromTheUsersHome(t *testing.T) {
	home := isolatedHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".runos"), 0o700); err != nil {
		t.Fatal(err)
	}
	trace := "2026-10-01T11:59:00Z vpn-cli: step=enrol status=failed HTTP 500\n"
	if err := os.WriteFile(filepath.Join(home, ".runos", "vpn-up.log"), []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runDiagnose(t, filepath.Join(t.TempDir(), "no.sock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"LAST FAILED STEP: enrol", "HTTP 500", "register this machine"} {
		if !strings.Contains(out, want) {
			t.Errorf("the failed step from the last `up` is not explained (%q missing):\n%s", want, out)
		}
	}
}

func TestDiagnoseAgainstARunningDaemonCarriesItsStatus(t *testing.T) {
	isolatedHome(t)
	daemon := newFakeDaemon(t)
	out, err := runDiagnose(t, daemon.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "control socket: reachable") {
		t.Errorf("a reachable daemon must read as reachable:\n%s", out)
	}
}

// countingTransport stands in for the network: it fails every request and counts them, so a test
// can say "no network call was made" instead of hoping the machine happens to be offline.
type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("test: the network is not available")
}

func noNetwork(t *testing.T) *countingTransport {
	t.Helper()
	ct := &countingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = ct
	t.Cleanup(func() { http.DefaultTransport = old })
	return ct
}

func TestDiagnoseMakesNoNetworkCallEvenWithAnAPIKeyAndNoConfig(t *testing.T) {
	// config.Load() fetches the default environment from a CDN, with a 10 second deadline, when
	// RUNOS_API_KEY is set and there is no config file. A diagnostic must never wait on that.
	isolatedHome(t)
	t.Setenv("RUNOS_API_KEY", "pat-for-test-only")
	ct := noNetwork(t)
	out, err := runDiagnose(t, filepath.Join(t.TempDir(), "no.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if n := ct.calls.Load(); n != 0 {
		t.Errorf("diagnose made %d network call(s); it promises none", n)
	}
	if !strings.Contains(out, "signed in: yes") {
		t.Errorf("a PAT in the environment still counts as signed in:\n%s", out)
	}
	if strings.Contains(out, "config error") {
		t.Errorf("a missing config file is normal for a PAT caller, not an error:\n%s", out)
	}
}

// The bootstrap in the root command's PersistentPreRunE fetches a config over the network when none
// exists. This drives that real hook for the real commands, not the helper that decides.
func TestLogsAndDiagnoseSkipTheRootBootstrapAndUpDoesNot(t *testing.T) {
	isolatedHome(t)
	t.Setenv("RUNOS_API_KEY", "pat-for-test-only")
	// isolatedHome blanks the account id, and a set-but-empty auth variable is refused by the hook
	// before it reaches the bootstrap.
	t.Setenv("RUNOS_ACCOUNT_ID", "acct-for-test")
	ct := noNetwork(t)

	for _, name := range []string{"logs", "diagnose"} {
		c, _, err := rootCmd.Find([]string{"vpn", name})
		if err != nil || c.Name() != name {
			t.Fatalf("vpn %s not found: %v", name, err)
		}
		if err := rootCmd.PersistentPreRunE(c, nil); err != nil {
			t.Errorf("vpn %s ran the config bootstrap and failed: %v", name, err)
		}
	}
	if n := ct.calls.Load(); n != 0 {
		t.Fatalf("vpn logs and diagnose made %d network call(s) before running", n)
	}

	// The control: a command that needs the config does bootstrap, so the counter is meaningful.
	up, _, err := rootCmd.Find([]string{"vpn", "up"})
	if err != nil {
		t.Fatal(err)
	}
	_ = rootCmd.PersistentPreRunE(up, nil)
	if ct.calls.Load() == 0 {
		t.Error("vpn up did not bootstrap: this test cannot tell a skipped bootstrap from a broken hook")
	}
}
