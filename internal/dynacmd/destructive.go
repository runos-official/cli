package dynacmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// destructiveVerbSuffixes is the curated set of command-path final
// segments that imply destructive semantics regardless of HTTP method.
// Pre-fix (#23) the guard fired only on Method=DELETE, but conductor
// uses POST/PATCH for many irreversible ops:
//   - clear-cache, set-data (Valkey/Redis-style state overwrites)
//   - drain, reset, restart (cluster/service lifecycle)
//   - exec-sql (postgres/mysql/clickhouse arbitrary SQL exec — destructive
//     when --read-write is set, and the guard errs on the safe side
//     by gating both modes)
//   - revoke-* / remove-* / delete-* (granular sub-resource deletes
//     under PATCH-shaped endpoints, e.g. minio/{id}/delete-bucket)
//   - wipe / flush / purge (rare but unambiguous)
//
// The mcp signal isn't a reliable discriminator: clear-cache and set-
// data report "write" alongside non-destructive ops like add/update,
// while exec-sql reports "sensitive_write". The verb-suffix catalog is
// what conductor's actual irreversibility classification looks like in
// the manifest namespaces.
var destructiveVerbSuffixes = []string{
	"delete",
	"drain",
	"reset",
	"clear-cache",
	"set-data",
	"exec-sql",
	"wipe",
	"flush",
	"purge",
}

// destructiveVerbPrefixes are leading-token matches used when the final
// segment carries a sub-resource name (e.g. `delete-bucket`,
// `delete-object`, `revoke-database`, `revoke-bucket`, `remove-peer`,
// `drop-user`, `drop-database`).
var destructiveVerbPrefixes = []string{
	"delete-",
	"revoke-",
	"remove-",
	"drop-",
}

// isDestructiveCommand reports whether cmdDef needs a confirmation
// guard before reaching the wire. Two signals contribute:
//
//  1. Method=DELETE: every DELETE-method endpoint in the manifest is
//     destructive by definition.
//  2. Command path verb suffix: many destructive ops are POST or PATCH
//     under conductor's REST shape (clear-cache, drain, reset, exec-sql,
//     delete-bucket, revoke-database, ...). Match the final path
//     segment against destructiveVerbSuffixes / destructiveVerbPrefixes
//     so the guard catches them too.
//
// Tagging on method + verb instead of an ad-hoc allow-list means new
// destructive endpoints inherit the prompt automatically when the
// conductor manifest grows.
func isDestructiveCommand(cmdDef manifest.Command) bool {
	if strings.EqualFold(cmdDef.Method, "DELETE") {
		return true
	}
	last := lastPathSegment(cmdDef.Command)
	if last == "" {
		return false
	}
	// Maintenance-scripts run triggers are destructive by category: every
	// registered script's `run` invocation reboots / cordons / drains /
	// otherwise mutates cluster state, and this gate inherits the prompt
	// for any future script the registry surfaces without enumerating
	// individual script ids. Tagged on path containment + final segment
	// so unrelated commands that happen to end in `run` (none today, but
	// hypothetical future ones) aren't falsely gated. Regression target:
	// foreman #37 / Story 50.
	if last == "run" && strings.Contains(cmdDef.Command, "maintenance-scripts/") {
		return true
	}
	for _, suffix := range destructiveVerbSuffixes {
		if last == suffix {
			return true
		}
	}
	for _, prefix := range destructiveVerbPrefixes {
		if strings.HasPrefix(last, prefix) {
			return true
		}
	}
	return false
}

// destructivePromptApplies decides whether confirmDestructive should
// actually fire for a given cobra invocation. It starts from
// isDestructiveCommand (which still drives --yes flag registration in
// buildLeafCommand) and adds per-command runtime carve-outs:
//
//   - exec-sql (postgresql / mysql / clickhouse) is only destructive in
//     read-write mode. The default invocation runs a read-only SELECT
//     and shouldn't force --yes; training operators to bypass the
//     prompt for every diagnostic query was a real footgun (foreman
//     #140). When --read-write is true, the prompt fires as before.
//
// Other destructive verbs (delete / drain / reset / clear-cache / ...)
// have no read-only mode and stay gated unconditionally.
func destructivePromptApplies(c *cobra.Command, cmdDef manifest.Command) bool {
	if !isDestructiveCommand(cmdDef) {
		return false
	}
	if lastPathSegment(cmdDef.Command) == "exec-sql" {
		if c != nil && c.Flags().Lookup("read-write") != nil {
			rw, _ := c.Flags().GetBool("read-write")
			if !rw {
				return false
			}
		}
	}
	return true
}

// lastPathSegment returns the final `/`-delimited segment of a manifest
// command path, e.g. "services/postgresql/{id}/exec-sql" -> "exec-sql".
// Used by isDestructiveCommand's verb-pattern matcher.
func lastPathSegment(cmd string) string {
	if i := strings.LastIndex(cmd, "/"); i >= 0 {
		return cmd[i+1:]
	}
	return cmd
}

// destructiveVerb renders a manifest command path as a space-delimited
// CLI invocation for the confirmation prompt, stripping `{name}`
// placeholders so the user sees `services valkey clear-cache` rather
// than `services valkey {id} clear-cache`.
func destructiveVerb(cmdPath string) string {
	parts := strings.Split(cmdPath, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if placeholderRegex.MatchString(p) {
			continue
		}
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, " ")
}

// destructiveSummary returns the human-readable target line shown in
// the confirmation prompt. Falls back to the manifest description
// when no positional id can be derived. Used so the user can spot a
// typo'd id before answering y/N.
//
// Positional fields are resolved from either the positional slot (args)
// or the matching `--<flag>` value, in declaration order. The flag
// fallback matters because some endpoints (e.g. `clusters/{cid}/reset`)
// accept the URL-bound id exclusively via `--cid` in everyday use, so
// the positional args slice is empty and the summary would otherwise
// leak the manifest description (foreman #131).
func destructiveSummary(c *cobra.Command, cmdDef manifest.Command, args []string) string {
	if cmdDef.Input != nil {
		idx := 0
		for _, field := range cmdDef.Input.Fields {
			if !field.Positional {
				continue
			}
			if idx < len(args) && args[idx] != "" {
				return fmt.Sprintf("%s=%s", field.Name, args[idx])
			}
			if c != nil {
				flagName := flagNameFor(field.Name)
				if c.Flags().Lookup(flagName) != nil {
					if v, _ := c.Flags().GetString(flagName); v != "" {
						return fmt.Sprintf("%s=%s", field.Name, v)
					}
				}
			}
			idx++
		}
	}
	return cmdDef.Description
}

// confirmDestructive prompts on stderr for a y/N response before a
// destructive endpoint is dispatched, or refuses outright when no
// human is available to answer. Returns nil to proceed.
//
// Contract:
//   - --yes / -y set: skip prompt, proceed. Mirrors `runos deploy`.
//   - stdin is a TTY: print prompt; user must type y/yes to proceed.
//   - stdin is NOT a TTY and --yes not set: REFUSE with a typed error
//     that names the --yes flag. This is the LLM/MCP/CI/--json case
//     the original bug (#23) called out: a no-prompt auto-proceed
//     against a typoed id is exactly the unrecoverable footgun the
//     guard exists to prevent.
//
// The non-TTY refusal differs from `runos deploy`'s confirmDeploy,
// which auto-proceeds in CI for the additive (deploy) operation.
// DELETE is destructive and never inherits CI's implicit permission.
func confirmDestructive(c *cobra.Command, cmdDef manifest.Command, args []string) error {
	if skip, _ := c.Flags().GetBool("yes"); skip {
		return nil
	}
	verb := destructiveVerb(cmdDef.Command)
	target := destructiveSummary(c, cmdDef, args)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf(
			"%s is destructive and requires confirmation. Re-run with --yes to proceed (target: %s)",
			verb, target,
		)
	}
	fmt.Fprintf(os.Stderr, "About to run `%s` against %s\n", verb, target)
	fmt.Fprintf(os.Stderr, "This is irreversible (the conductor may not be able to restore deleted state).\n")
	fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("%s cancelled", verb)
	}
	return nil
}
