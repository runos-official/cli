package dynacmd

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// The destructive confirmation prompt printed the node id alone, so the
// operator read `nid=<node-id>` and could not tell which machine that node
// id was. This file reads the node NAME and adds the node name to that one
// line, in the prompt and in the non-TTY refusal alike.
//
// Three rules govern everything here:
//
//  1. The lookup only DECORATES a line. The lookup never blocks a command,
//     never prints a warning, and never returns an error. Every failure
//     falls back to the node id alone.
//  2. ONE deadline covers the WHOLE decoration, not just the API call. The
//     config load and the token refresh each build their own ten second
//     client, so a bound on the API call alone would still let the prompt
//     wait about twelve seconds on a slow token endpoint.
//  3. The node id stays on the line beside the node name. The operator
//     types the node id, so the operator must be able to see a mistyped
//     node id as well as a wrong machine.

// nodeIDFlagName is the flag the CLI spells the node id as, everywhere on
// the surface.
const nodeIDFlagName = "nid"

// isNodeIDField reports whether a manifest field's displayed value names a
// node, and it is the ONE definition both prompt rules use, so the two
// rules cannot disagree about the same command.
//
// The test is the FLAG spelling, not the wire field name. The manifest
// carries the node id under two field names: `nid` on the nodes and
// storage-groups commands, and `NODE_NID` on every
// maintenance-scripts/<script>/run command, which flagSpellingOverrides
// (builder.go) maps to the same `--nid` flag. Keying on the field name
// alone left a maintenance script run naming no node, and a script run
// cordons, drains or reboots a machine. No manifest command declares both
// names, so the predicate is unambiguous.
func isNodeIDField(fieldName string) bool {
	return flagNameFor(fieldName) == nodeIDFlagName
}

// nodeNameDeadline bounds the whole decoration: the config load, the
// credential check, the token resolution and the node read together.
//
// Two steps inside that work carry a TEN second timeout of their own.
// config.Load can fetch the environment URLs remotely, and the
// interactive credential refreshes its ID token on every call. This
// deadline is what keeps the prompt quick when either of those two is
// slow or blackholed.
const nodeNameDeadline = 2 * time.Second

// loadNodeNameConfig is config.Load behind a seam, so a test can inject a
// config and never depend on a developer's config file. A test also
// injects a SLOW loader here, which proves the deadline covers the config
// load. Nothing outside a test writes this.
var loadNodeNameConfig = config.Load

// nodeNameWorkerDone is nil in production. A deadline test sets it, so the
// test can wait for the abandoned worker to stop before the test restores
// the package level seams the worker reads. Without that wait the restore
// and the worker are an unsynchronised pair, which the race detector
// reports.
var nodeNameWorkerDone func()

// decorateNodeTarget renders one positional target field for the
// destructive confirmation prompt, and adds the node name when the field
// is the node id field.
//
// Returns `nid=<node-id> name=<node-name>` when the lookup resolves a
// usable node name. Returns `<field>=<value>` unchanged for every other
// field, and for every failure of the lookup.
func decorateNodeTarget(c *cobra.Command, cmdDef manifest.Command, args []string, fieldName, value string) string {
	return fmt.Sprintf("%s=%s", fieldName, value) + nodeNameSuffix(c, cmdDef, args, fieldName, value)
}

// nodeNameSuffix is the node name part of one target line entry, and it
// is the ONE place the lookup and the label rule are reached from.
//
// The prompt has two rules that render an entry differently: the
// positional rule prints the manifest FIELD name, the changed-flag rule
// prints the FLAG name. The two rules share this suffix rather than the
// rendering, so a node is named the same way whichever rule builds the
// line.
//
// Returns ` name=<node-name>` when the field is the node id field and the
// lookup resolves a usable name. Returns the empty string for every other
// field, and for every failure, blank name or timeout.
func nodeNameSuffix(c *cobra.Command, cmdDef manifest.Command, args []string, fieldName, value string) string {
	if !isNodeIDField(fieldName) {
		return ""
	}
	name := resolveNodeName(c, cmdDef, args, value)
	if name == "" {
		return ""
	}
	return " name=" + name
}

// resolveNodeName runs the whole decoration under one deadline and returns
// the node label, or the empty string when the decoration does not finish
// in time or does not succeed.
//
// The result channel holds one value, so the abandoned worker always
// completes its send and never leaks on the channel. The worker still ends
// by itself when its own client timeout expires.
func resolveNodeName(c *cobra.Command, cmdDef manifest.Command, args []string, nodeID string) string {
	result := make(chan string, 1)
	go func() {
		done := nodeNameWorkerDone
		defer func() {
			if done != nil {
				done()
			}
		}()
		result <- readNodeName(c, cmdDef, args, nodeID)
	}()
	select {
	case name := <-result:
		return nodeLabel(name)
	case <-time.After(nodeNameDeadline):
		return ""
	}
}

// readNodeName resolves the inputs the way the executor resolves them, so
// the prompt names the same node the request that follows the prompt
// addresses. Every failure returns the empty string, silently.
func readNodeName(c *cobra.Command, cmdDef manifest.Command, args []string, nodeID string) string {
	cfg, err := loadNodeNameConfig()
	if err != nil || cfg == nil {
		return ""
	}
	// HasCredentials makes no network round trip, so a machine with no
	// credential ends the decoration here rather than in a token refresh.
	if !auth.HasCredentials(cfg) {
		return ""
	}
	accountID := cfg.GetAccountID()
	if accountID == "" {
		return ""
	}
	clusterID := nodeNameClusterID(c, cmdDef, args, cfg)
	if clusterID == "" {
		return ""
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil || token == "" {
		return ""
	}
	client := api.NewClientWithTimeout(cfg.GetAPIURL(), nodeNameDeadline)
	name, err := client.NodeName(accountID, clusterID, nodeID, token)
	if err != nil {
		return ""
	}
	return name
}

// nodeNameClusterID returns the cluster the executor would address, in the
// executor's own order: the --cid flag, then a cid positional field, then
// the default cluster from the config.
func nodeNameClusterID(c *cobra.Command, cmdDef manifest.Command, args []string, cfg *config.Config) string {
	if c != nil {
		if f := c.Flags().Lookup("cid"); f != nil {
			if cid := f.Value.String(); cid != "" {
				return cid
			}
		}
	}
	if cid := positionalArgForField(args, cmdDef, "cid"); cid != "" {
		return cid
	}
	return cfg.GetDefaultClusterID()
}

// nodeLabel decides what the prompt may print as a node name, and returns
// the empty string when the prompt must print the node id alone.
//
// The rule matches the console, so the two surfaces name a node the same
// way. The node hostname is never a fallback.
//
// The node rename route refuses a control character, but the two node
// registration routes validate nothing, so a stored node name can carry
// surrounding space, only space characters, or a control character. A
// control character in this one line could move the cursor and imitate a
// prompt, and the operator reads this line before an irreversible command.
func nodeLabel(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return trimmed
}
