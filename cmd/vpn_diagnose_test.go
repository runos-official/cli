package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestLogsAndDiagnoseNeedNoConfigBootstrap(t *testing.T) {
	// A stuck user may have no config and no network; the bootstrap would fetch one first.
	for _, name := range []string{"logs", "diagnose"} {
		if !isBootstrapFreeVPNCommand(name) {
			t.Errorf("vpn %s must not run the config bootstrap", name)
		}
	}
	for _, name := range []string{"up", "status", "down", "connect"} {
		if isBootstrapFreeVPNCommand(name) {
			t.Errorf("vpn %s needs the config", name)
		}
	}
}
