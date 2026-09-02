package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// FPL31 / story 177 criterion 9. `runos vms list` against an account with
// virt switched off must name `runos account modules enable virt`, and a
// command that genuinely does not exist must keep the wording it has now.

func TestUnknownCommandPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		msg  string
		want string
	}{
		{"top-level group", `unknown command "vms" for "runos"`, "vms"},
		{"one parent", `unknown command "list" for "runos vms"`, "vms/list"},
		{"two parents", `unknown command "virt-shape" for "runos nodes n1"`, "nodes/n1/virt-shape"},
		{"an unknown flag is not a command", `unknown flag: --nope`, ""},
		{"an unrelated error", `dial tcp: connection refused`, ""},
		{"a message with no quotes", `unknown command`, ""},
		{"an empty leaf", `unknown command "" for "runos"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tc.msg != "" {
				err = errors.New(tc.msg)
			}
			if got := unknownCommandPath(err); got != tc.want {
				t.Errorf("unknownCommandPath(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
	if got := unknownCommandPath(nil); got != "" {
		t.Errorf("unknownCommandPath(nil) = %q, want empty", got)
	}
}

// A group name has to match the commands under it: the user typed
// `runos vms list` and cobra reported the group, so `vms` must find
// `vms/list` or nothing explains the failure.
func TestManifestHasPath(t *testing.T) {
	t.Parallel()
	bare := &manifest.Manifest{Commands: []manifest.Command{
		{Command: "vms/list"}, {Command: "vms/{id}/show"}, {Command: "clusters/list"},
	}}
	for path, want := range map[string]bool{
		"vms":           true,
		"vms/list":      true,
		"vms/show":      true,
		"clusters/list": true,
		"vm":            false,
		"vms/nope":      false,
		"nodes":         false,
		"":              false,
	} {
		if got := manifestHasPath(bare, path); got != want {
			t.Errorf("manifestHasPath(%q) = %v, want %v", path, got, want)
		}
	}
	if manifestHasPath(nil, "vms") {
		t.Error("a nil manifest defines nothing")
	}
}

// The criterion the Orchestrator adopted (FPL34 amendment A2): a scoped
// manifest without vms/list, a bare manifest with it, and a modules route
// listing virt as disabled, must produce the enable command on stderr.
func TestAModuleGateNamesTheEnableCommand(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: false})
	defer fake.Close()

	out := captureStderr(t, func() {
		explainPossiblyStaleManifest(errors.New(`unknown command "vms" for "runos"`))
	})

	if !strings.Contains(out, "runos account modules enable virt") {
		t.Errorf("stderr does not name the enable command:\n%s", out)
	}
	if strings.Contains(out, "really does not exist") {
		t.Errorf("a module gate must not be reported as a missing command:\n%s", out)
	}
}

// A path the BARE manifest also lacks is a command that really does not
// exist. The existing wording must survive untouched.
func TestACommandTheAPIDoesNotServeKeepsTheOldWording(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: false, virtEnabled: false})
	defer fake.Close()

	out := captureStderr(t, func() {
		explainPossiblyStaleManifest(errors.New(`unknown command "nope" for "runos"`))
	})

	if !strings.Contains(out, "really does not exist") {
		t.Errorf("expected the unchanged missing-command wording:\n%s", out)
	}
	if strings.Contains(out, "account modules") {
		t.Errorf("a command the API does not serve must not be blamed on a module:\n%s", out)
	}
}

// Naming the wrong module key is worse than naming none, so a modules
// route that cannot be read points at the listing command instead.
func TestAnUnreadableModuleListNamesTheListingCommand(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, modulesStatus: http.StatusInternalServerError})
	defer fake.Close()

	out := captureStderr(t, func() {
		explainPossiblyStaleManifest(errors.New(`unknown command "vms" for "runos"`))
	})

	if !strings.Contains(out, "runos account modules") {
		t.Errorf("stderr does not name the listing command:\n%s", out)
	}
	if strings.Contains(out, "enable virt") {
		t.Errorf("a key was named although the list could not be read:\n%s", out)
	}
}

// A module that is ON explains nothing, so the hint must stay quiet and
// the ordinary wording must stand.
func TestAnEnabledModuleProducesNoHint(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: true})
	defer fake.Close()

	out := captureStderr(t, func() {
		explainPossiblyStaleManifest(errors.New(`unknown command "vms" for "runos"`))
	})

	if strings.Contains(out, "enable virt") {
		t.Errorf("an enabled module was offered for enabling:\n%s", out)
	}
}

// --- fixtures ---

type moduleConductorOpts struct {
	bareHasVMs    bool
	virtEnabled   bool
	modulesStatus int
}

// newModuleConductor serves the scoped manifest (never carrying the VM
// commands), the bare manifest, and the modules route, and points a temp
// HOME at it. The cached manifest is seeded at the version the fake
// serves, so judgeStaleManifest reaches verdictCommandUnknown, which is
// the branch under test.
func newModuleConductor(t *testing.T, opts moduleConductorOpts) *httptest.Server {
	t.Helper()
	const aid = "acct1"
	const version = "45.3.0"

	mux := http.NewServeMux()
	mux.HandleFunc("/"+aid+"/cli/manifest-version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version})
	})
	mux.HandleFunc("/"+aid+"/cli/manifest", func(w http.ResponseWriter, r *http.Request) {
		writeManifest(w, version, "clusters/list")
	})
	mux.HandleFunc("/cli/manifest", func(w http.ResponseWriter, r *http.Request) {
		if opts.bareHasVMs {
			writeManifest(w, version, "clusters/list", "vms/list", "vms/{id}/show")
			return
		}
		writeManifest(w, version, "clusters/list")
	})
	mux.HandleFunc("/"+aid+"/modules", func(w http.ResponseWriter, r *http.Request) {
		if opts.modulesStatus != 0 && opts.modulesStatus != http.StatusOK {
			w.WriteHeader(opts.modulesStatus)
			fmt.Fprint(w, `{"error":"boom"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"modules": []map[string]any{
			{"key": "virt", "name": "Virtual Machines", "tier": "premium", "enabled": opts.virtEnabled},
		}})
	})
	srv := httptest.NewServer(mux)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.Unsetenv("RUNOS_API_KEY")
	os.Unsetenv("RUNOS_ACCOUNT_ID")
	runosDir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(runosDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := fmt.Sprintf(`{"api_key":"pat-test-token","account_id":%q,"conductor_url":%q}`, aid, srv.URL)
	if err := os.WriteFile(filepath.Join(runosDir, "config.json"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// The cached list matches the server, which is what sends
	// explainPossiblyStaleManifest down the verdictCommandUnknown branch.
	local := fmt.Sprintf(`{"version":%q,"commands":[{"command":"clusters/list"}]}`, version)
	if err := os.WriteFile(filepath.Join(runosDir, "manifest.json"), []byte(local), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runosDir, "manifest.account"), []byte(aid), 0600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	return srv
}

func writeManifest(w http.ResponseWriter, version string, commands ...string) {
	type cmd struct {
		Command string `json:"command"`
	}
	out := struct {
		Version  string `json:"version"`
		Commands []cmd  `json:"commands"`
	}{Version: version}
	for _, c := range commands {
		out.Commands = append(out.Commands, cmd{Command: c})
	}
	_ = json.NewEncoder(w).Encode(out)
}
