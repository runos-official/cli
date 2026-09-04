package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
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
// THE COST, AND WHERE IT IS PAID. The probe makes two extra requests at
// AdvisoryTimeout, so it runs on the FAILURE path ONLY. A command that
// cobra RAN is never probed: a runnable command consumed the leftover
// tokens as positionals and did what the user asked, so a module gate
// cannot be the cause. Cobra's `__complete` command is runnable too, so
// shell completion pays nothing. A gate that takes a leaf leaves a
// surviving GROUP, and a group carries no Run (see the intermediate
// command in internal/dynacmd/builder.go), so the hint keeps working.
//
// THE ONE SHAPE THIS RULE DROPS. A group that is runnable AND takes
// arbitrary positionals runs with the leftover token as an argument, so
// the command did run and the "success that is really a failure" case
// does not apply to it. No RunOS group has that shape today.

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

	// Read the module list BEFORE printing anything. An account with
	// every module ON has no module explanation to give, and printing
	// the preamble first would commit to one before knowing that.
	disabled, read := disabledModuleKeys(cfg)
	if read && len(disabled) == 0 {
		return false
	}

	fmt.Fprintf(os.Stderr,
		"\n`%s` is a real RunOS command, but it is not available to this account.\n"+
			"It belongs to an account module that is switched off.\n", strings.ReplaceAll(unknownPath, "/", " "))

	if !read {
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

// disabledModuleKeys lists the modules this account has switched off,
// and reports whether the list could be READ at all.
//
// The two answers must not be conflated. "No module is off" and "I could
// not find out" both yield an empty slice, and they call for opposite
// behaviour: the first means there is no module explanation to give, the
// second means give a general one. Returning them as one value made an
// account with every module ON print a module hint. Every failure is
// silent: this runs to explain a failure the user already has.
func disabledModuleKeys(cfg *config.Config) (keys []string, read bool) {
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil, false
	}
	client := api.NewClientWithTimeout(cfg.GetAPIURL(), manifest.AdvisoryTimeout)
	modules, err := client.AccountModules(cfg.GetAccountID(), token)
	if err != nil {
		return nil, false
	}
	for _, m := range modules {
		if !m.Enabled {
			keys = append(keys, m.Key)
		}
	}
	return keys, true
}

// THE SECOND SHAPE OF THE SAME DEFECT (found by the story 177 live run).
//
// A module gate removes a LEAF while its parent group SURVIVES, because
// the parent also carries commands the module does not own: `runos vms
// ssh` is a static Go command, not a manifest row, so `vms` stays in the
// tree with virt off. Cobra then resolves the parent, treats the missing
// leaf as a stray argument, and never says "unknown command":
//
//	runos vms list                 -> prints the `vms` help, EXIT 0
//	runos nodes virt-shape --nid X -> "unknown flag: --nid", exit 1
//
// Both are module gates. Neither reaches unknownCommandPath, which reads
// only cobra's "unknown command" wording, so the account was told
// nothing. These recover the path from the command LINE instead of the
// message.

// typedCommandPath returns the leading non-flag arguments of a command
// line: the command path the user typed, up to the first flag.
func typedCommandPath(args []string) []string {
	var path []string
	for _, a := range args {
		if a == "--" || strings.HasPrefix(a, "-") {
			break
		}
		path = append(path, a)
	}
	return path
}

// unresolvedTypedPath reports the manifest path the user typed when cobra
// resolved only a PREFIX of it, and "" when cobra ran a command or found
// the whole path.
//
// Two conditions must both hold. Cobra must leave tokens over, AND the
// command cobra resolved must not be runnable. A runnable command took
// the leftover tokens as positionals and ran (`runos vms ssh myvm`), so
// the user got the behaviour the user asked for and no module gate is
// involved. Probing that command spends two requests on a command that
// already succeeded (objective 84, findings 25 and 18).
//
// A module gate leaves the opposite shape. The gated leaf is gone, the
// parent GROUP survives, and a group carries no Run, so `runos vms list`
// still reaches the probe.
//
// The caller keeps its own safety net: the BARE manifest has to define
// the path before any module is named, so a wrong guess prints nothing.
func unresolvedTypedPath(root *cobra.Command, args []string) string {
	typed := typedCommandPath(args)
	if root == nil || len(typed) == 0 {
		return ""
	}
	found, rest, err := root.Find(typed)
	if err != nil || len(rest) == 0 {
		return ""
	}
	if found.Runnable() {
		// Cobra dispatched this command and took the leftover tokens as
		// positional arguments. The command ran, so nothing failed and
		// nothing needs an explanation.
		return ""
	}
	return strings.Join(typed, "/")
}

// explainUnresolvedParentSurvivor explains a command line whose leaf
// cobra could not resolve although its parent group survived, and
// reports whether it printed anything.
//
// The caller turns true into a non-zero exit. Cobra printed the parent's
// help and returned nil, so without this the operator gets a SUCCESS
// exit code for a command this account cannot run, which is the one
// success path that is really a failure.
//
// Only a CURRENT cache proves the absence is a module gate rather than a
// stale command list. The other two verdicts already have their own
// wording on the error path, and saying it twice would be worse than
// saying it once.
func explainUnresolvedParentSurvivor(root *cobra.Command, args []string) bool {
	path := unresolvedTypedPath(root, args)
	if path == "" {
		return false
	}
	loader, err := newAdvisoryManifestLoader()
	if err != nil {
		return false
	}
	cached := ""
	if m, merr := loader.LoadLocal(); merr == nil && m != nil {
		cached = m.Version
	}
	server, serr := loader.ServerVersion()
	if judgeStaleManifest(cached, server, serr) != verdictCommandUnknown {
		return false
	}
	return explainModuleGate(path)
}
