package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
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
	bareHasVMs bool
	// bareHasVirtShape adds the gated leaf that surfaces as an unknown
	// FLAG rather than an unknown command: `runos nodes virt-shape --nid X`.
	bareHasVirtShape bool
	virtEnabled      bool
	modulesStatus    int
}

// newModuleConductor serves the scoped manifest (never carrying the VM
// commands), the bare manifest, and the modules route, and points a temp
// HOME at it. The cached manifest is seeded at the version the fake
// serves, so judgeStaleManifest reaches verdictCommandUnknown, which is
// the branch under test.
func newModuleConductor(t *testing.T, opts moduleConductorOpts) *moduleConductor {
	t.Helper()
	const aid = "acct1"
	const version = "45.3.0"

	fake := &moduleConductor{}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+aid+"/cli/manifest-version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version})
	})
	mux.HandleFunc("/"+aid+"/cli/manifest", func(w http.ResponseWriter, r *http.Request) {
		writeManifest(w, version, "clusters/list")
	})
	mux.HandleFunc("/cli/manifest", func(w http.ResponseWriter, r *http.Request) {
		bare := []string{"clusters/list"}
		if opts.bareHasVMs {
			bare = append(bare, "vms/list", "vms/{id}/show")
		}
		if opts.bareHasVirtShape {
			bare = append(bare, "nodes/{nid}/virt-shape")
		}
		writeManifest(w, version, bare...)
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
	fake.Server = httptest.NewServer(fake.record(mux))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.Unsetenv("RUNOS_API_KEY")
	os.Unsetenv("RUNOS_ACCOUNT_ID")
	runosDir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(runosDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := fmt.Sprintf(`{"api_key":"pat-test-token","account_id":%q,"conductor_url":%q}`, aid, fake.URL)
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
	return fake
}

// moduleConductor is the fake conductor and the log of every request the
// CLI made to it. The log is what proves a cost: objective 84 finding 25
// was measured as requests, so the tests assert requests, not source text.
type moduleConductor struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string
}

// record wraps the routes and appends the method and path of each request.
func (c *moduleConductor) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.requests = append(c.requests, r.Method+" "+r.URL.Path)
		c.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// recorded returns the requests made so far, in order.
func (c *moduleConductor) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
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

// --- the second shape: a gated LEAF whose PARENT group survives ---
//
// Found by the story 177 live run against conductor rc.6. `runos vms
// list` exited 0 and printed the `vms` help, and `runos nodes virt-shape
// --nid X` reported an unknown flag. Neither said the module was off,
// because neither produces cobra's "unknown command" wording.

func TestTypedCommandPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"plain path", []string{"vms", "list"}, []string{"vms", "list"}},
		{"stops at a long flag", []string{"nodes", "virt-shape", "--nid", "n1"}, []string{"nodes", "virt-shape"}},
		{"stops at a short flag", []string{"vms", "list", "-j"}, []string{"vms", "list"}},
		{"stops at a bare dash-dash", []string{"run", "--", "sh"}, []string{"run"}},
		{"a leading flag leaves nothing", []string{"--help"}, nil},
		{"no args", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := typedCommandPath(tc.args)
			if len(got) != len(tc.want) {
				t.Fatalf("typedCommandPath(%v) = %v, want %v", tc.args, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("typedCommandPath(%v) = %v, want %v", tc.args, got, tc.want)
				}
			}
		})
	}
}

// survivorTree is the shape a module gate actually leaves behind: `vms`
// survives because `vms ssh` is a static Go command, and `nodes`
// survives because it is core, but the gated leaves are gone.
//
// THE TWO GROUPS HAVE DIFFERENT SHAPES, AND THE PRODUCT IS WHY. This
// fixture runs the REAL strictenParentExitCodes (cmd/parents.go) at the
// point cmd/root.go's init runs it, so the fixture follows that function
// if it ever changes.
//
// `nodes` already has a child then, so the strictener gives it
// `Args: cobra.NoArgs` and a dummy RunE, and it comes out RUNNABLE. Every
// dynamic group (nodes, jobs, account, integrations, services/<type>) is
// built during registerDynamicCommands and so has the same shape.
//
// `vms` escapes, because cmd/vms.go's init adds `vms ssh` AFTER
// cmd/root.go's init has run (Go initializes files in filename order), so
// vmsCmd is childless when the strictener walks past it.
//
// A fixture that gave BOTH groups no Run would hide the defect objective
// 84 rework cycle 1 found: a runnable test in the shared helper kills the
// `runos nodes virt-shape --nid X` hint on the error path.
func survivorTree() *cobra.Command {
	root := &cobra.Command{Use: "runos"}
	vms := &cobra.Command{Use: "vms"}
	nodes := &cobra.Command{Use: "nodes"}
	nodes.AddCommand(&cobra.Command{Use: "cordon", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(vms, nodes)

	strictenParentExitCodes(root)

	vms.AddCommand(&cobra.Command{Use: "ssh", Run: func(*cobra.Command, []string) {}})
	return root
}

func TestUnresolvedTypedPath(t *testing.T) {
	t.Parallel()
	root := survivorTree()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"gated leaf under a surviving parent", []string{"vms", "list"}, "vms/list"},
		{"gated leaf before a flag", []string{"nodes", "virt-shape", "--nid", "n1"}, "nodes/virt-shape"},
		{"a fully resolved path is not unresolved", []string{"vms", "ssh"}, ""},
		{"a resolved group alone is not unresolved", []string{"nodes"}, ""},
		// The helper keeps every leftover token. The SUCCESS-path caller
		// drops this one, because `vms ssh` is runnable and cobra RAN it;
		// the ERROR-path caller must still see paths of this shape.
		{"a positional argument still yields a path the bare manifest will reject", []string{"vms", "ssh", "myvm"}, "vms/ssh/myvm"},
		{"nothing typed", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := unresolvedTypedPath(root, tc.args); got != tc.want {
				t.Errorf("unresolvedTypedPath(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
	if got := unresolvedTypedPath(nil, []string{"vms", "list"}); got != "" {
		t.Errorf("a nil root resolves nothing, got %q", got)
	}
}

// Criterion 9, the case v1.20.0-rc.1 got wrong. `runos vms list` with
// virt off must name the enable command, not print help and exit 0.
func TestAGatedLeafUnderASurvivingParentNamesTheEnableCommand(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: false})
	defer fake.Close()

	var explained bool
	out := captureStderr(t, func() {
		explained = explainUnresolvedParentSurvivor(survivorTree(), []string{"vms", "list"})
	})

	if !explained {
		t.Error("a gated leaf must be explained, so the caller can exit non-zero")
	}
	if !strings.Contains(out, "runos account modules enable virt") {
		t.Errorf("stderr does not name the enable command:\n%s", out)
	}
}

// The safety net: a path the BARE manifest does not define must stay
// silent, so an ordinary typo is never blamed on a module. `vms nope`
// takes the same route a gated leaf takes, because `vms` is a group with
// no Run: cobra prints the `vms` help and exits 0.
func TestAnUnresolvedPathTheAPIDoesNotServeExplainsNothing(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: false})
	defer fake.Close()

	var explained bool
	out := captureStderr(t, func() {
		explained = explainUnresolvedParentSurvivor(survivorTree(), []string{"vms", "nope"})
	})

	if explained {
		t.Error("a path the bare manifest does not define is not a module gate")
	}
	if strings.Contains(out, "account modules") {
		t.Errorf("a module was blamed for a wrong guess:\n%s", out)
	}
}

// Criterion 4, the error half of the same rule. A typed path the bare
// manifest also lacks keeps the unknown-command wording, so a wrong guess
// never blames a module even when the module the group belongs to is off.
func TestAnUnresolvedPathTheAPIDoesNotServeKeepsTheUnknownCommandText(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: false})
	defer fake.Close()

	out := captureStderr(t, func() {
		explainPossiblyStaleManifest(errors.New(`unknown command "nope" for "runos vms"`))
	})

	if !strings.Contains(out, "really does not exist") {
		t.Errorf("expected the unchanged missing-command wording:\n%s", out)
	}
	if strings.Contains(out, "account modules") {
		t.Errorf("a path the bare manifest lacks must not be blamed on a module:\n%s", out)
	}
}

// REGRESSION, objective 84 rework cycle 1. The runnable test belongs to
// the SUCCESS-path caller only. `runos nodes virt-shape --nid n1` reaches
// the ERROR path instead: the gate takes `virt-shape`, `nodes` swallows it
// as an argument, and cobra refuses `--nid`. Cobra RAN nothing there, so
// the runnable test does not apply, although `nodes` IS runnable after
// strictenParentExitCodes. A guard in the shared helper made this hint
// silent and printed "this flag really does not exist" instead.
func TestAGatedLeafUnderAStrictenedGroupIsExplainedOnTheErrorPath(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVirtShape: true, virtEnabled: false})
	defer fake.Close()

	// The error path reads the GLOBAL rootCmd, so the group has to live
	// there. Take out anything the process-start init already registered
	// under that name, and put it back afterwards.
	if existing, _, err := rootCmd.Find([]string{"nodes"}); err == nil && existing != rootCmd {
		rootCmd.RemoveCommand(existing)
		defer rootCmd.AddCommand(existing)
	}
	nodes := &cobra.Command{Use: "nodes"}
	nodes.AddCommand(&cobra.Command{Use: "cordon", Run: func(*cobra.Command, []string) {}})
	strictenParentExitCodes(nodes)
	if !nodes.Runnable() {
		t.Fatal("the fixture must model the strictened tree, so `nodes` has to be runnable")
	}
	rootCmd.AddCommand(nodes)
	defer rootCmd.RemoveCommand(nodes)

	oldArgs := os.Args
	os.Args = []string{"runos", "nodes", "virt-shape", "--nid", "n1"}
	defer func() { os.Args = oldArgs }()

	out := captureStderr(t, func() {
		explainPossiblyStaleManifest(errors.New("unknown flag: --nid"))
	})

	if !strings.Contains(out, "runos account modules enable virt") {
		t.Errorf("stderr does not name the enable command:\n%s", out)
	}
	if strings.Contains(out, "this flag really does not exist") {
		t.Errorf("a module gate was blamed on the flag:\n%s", out)
	}
}

// --- the cost of the probe (objective 84, findings 25 and 18) ---
//
// The probe made two conductor requests after EVERY successful command,
// because a consumed positional argument leaves the same leftover token a
// gated leaf leaves. These tests measure requests, which is the unit the
// reviewers measured the defect in.

// Criterion 1. `runos vms ssh myvm` succeeds, and the probe that runs
// after it must reach the network zero times.
func TestASuccessfulCommandWithAPositionalArgumentMakesNoRequest(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: false})
	defer fake.Close()

	root := survivorTree()
	args := []string{"vms", "ssh", "myvm"}
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("the command must succeed before the probe runs: %v", err)
	}

	var explained bool
	out := captureStderr(t, func() {
		explained = explainUnresolvedParentSurvivor(root, args)
	})

	if explained {
		t.Error("a command that ran is not a module gate")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a successful command made %d conductor requests, want 0: %v", len(got), got)
	}
	if strings.Contains(out, "account modules") {
		t.Errorf("a module was blamed for a positional argument:\n%s", out)
	}
}

// Criterion 2. Shell completion runs on every TAB, so it must cost
// nothing. Cobra registers `__complete` from Execute and gives it a RunE,
// which is why the probe returns before any request.
func TestShellCompletionMakesNoRequest(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: false})
	defer fake.Close()

	root := survivorTree()
	args := []string{"__complete", "vms", ""}
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("cobra must serve the completion request: %v", err)
	}

	var explained bool
	captureStderr(t, func() {
		explained = explainUnresolvedParentSurvivor(root, args)
	})

	if explained {
		t.Error("a completion request is not a module gate")
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("a completion request made %d conductor requests, want 0: %v", len(got), got)
	}
}

// A module that is ON explains nothing here either.
func TestAGatedLeafWithTheModuleOnExplainsNothing(t *testing.T) {
	fake := newModuleConductor(t, moduleConductorOpts{bareHasVMs: true, virtEnabled: true})
	defer fake.Close()

	var explained bool
	out := captureStderr(t, func() {
		explained = explainUnresolvedParentSurvivor(survivorTree(), []string{"vms", "list"})
	})

	if explained {
		t.Error("an enabled module must not be offered for enabling")
	}
	if strings.Contains(out, "enable virt") {
		t.Errorf("an enabled module was offered for enabling:\n%s", out)
	}
}
