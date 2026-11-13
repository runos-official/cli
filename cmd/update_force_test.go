package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/update"
	"github.com/runos-official/cli/version"
	"github.com/spf13/cobra"
)

/*
`runos update --force`, against a fake conductor (the latest version) and a fake release server (the
archive and its checksums). Nothing here touches the network, the installed binary or ~/.runos: the
"binary" that gets replaced is a scratch file.
*/

const (
	oldBinary = "OLD-BINARY"
	newBinary = "NEW-BINARY-FROM-THE-RELEASE"
)

type forceHarness struct {
	exe       string
	progress  *bytes.Buffer
	errOut    *bytes.Buffer
	mu        sync.Mutex
	downloads int
	// said is what the progress writer held when the first download request arrived.
	said string
}

// newForceHarness stands up both servers and a config pointing at them. latest is what the fake
// conductor reports; the release server serves an archive for latest, with a checksum that is wrong
// when tamper is true.
func newForceHarness(t *testing.T, running, latest string, tamper bool) *forceHarness {
	t.Helper()
	h := &forceHarness{progress: &bytes.Buffer{}, errOut: &bytes.Buffer{}}

	conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"key":"CLI_VERSION","value":%q}`, latest)
	}))
	t.Cleanup(conductor.Close)

	binaryName := fmt.Sprintf("runos-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarGzWithRunos(t, newBinary)
	sum := sha256.Sum256(archive)
	hash := hex.EncodeToString(sum[:])
	if tamper {
		hash = strings.Repeat("0", 64)
	}
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", hash, binaryName)
		case strings.HasSuffix(r.URL.Path, "/"+binaryName):
			h.mu.Lock()
			h.downloads++
			if h.said == "" {
				h.said = h.progress.String()
			}
			h.mu.Unlock()
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(release.Close)

	dir := t.TempDir()
	h.exe = filepath.Join(dir, "runos")
	if err := os.WriteFile(h.exe, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	update.SetReleaseForTest(t, release.URL, h.exe)

	isolateConfigEnv(t)
	cfg := &config.Config{ConductorURL: conductor.URL, AccountID: "aaaaa", APIKey: "pat"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	previous := version.Version
	version.Version = running
	t.Cleanup(func() { version.Version = previous })
	return h
}

func tarGzWithRunos(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "runos", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func (h *forceHarness) binary(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// run is `runos update` for the CLI component, with the flags given. It returns the result document.
func (h *forceHarness) run(t *testing.T, flags ...string) (combinedUpdateResult, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().BoolP("check", "c", false, "")
	cmd.Flags().BoolP("json", "j", false, "")
	cmd.Flags().Bool("force", false, "")
	registerSocketFlag(cmd)
	cmd.SetErr(h.errOut)
	if err := cmd.ParseFlags(append(flags, "--socket", "/nonexistent/runos-test.sock")); err != nil {
		t.Fatal(err)
	}
	checkOnly, _ := cmd.Flags().GetBool("check")
	force, _ := cmd.Flags().GetBool("force")
	updater, err := update.NewUpdater()
	if err != nil {
		t.Fatal(err)
	}
	updater.SetProgress(h.progress)
	current := updater.CurrentVersion()
	result := combinedUpdateResult{SchemaVersion: 1, CLI: cliUpdateResult{updateComponentResult: updateComponentResult{Version: current, CurrentVersion: current}}}
	err = updateCLIComponent(cmd, updater, checkOnly, force, h.progress, &result)
	return result, err
}

const devBuild = "dev-2026-11-13T09:00:00Z"

// Without --force a development build behaves as it always has, plus one line on stderr naming the flag.
func TestADevBuildWithoutForceSkipsAndSaysHowToForce(t *testing.T) {
	h := newForceHarness(t, devBuild, "1.24.2", false)
	result, err := h.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if result.CLI.Message != "Local CLI development build. The CLI update was skipped." || result.CLI.Updated || result.CLI.Force {
		t.Fatalf("result %+v, want the old skip", result.CLI)
	}
	if !strings.Contains(h.progress.String(), "Local CLI development build. The CLI update was skipped.") {
		t.Fatalf("progress %q lost the old line", h.progress.String())
	}
	if h.errOut.String() != "Use runos update --force to replace it with the published release.\n" {
		t.Fatalf("stderr %q, want exactly the hint line", h.errOut.String())
	}
	if h.downloads != 0 || h.binary(t) != oldBinary {
		t.Fatalf("downloads=%d binary=%q, want nothing replaced", h.downloads, h.binary(t))
	}
}

func TestADevBuildWithForceReplacesItWithTheRelease(t *testing.T) {
	h := newForceHarness(t, devBuild, "1.24.2", false)
	result, err := h.run(t, "--force")
	if err != nil {
		t.Fatal(err)
	}
	if h.binary(t) != newBinary {
		t.Fatalf("binary %q, want the release", h.binary(t))
	}
	want := "Replacing the development build " + devBuild + " with the published release v1.24.2"
	if !strings.Contains(h.said, want) {
		t.Fatalf("before the download the output said %q, want it to contain %q", h.said, want)
	}
	if !result.CLI.Updated || !result.CLI.Force || result.CLI.PreviousVersion != devBuild || result.CLI.NewVersion != "1.24.2" || result.CLI.Version != "1.24.2" {
		t.Fatalf("result %+v", result.CLI)
	}
	if h.errOut.Len() != 0 {
		t.Fatalf("stderr %q, want none (the hint is for a run without --force)", h.errOut.String())
	}
	// The VPN notice reads what is now on disk, so a forced install must report the release.
	if got := installedCLIVersion(version.Version, result.CLI.updateComponentResult); got != "1.24.2" {
		t.Fatalf("installedCLIVersion = %q, want 1.24.2", got)
	}
}

func TestAReleaseCandidateIsReplacedByTheOlderFinalAndCalledADowngrade(t *testing.T) {
	h := newForceHarness(t, "1.25.0-rc.2", "1.24.2", false)
	if _, err := h.run(t, "--force"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.said, "downgrade") || !strings.Contains(h.said, "Replacing 1.25.0-rc.2 with the published release v1.24.2") {
		t.Fatalf("said %q, want the downgrade named before the download", h.said)
	}
	if h.binary(t) != newBinary {
		t.Fatal("not replaced")
	}
}

// Every verification of the normal update stays: a wrong checksum replaces nothing.
func TestAChecksumMismatchReplacesNothing(t *testing.T) {
	h := newForceHarness(t, devBuild, "1.24.2", true)
	_, err := h.run(t, "--force")
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum failure", err)
	}
	if h.binary(t) != oldBinary {
		t.Fatalf("binary %q, want the old one untouched", h.binary(t))
	}
	entries, _ := os.ReadDir(filepath.Dir(h.exe))
	if len(entries) != 1 {
		t.Fatalf("the install directory holds %d entries, want only the old binary", len(entries))
	}
}

// Only a final release is ever chosen.
func TestAPrereleaseIsNeverChosen(t *testing.T) {
	h := newForceHarness(t, devBuild, "1.25.0-rc.3", false)
	_, err := h.run(t, "--force")
	if err == nil || !strings.Contains(err.Error(), "final release") {
		t.Fatalf("err = %v, want a refusal naming final releases", err)
	}
	if h.downloads != 0 || h.binary(t) != oldBinary {
		t.Fatalf("downloads=%d binary=%q, want nothing fetched or replaced", h.downloads, h.binary(t))
	}
}

func TestCheckWithForceInstallsNothingAndSaysWhatWouldBeReplaced(t *testing.T) {
	h := newForceHarness(t, "1.25.0-rc.2", "1.24.2", false)
	result, err := h.run(t, "--check", "--force")
	if err != nil {
		t.Fatalf("--check --force must exit 0: %v", err)
	}
	if h.downloads != 0 || h.binary(t) != oldBinary {
		t.Fatalf("downloads=%d binary=%q, want nothing installed", h.downloads, h.binary(t))
	}
	out := h.progress.String()
	if !strings.Contains(out, "Replacing 1.25.0-rc.2 with the published release v1.24.2") || !strings.Contains(out, "Nothing was installed") {
		t.Fatalf("output %q, want what would be replaced and that nothing was", out)
	}
	if result.CLI.Updated || !result.CLI.UpdateAvailable || !result.CLI.Force || result.CLI.NewVersion != "1.24.2" {
		t.Fatalf("result %+v", result.CLI)
	}
}

func TestTheForcedResultJSONCarriesTheForceFactAndBothVersions(t *testing.T) {
	h := newForceHarness(t, devBuild, "1.24.2", false)
	result, err := h.run(t, "--force")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	cli, _ := decoded["cli"].(map[string]any)
	if cli["force"] != true || cli["previousVersion"] != devBuild || cli["newVersion"] != "1.24.2" || cli["updated"] != true {
		t.Fatalf("cli = %v", cli)
	}
	// An ordinary run keeps its old shape: no force fields at all.
	h2 := newForceHarness(t, "1.23.0", "1.23.0", false)
	plain, _ := h2.run(t)
	data, _ = json.Marshal(plain)
	if strings.Contains(string(data), "force") || strings.Contains(string(data), "previousVersion") {
		t.Fatalf("an ordinary result carries force fields: %s", data)
	}
}

func TestForceOnTheSameVersionReinstallsAndNamesIt(t *testing.T) {
	h := newForceHarness(t, "1.24.2", "1.24.2", false)
	if _, err := h.run(t, "--force"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.said, "Reinstalling the published release v1.24.2") || h.binary(t) != newBinary {
		t.Fatalf("said %q binary %q", h.said, h.binary(t))
	}
}

// A normal update (no --force) of a released build is unchanged.
func TestWithoutForceAReleasedBuildUpdatesAsBefore(t *testing.T) {
	h := newForceHarness(t, "1.23.0", "1.24.2", false)
	result, err := h.run(t)
	if err != nil || !result.CLI.Updated || h.binary(t) != newBinary || result.CLI.Force {
		t.Fatalf("err %v result %+v binary %q", err, result.CLI, h.binary(t))
	}
	if strings.Contains(h.progress.String(), "Replacing") {
		t.Fatalf("a normal update printed the forced line: %q", h.progress.String())
	}
	h2 := newForceHarness(t, "1.25.0-rc.2", "1.24.2", false)
	r2, err := h2.run(t)
	if err != nil || r2.CLI.Updated || h2.binary(t) != oldBinary {
		t.Fatalf("a build ahead of the release was changed without --force: %+v %v", r2.CLI, err)
	}
}
