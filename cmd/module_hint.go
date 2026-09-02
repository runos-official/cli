package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// A command that is missing because its MODULE is off looks exactly like
// a command that does not exist (FPL31, story 177 criterion 9).
//
// THE DEFECT. Conductor serves this account's manifest, and a disabled
// module's commands are not in it. The cobra tree is built from that
// manifest, so `runos vms list` never reaches the 403 renderer: cobra
// answers `unknown command "vms" for "runos"` and the stale-manifest
// explainer then says the command "really does not exist", because the
// cached list genuinely does match the server. Every word of that is
// true and the conclusion is wrong. The capability exists, the account
// has it switched off, and one command switches it on.
//
// THE TEST. The BARE manifest is the unfiltered command list: it names
// every command conductor serves, whatever any account has switched on.
// A path missing from this account's tree but present in the bare list is
// therefore a module gate, not a typo.
//
// Two extra requests, on the failure path only, at AdvisoryTimeout: this
// runs to explain a failure the user already has, so it must not add ten
// seconds to a command that has already failed.

// unknownCommandPath turns cobra's message into the manifest path the
// user was reaching for.
//
// `unknown command "list" for "runos vms"` -> `vms/list`. The quoted
// parent is the command LINE, so its first word is the binary name and is
// dropped. Returns "" for any message this does not recognise, which
// keeps a reworded cobra error from producing a nonsense path.
func unknownCommandPath(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "unknown command") {
		return ""
	}
	quoted := quotedRuns(msg)
	if len(quoted) == 0 {
		return ""
	}
	leaf := quoted[0]
	if leaf == "" {
		return ""
	}
	var parents []string
	if len(quoted) > 1 {
		// Drop the binary name; keep every group under it.
		fields := strings.Fields(quoted[1])
		if len(fields) > 1 {
			parents = fields[1:]
		}
	}
	return strings.Join(append(parents, leaf), "/")
}

// quotedRuns returns the double-quoted runs of s, in order.
func quotedRuns(s string) []string {
	var out []string
	parts := strings.Split(s, `"`)
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

// manifestHasPath reports whether m defines commandPath, or any command
// under it. `vms` matches `vms/list`, so a group name gets the same
// answer as a leaf: the user who typed `runos vms list` is told about the
// module either way.
func manifestHasPath(m *manifest.Manifest, commandPath string) bool {
	if m == nil || commandPath == "" {
		return false
	}
	for _, c := range m.Commands {
		defined := strings.Trim(placeholderRegex.ReplaceAllString(c.Command, ""), "/")
		if defined == commandPath || strings.HasPrefix(defined, commandPath+"/") {
			return true
		}
	}
	return false
}

// explainModuleGate prints the module explanation for an unknown command
// and reports whether it printed anything.
//
// It stays quiet unless it can prove the case: the bare manifest has to
// define the path this account's tree lacks. When the module list cannot
// be read it names `runos account modules` rather than guessing a key,
// because naming the wrong module is worse than naming none.
func explainModuleGate(unknownPath string) bool {
	if unknownPath == "" {
		return false
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	loader, err := newAdvisoryManifestLoader()
	if err != nil {
		return false
	}
	bare, err := loader.BareManifest()
	if err != nil || !manifestHasPath(bare, unknownPath) {
		return false
	}

	fmt.Fprintf(os.Stderr,
		"\n`%s` is a real RunOS command, but it is not available to this account.\n"+
			"It belongs to an account module that is switched off.\n", strings.ReplaceAll(unknownPath, "/", " "))

	disabled := disabledModuleKeys(cfg)
	if len(disabled) == 0 {
		// Naming the wrong module key is worse than naming none, so an
		// unreadable list points at the command that shows the real one.
		fmt.Fprintf(os.Stderr, "Run `runos account modules` to see which, then `runos account modules enable <key>`.\n")
		return true
	}
	for _, key := range disabled {
		fmt.Fprintf(os.Stderr, "Run `runos account modules enable %s` to switch it on, then run your command again.\n", key)
	}
	return true
}

// disabledModuleKeys lists the modules this account has switched off.
//
// Returns nothing when the list cannot be read, and the caller then falls
// back to naming the listing command. Every failure is silent: this runs
// to explain a failure the user already has.
func disabledModuleKeys(cfg *config.Config) []string {
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil
	}
	client := api.NewClientWithTimeout(cfg.GetAPIURL(), manifest.AdvisoryTimeout)
	modules, err := client.AccountModules(cfg.GetAccountID(), token)
	if err != nil {
		return nil
	}
	var keys []string
	for _, m := range modules {
		if !m.Enabled {
			keys = append(keys, m.Key)
		}
	}
	return keys
}
