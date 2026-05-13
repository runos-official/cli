// Package dynacmd builds and executes CLI commands dynamically from the API manifest.
package dynacmd

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/pflag"

	"github.com/spf13/cobra"
)

// defaultAPIKeyExpiryDays is the CLI-side fallback for the `expiresAt`
// field on `account/api-keys/add` when the user omits `--expires-at`.
// Matches the api-keys topic's recommended rotation cadence and the
// conductor's documented 90d default for `limited` keys. Pure constant
// so the regression test can assert the exact derivation.
const defaultAPIKeyExpiryDays = 90

// buildLongDescription extends cobra's Long-help with a discoverability
// note for body fields the manifest declares but the builder doesn't
// register a flag for (object-typed fields, currently). Pre-fix
// (I26-P), `runos apps update --requires <...>` was the user's
// natural reach for setting service dependencies — but `requires` is
// object-typed, has no flag form, and the only valid surface is
// `-f body.yaml`. With no flag visible in --help and no missing-
// required error firing on optional PATCH endpoints, the body-file
// path was effectively undiscoverable. The Long block now lists each
// object-typed body field with a one-line "must be supplied via -f"
// hint so `runos <verb> --help` surfaces the full input surface.
func buildLongDescription(cmdDef manifest.Command) string {
	if cmdDef.Input == nil {
		return ""
	}
	var objectFields []manifest.Field
	for _, field := range cmdDef.Input.Fields {
		if field.Type == "object" && !field.Positional {
			objectFields = append(objectFields, field)
		}
	}
	if len(objectFields) == 0 {
		return ""
	}
	var sb strings.Builder
	if cmdDef.Description != "" {
		sb.WriteString(cmdDef.Description)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Body fields without a flag form (pass via -f body.yaml):\n")
	for _, field := range objectFields {
		sb.WriteString("  ")
		sb.WriteString(field.Name)
		if field.Required {
			sb.WriteString(" (required)")
		}
		if field.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(field.Description)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatMissingFieldHint renders the per-field entry in the
// "missing required argument" error so the suggested recovery path
// matches the flag surface actually registered for the command.
//
// I26-S: pre-fix, the generic message was `pass --<flag>, or include
// <field>: in -f file` regardless of whether the flag had been
// registered. Object-typed fields like `envVars` on
// `apps/env-vars/set` have no good flag form (the builder skips the
// type switch), so the message suggested a `--env-vars` flag that
// cobra then rejected as unknown. The user landed in a loop:
// error → suggested flag → unknown-flag error → look at --help →
// neither the field nor its flag show up. Fix: probe cobra's
// registered flags before naming the flag form; when no flag exists,
// suggest the body-file path only.
func formatMissingFieldHint(c *cobra.Command, field manifest.Field, flagName string) string {
	if c.Flags().Lookup(flagName) == nil {
		return fmt.Sprintf("%s (include %s: in -f file; this field accepts no flag form, pass `-f body.yaml` with `%s: <value>`)", field.Name, field.Name, field.Name)
	}
	return fmt.Sprintf("%s (pass --%s, or include %s: in -f file)", field.Name, flagName, field.Name)
}

// applyCLIDefaults injects CLI-side fallback values into the cobra
// command before the missing-required-arg gate runs. Each entry is a
// targeted carve-out for a specific (command, field) pair where the
// conductor's `createPatCommand` / setupInstructions shape omits a
// required field for usability reasons. Currently scoped to
// `account/api-keys/add.expiresAt` (I25-AP). Explicit user input
// (positional, flag, or `-f` file) wins over any default applied here.
func applyCLIDefaults(c *cobra.Command, cmdDef manifest.Command) error {
	if cmdDef.Command != "account/api-keys/add" {
		return nil
	}
	const flagName = "expires-at"
	if c.Flags().Changed(flagName) {
		return nil
	}
	bodyFilePath, _ := c.Flags().GetString("file")
	if bodyFilePath != "" && bodyFilePresentsField(bodyFilePath, "expiresAt") {
		return nil
	}
	value := time.Now().UTC().Add(time.Duration(defaultAPIKeyExpiryDays) * 24 * time.Hour).Format(time.RFC3339)
	if err := c.Flags().Set(flagName, value); err != nil {
		return fmt.Errorf("apply default --%s: %w", flagName, err)
	}
	return nil
}

// placeholderRegex matches {name} patterns in command paths
var placeholderRegex = regexp.MustCompile(`^\{(\w+)\}$`)

// Builder builds Cobra commands from a manifest
type Builder struct {
	manifest     *manifest.Manifest
	executor     *Executor
	existingCmds map[string]*cobra.Command
}

// NewBuilder creates a new command builder
func NewBuilder(m *manifest.Manifest, executor *Executor) *Builder {
	return &Builder{
		manifest:     m,
		executor:     executor,
		existingCmds: make(map[string]*cobra.Command),
	}
}

// WithExistingCommands registers static commands that dynamic commands should merge with.
// When a manifest command has the same top-level name as an existing command, the dynamic
// subcommands will be added to the existing command rather than creating a new one.
// This allows static subcommands (like "clusters default") to coexist with dynamic ones
// (like "clusters list", "clusters show").
func (b *Builder) WithExistingCommands(cmds ...*cobra.Command) *Builder {
	for _, cmd := range cmds {
		b.existingCmds[cmd.Name()] = cmd
	}
	return b
}

// BuildCommands generates all commands from the manifest, merging with existing commands
func (b *Builder) BuildCommands() []*cobra.Command {
	// Map to track created parent commands, pre-populated with existing commands
	parents := make(map[string]*cobra.Command)
	for name, cmd := range b.existingCmds {
		parents[name] = cmd
	}

	for _, cmdDef := range b.manifest.Commands {
		b.buildCommandTree(cmdDef, parents)
		// Surface account-scoped manifest entries that live under a
		// cluster-prefixed namespace as aliases under `account/`, since
		// the user-facing namespace is misleading. Currently:
		// `clusters/mcp/show` and `clusters/mcp/update` are
		// account-scoped (`/:aid/clusters/mcp-docs`, no `:cid` in the
		// endpoint) but live under `clusters` because they describe the
		// cluster surface. Mirror them under `account/mcp/{show,update}`
		// so LLM/MCP users discover them in the right namespace.
		// Regression target: I12-B.
		for _, aliasedPath := range aliasPathsFor(cmdDef.Command) {
			aliased := cmdDef
			aliased.Command = aliasedPath
			b.buildCommandTree(aliased, parents)
		}
	}

	// Return top-level commands (excluding ones that were passed in as existing)
	var topLevel []*cobra.Command
	for path, cmd := range parents {
		if !strings.Contains(path, "/") {
			if _, wasExisting := b.existingCmds[path]; !wasExisting {
				topLevel = append(topLevel, cmd)
			}
		}
	}

	return topLevel
}

func (b *Builder) buildCommandTree(cmdDef manifest.Command, parents map[string]*cobra.Command) {
	parts := strings.Split(cmdDef.Command, "/")

	// Filter out placeholder segments like {id} - they're just metadata, not actual commands
	// The placeholder value comes from input fields as flags (e.g., --id)
	var filteredParts []string
	for _, part := range parts {
		if !placeholderRegex.MatchString(part) {
			filteredParts = append(filteredParts, part)
		}
	}

	// Build parent chain
	var currentPath string
	var parent *cobra.Command

	for i, part := range filteredParts {
		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		isLeaf := i == len(filteredParts)-1

		if existing, ok := parents[currentPath]; ok {
			parent = existing
			continue
		}

		// Skip when the parent already has a child of this name. Avoids
		// `runos config get` (static CLI config) being shadowed by a
		// duplicate dynamic `config get` (system-config from manifest):
		// without this, two leaf commands of the same name attach to the
		// merged parent and cobra picks one nondeterministically.
		if parent != nil {
			alreadyExists := false
			for _, existingChild := range parent.Commands() {
				if existingChild.Name() == part {
					alreadyExists = true
					parents[currentPath] = existingChild
					parent = existingChild
					break
				}
			}
			if alreadyExists {
				continue
			}
		}

		var cmd *cobra.Command
		if isLeaf {
			// Leaf command - has the actual execution logic
			cmd = b.buildLeafCommand(part, cmdDef)
		} else {
			// Intermediate command - just a container
			cmd = &cobra.Command{
				Use:   part,
				Short: "Manage " + part,
			}
		}

		parents[currentPath] = cmd

		if parent != nil {
			parent.AddCommand(cmd)
		}

		parent = cmd
	}
}

func (b *Builder) buildLeafCommand(name string, cmdDef manifest.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   b.buildUseLine(name, cmdDef),
		Short: cmdDef.Description,
		Long:  buildLongDescription(cmdDef),
		// SilenceUsage on runtime errors: the user got far enough to dispatch,
		// so the long usage block is noise. Without this, every API error
		// (404, 409 dependents refusal, etc.) printed the full --help text
		// after the actual error, drowning the diagnostic. The error itself
		// is still printed by cobra (not silenced) and propagated to the
		// process exit code by main.Execute.
		SilenceUsage: true,
		// Refuse extra positional args. Cobra defaults to ArbitraryArgs, which
		// silently swallowed typos like `app-info rrc nonsenseflag` (I11-X).
		// The cap is the number of positional fields declared on the command,
		// plus one slot per convenience-positional mapping (I13-H: certain
		// query-style commands like `tools/domain-check` accept their primary
		// required string field as a positional too, even though the manifest
		// keeps the field non-positional).
		Args: cobra.MaximumNArgs(countPositionalFields(cmdDef) + len(conveniencePositionalFields(cmdDef))),
		RunE: func(c *cobra.Command, args []string) error {
			// wrapJSONIfSet routes pre-network refusals raised inside this
			// RunE through the standard JSON envelope when --json is set,
			// so missing-positional / missing-required errors lands on the
			// same machine-parseable contract as API errors. The R1 fix
			// (I11-B) wrapped errors raised inside executor.Execute, but
			// missed the refusals raised here in the RunE itself; the
			// retest carry-forward closes that gap.
			wrapJSONIfSet := func(err error) error {
				if err == nil {
					return nil
				}
				if jsonOutput, _ := c.Flags().GetBool("json"); jsonOutput {
					return emitJSONErrorAndSilence(c, err)
				}
				return err
			}

			// I25-AP: when conductor's apps_add response surfaces a
			// `createPatCommand` to copy-paste, it bakes the command
			// shape but omits `--expires-at` because PAT expiry is
			// caller-policy, not server-policy. A verbatim copy-paste
			// of `runos account api-keys add --name <X>` previously
			// failed at the missing-required gate. The api-keys topic
			// recommends 90 days as the rotation cadence; default the
			// flag to that value when the user doesn't pass one and
			// no `-f` file supplies it. Explicit `--expires-at` and
			// `-f body.yaml` carrying `expiresAt:` both win, so the
			// default is a fallback, not a clobber.
			if err := applyCLIDefaults(c, cmdDef); err != nil {
				return wrapJSONIfSet(err)
			}

			// Check if required positional args are missing.
			// `cid` is special: even when declared positional+required, it
			// can also be supplied via --cid or the default cluster in
			// config. Skip the positional-missing check for cid; the
			// executor resolves it from positional/flag/config default and
			// produces a single clear error if all three are empty.
			//
			// Other required positional fields (e.g. `id` on `apps
			// status`) now ALSO accept a `--<name>` flag form (see
			// addFieldFlags). The missing-arg check honours either
			// shape: only fail when neither the positional slot nor the
			// flag carry a value. This closes the 5d gap where
			// `runos apps status --id y2w1y` was rejected as
			// `unknown flag` because the flag wasn't registered.
			if cmdDef.Input != nil {
				// `-f body.yaml` is the third way to supply a required
				// positional field (alongside the positional slot and the
				// `--<name>` flag). When the body file carries the field,
				// the executor already prefers it for endpoint substitution
				// (see executor.buildEndpoint), so the missing-arg check
				// must honour it too. Closes I6-D.
				bodyFilePath, _ := c.Flags().GetString("file")
				// I12-A: collect EVERY missing required arg before failing
				// rather than aborting on the first. Previously the user
				// had to re-run the command for each missing positional
				// to discover the next one, which an LLM iterating
				// against an unfamiliar command can't easily recover from.
				// Each missing entry names the field plus its accepted
				// alternate slots so the user can pick the easiest path.
				var missing []string
				var enumField *manifest.Field
				argIndex := 0
				for i := range cmdDef.Input.Fields {
					field := cmdDef.Input.Fields[i]
					if field.Positional {
						if argIndex >= len(args) && field.Required {
							if field.Name == "cid" {
								argIndex++
								continue
							}
							// `user/permissions` defaults the `uid`
							// positional to the caller's own Firebase
							// uid when omitted (executor injects from
							// the JWT). Skip the missing-required check
							// so the executor's injection can fire.
							// Regression target: I12-D.
							if cmdDef.Command == "user/permissions" && field.Name == "uid" {
								argIndex++
								continue
							}
							flagName := flagNameFor(field.Name)
							if c.Flags().Changed(flagName) {
								argIndex++
								continue
							}
							if bodyFileProvidesField(bodyFilePath, field.Name) {
								argIndex++
								continue
							}
							// Surface enum-options on the *first* missing
							// enum-bound positional, since the user
							// probably wants to know the options before
							// continuing. The full missing-list still
							// renders for non-enum gaps below.
							if len(field.Enum) > 0 && enumField == nil {
								enumField = &field
							}
							missing = append(missing, fmt.Sprintf("%s (or pass --%s, or include %s: in -f file)", field.Name, flagName, field.Name))
						}
						argIndex++
					}
				}
				// Required non-positional fields used to be enforced by
				// cobra's MarkFlagRequired, which fired before -f was
				// consulted. The leaf check now consults all three sources
				// (positional, flag, -f) so users can supply required
				// values exclusively through `-f body.yaml`. Closes I11-A.
				//
				// Convenience-positional mappings (I13-H) extend the slot
				// set: `tools/domain-check example.com` maps the trailing
				// positional to body["domain"], so the missing-required
				// check has to honour an extra positional arg even though
				// the manifest field isn't marked positional.
				conveniencePos := conveniencePositionalFields(cmdDef)
				convenienceStart := argIndex
				for _, field := range cmdDef.Input.Fields {
					if field.Positional || !field.Required {
						continue
					}
					flagName := flagNameFor(field.Name)
					if c.Flags().Changed(flagName) {
						continue
					}
					if bodyFileProvidesField(bodyFilePath, field.Name) {
						continue
					}
					// AllowEmpty fields (I13-K) treat an empty-string
					// in the -f file as a meaningful value (e.g.
					// `name: ""` on nodes/rename clears the display
					// name back to bootstrap). bodyFileProvidesField
					// returns false on empty strings, so without this
					// carve-out the missing-required gate would refuse
					// the user's deliberate clear-attempt.
					if field.AllowEmpty && bodyFilePresentsField(bodyFilePath, field.Name) {
						continue
					}
					if conveniencePositionalSatisfies(field.Name, conveniencePos, args, convenienceStart) {
						continue
					}
					missing = append(missing, formatMissingFieldHint(c, field, flagName))
				}
				if len(missing) > 0 {
					if enumField != nil && len(missing) == 1 {
						return showEnumOptions(c, *enumField)
					}
					if len(missing) == 1 {
						return wrapJSONIfSet(fmt.Errorf("missing required argument: %s", missing[0]))
					}
					return wrapJSONIfSet(fmt.Errorf("missing %d required arguments:\n  - %s", len(missing), strings.Join(missing, "\n  - ")))
				}
			}
			return b.executor.Execute(c, args, cmdDef)
		},
	}

	// Add flags from input schema
	if cmdDef.Input != nil {
		addFieldFlags(cmd, cmdDef.Input.Fields, cmdDef.Command)
		addBoolFlags(cmd, cmdDef.Input.Flags)
	}

	// Add per-command extra fields the conductor accepts on the wire but
	// hasn't advertised in the manifest yet (I24-U). Re-uses
	// addFieldFlags so the registered flag behaves identically to a
	// manifest-declared field — kebab-casing, type-aware default, etc.
	if extras := extraFieldsFor(cmdDef.Command); len(extras) > 0 {
		addFieldFlags(cmd, extras, cmdDef.Command)
	}

	// Register -f / --file on every dynacmd command for uniform surface.
	// Pre-I24-G the flag was gated on `hasNonPositionalInput` to avoid
	// silently ignoring file content on body-less commands (I11-U), but
	// the side effect was that `apps replace-manifest -f /tmp/x.json`
	// (a natural shape: the verb name "replace-manifest" reads like it
	// takes a manifest body) errored with "unknown flag: --file" which
	// reads as "this CLI doesn't support body files" rather than the
	// true state ("this specific command has no body input"). The flag
	// is now always present; the I11-U doctrine is preserved by
	// rejecting non-empty file values in the executor when the command
	// has no body fields, with a typed error naming the alternative.
	if cmdDef.Input != nil {
		cmd.Flags().StringP("file", "f", "", "YAML file with input values")
	}

	// Accept camelCase flag spellings as aliases for their kebab forms.
	// Manifest field names are camelCase (`overrideId`, `appId`,
	// `clusterDomainId`); the CLI registers their kebab equivalents via
	// flagNameFor (`--override-id`, `--app-id`, `--cluster-domain-id`).
	// MCP/tool docs quote the camelCase shape, so users translating an
	// MCP example to a CLI invocation otherwise hit "unknown flag" on
	// `--overrideId`. NormalizeFunc maps the camelCase form back to the
	// canonical kebab name at lookup time. Regression target: I9-L.
	//
	// Additionally, when the command has exactly ONE positional `*Id`
	// field, `--id` is accepted as an alias for the canonical kebab form
	// (`--job-id` / `--override-id` / `--integration-id` etc.). CLI
	// convention across most other surfaces is `--id`; without this
	// alias, `jobs show --id` and `apps overrides update --id <oid>`
	// rejected the flag despite being the obvious natural form for an
	// LLM/MCP user. Skipped when multiple `*Id` positionals exist (e.g.
	// `integrations/dns/records` takes both `integrationId` and `zoneId`)
	// to avoid ambiguity. Regression target: I12-L (jobs show), I12-I
	// (notify-keys uses `id` natively so this is a no-op there but stays
	// consistent).
	primaryIDAlias := primaryPositionalIDField(cmdDef)
	cmd.Flags().SetNormalizeFunc(makeFlagNormalizer(primaryIDAlias))

	// Add --cid flag for cluster ID (if endpoint uses :cid). The
	// `cluster-domains/show` command is an exception: its endpoint
	// `/:aid/cluster-domains/:id` is global, but the synthetic per-cluster
	// `runos` id is unreachable without a cluster scope, so the executor
	// refuses `--id runos` with a redirect. Register `--cid` here too so
	// the natural form `cluster-domains show --id runos --cid <cid>`
	// reaches the executor's refusal (the value itself is unused on the
	// global endpoint). Regression target: I11-W R2.
	if strings.Contains(cmdDef.Endpoint, ":cid") || cmdDef.Command == "cluster-domains/show" || cmdDef.Command == "cluster-domains/{id}/show" {
		cmd.Flags().String("cid", "", "Cluster ID (uses default from config if not specified)")
	}
	// Compose a single FlagErrorFunc that handles two unknown-flag cases
	// with self-documenting hints:
	//
	//   1. `--cid` on an account-scoped command (no `:cid` in endpoint)
	//      → "this command is account-scoped; --cid does not apply"
	//      (I12-S).
	//   2. `--id` on a command with multiple positional `*Id` fields
	//      (where the I12-L alias deliberately can't fire) → list the
	//      canonical kebab forms the user should pick from (I13-D).
	//
	// Either condition may apply to a given command; both compose into
	// the same closure so cobra only sees one FlagErrorFunc per leaf.
	accountScoped := !strings.Contains(cmdDef.Endpoint, ":cid") && cmdDef.Command != "cluster-domains/show" && cmdDef.Command != "cluster-domains/{id}/show"
	multiIDFlags := multiPositionalIDFlagNames(cmdDef)
	if accountScoped || len(multiIDFlags) > 0 {
		cmd.SetFlagErrorFunc(makeFlagErrorFunc(accountScoped, multiIDFlags))
	}

	// Add --json flag for JSON output
	// `--json` ships with `-j` shorthand to match the static commands
	// (`apps_pull`, `services_pull`, `apps_sync`, etc.) and the jq-adjacent
	// muscle memory most CLI users carry. Regression target: I13-B.
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")

	// Add --follow flag for commands that return jobs (detected by jobId in output)
	if hasJobIdOutput(cmdDef) {
		cmd.Flags().Bool("follow", false, "Follow job progress until completion")
	}
	// I14-E + I17-F: pod-logs readers (apps/logs and every
	// services/<type>/{id}/logs) get a separate `--follow` that does
	// poll-and-stream of new log lines (the conductor endpoints are
	// single-shot GETs; the executor polls every few seconds and
	// dedupes by timestamp). No `-f` shorthand: `-f` is already taken
	// by the generic `--file` body-input flag registered on every
	// command with non-positional inputs. The kubectl-mental-model
	// user types `--follow` (full word) here, distinct from the
	// job-follow path above. I17-F extended the original I14-E fix
	// (apps/logs only) to every service-type logs reader so the
	// surface is uniform — `runos services postgresql logs --follow`
	// behaves like `runos apps logs --follow`.
	if isPodLogsCommand(cmdDef.Command) {
		cmd.Flags().Bool("follow", false, "Stream new log lines (polls every 2s; ^C to exit)")
	}

	return cmd
}

func (b *Builder) buildUseLine(name string, cmdDef manifest.Command) string {
	useLine := name

	if cmdDef.Input == nil {
		return useLine
	}

	// Add positional args to use line. `cid` is rendered with the
	// optional bracket form because `--cid` and the default-cluster
	// config both satisfy the same slot, so using `<cid>` here was
	// misleading users into thinking the positional was the only path.
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			if field.Required && field.Name != "cid" {
				useLine += " <" + field.Name + ">"
			} else {
				useLine += " [" + field.Name + "]"
			}
		}
	}

	return useLine
}

// flagNameFor returns the kebab-case flag form of a manifest field
// name. The body's JSON key still uses the original (camelCase)
// `field.Name`; only the user-facing flag spelling differs. So the
// manifest field `publicRead` registers as `--public-read` while the
// outgoing request body still carries `{"publicRead": true}`. Single
// lowercase words pass through unchanged. Idempotent for already-kebab
// inputs.
//
// Regression target: I2-3c (TEST_LOG.md). Prior to this helper the
// builder registered manifest fields verbatim, producing camelCase
// flags (`--publicRead`) that fought every other RunOS CLI flag's
// kebab-case convention.
// countPositionalFields returns the number of positional fields declared
// on a command's input. Used to cap the maximum allowed positional args
// (I11-X) so typos like `app-info rrc nonsenseflag` are rejected with a
// cobra "accepts at most N arg(s)" error instead of silently being
// ignored. Commands with no input schema or no positional fields return
// zero, which still satisfies `cobra.MaximumNArgs(0)`.
func countPositionalFields(cmdDef manifest.Command) int {
	if cmdDef.Input == nil {
		return 0
	}
	n := 0
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			n++
		}
	}
	return n
}

// hasNonPositionalInput reports whether a command takes any input the
// `-f` body file could supply. Returns true if any field is
// non-positional or if any boolean flags exist. Used to gate `-f`
// registration so commands whose only inputs are path params (e.g.
// `apps replace-manifest <id>`) don't expose an inert `-f` slot that
// silently ignored user files. Regression target: I11-U.
func hasNonPositionalInput(cmdDef manifest.Command) bool {
	if cmdDef.Input == nil {
		return false
	}
	for _, field := range cmdDef.Input.Fields {
		if !field.Positional {
			return true
		}
	}
	return len(cmdDef.Input.Flags) > 0
}

// flagNameFor returns the kebab-case flag form of a manifest field
// name with acronym awareness. Pre-fix the naive "insert dash before
// any uppercase" rule split runs of consecutive uppercase letters into
// per-letter tokens: the manifest field `minIOOsid` (where `MinIO` is
// a brand-name acronym) rendered as `--min-i-o-osid`, an unguessable
// flag form that an LLM/MCP user copying the canonical camelCase
// couldn't translate. The acronym-aware rule treats consecutive
// uppercase letters as one word and only inserts a dash:
//
//   - At the lowercase→uppercase boundary (`appId` → `app-id`).
//   - At the acronym→title boundary, i.e. between two uppercase
//     letters when the second one is followed by a lowercase letter
//     (`URLPath` → `url-path`, `JSONResponse` → `json-response`,
//     `minIOOsid` → `min-io-osid`).
//
// Consecutive uppercase runs at the end of the string stay together
// (`publicURL` → `public-url`, `IOID` → `ioid`). The result matches
// what golang.org/x/text/cases and similar idiomatic Go converters
// produce. Regression target: I2-3c (TEST_LOG.md), I13-D / I13-G
// (acronym handling for `minIOOsid` and similar).
func flagNameFor(name string) string {
	if name == "" {
		return name
	}
	runes := []rune(name)
	isUpper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	isLower := func(r rune) bool { return r >= 'a' && r <= 'z' }
	toLower := func(r rune) rune {
		if isUpper(r) {
			return r + ('a' - 'A')
		}
		return r
	}
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && isUpper(r) {
			prev := runes[i-1]
			switch {
			case isLower(prev):
				// Lowercase→uppercase boundary always splits.
				b.WriteByte('-')
			case isUpper(prev) && i+1 < len(runes) && isLower(runes[i+1]):
				// Acronym→title boundary: `URLPath` -> "url-path",
				// the `P` starts a new word because the next char
				// is lowercase. Without this rule "URLPath" would
				// produce "urlpath".
				b.WriteByte('-')
			}
		}
		b.WriteRune(toLower(r))
	}
	return b.String()
}

// normalizeCamelToKebab is a pflag NormalizeFunc that maps camelCase
// flag names to their canonical kebab form. Used so a user copying an
// MCP-doc example like `overrideId: xxx` into a CLI invocation as
// `--overrideId xxx` lands on the registered `--override-id` flag
// instead of hitting "unknown flag". The kebab form remains canonical
// for help text and conflict resolution; camelCase is purely an alias
// at lookup time. Regression target: I9-L.
func normalizeCamelToKebab(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	if !containsUpper(name) {
		return pflag.NormalizedName(name)
	}
	return pflag.NormalizedName(flagNameFor(name))
}

// primaryPositionalIDField returns the kebab-flag name of a command's
// single positional `*Id` field (e.g. "job-id" for `jobs/show`,
// "override-id" for `apps/overrides/show`). Used to install a
// `--id → <kebab>` alias so the natural CLI convention works on
// dynacmd commands whose manifest field name is `<resource>Id`.
//
// Returns "" in three cases, all of which would make the alias unsafe
// or ambiguous:
//
//  1. No `*Id` positional (e.g. `notify-keys/update` already has a
//     plain `id` field that's reachable as `--id` natively).
//  2. More than one `*Id` positional (`jobs/workitem-logs` has both
//     `jobId` and `workItemId`; `integrations/dns/records` has
//     `integrationId` and `zoneId`). Pick the wrong one and the user
//     silently targets a different resource.
//  3. The command ALSO has a plain `id` positional alongside the
//     `*Id` field (`apps/overrides/{show,update,delete}` carry `id`
//     (the app) plus `overrideId`; `apps/prepare-cli-pull` carries
//     `id` (the app) plus `cliUploadId`). Aliasing `--id` to
//     `--override-id` here would clobber the natural app-id slot
//     and silently retarget the call. Force the user to spell the
//     secondary id with its full kebab name in these cases.
//
// Regression target: I12-L.
func primaryPositionalIDField(cmdDef manifest.Command) string {
	if cmdDef.Input == nil {
		return ""
	}
	var matches []string
	hasPlainID := false
	for _, field := range cmdDef.Input.Fields {
		if !field.Positional {
			continue
		}
		if field.Name == "id" {
			hasPlainID = true
			continue
		}
		if field.Name == "cid" {
			continue
		}
		if strings.HasSuffix(field.Name, "Id") {
			matches = append(matches, flagNameFor(field.Name))
		}
	}
	if hasPlainID || len(matches) != 1 {
		return ""
	}
	return matches[0]
}

// aliasPathsFor returns the alternate manifest paths a given command
// should also surface under. The use-case is hand-fixing namespace
// misclassifications without forcing a conductor manifest rename. The
// alias receives a separate cobra command tree but shares the original
// command's endpoint, so both invocations hit the same handler.
// Regression target: I12-B (`clusters/mcp/{show,update}` exposed under
// `account/mcp/{show,update}` since the endpoint is account-scoped).
func aliasPathsFor(originalPath string) []string {
	switch originalPath {
	case "clusters/mcp/show":
		return []string{"account/mcp/show"}
	case "clusters/mcp/update":
		return []string{"account/mcp/update"}
	}
	return nil
}

// conveniencePositionalSatisfies reports whether the given field name
// is satisfied by a convenience-positional arg supplied at the trailing
// slot. fieldName must appear in the conveniencePos allow-list, and the
// args slice must carry a value at the corresponding offset for the
// check to fire. Used by the leaf RunE's missing-required gate so
// `tools/domain-check example.com` is not refused for a missing
// `--domain` value. Regression target: I13-H.
func conveniencePositionalSatisfies(fieldName string, conveniencePos []string, args []string, start int) bool {
	for offset, mapped := range conveniencePos {
		if mapped != fieldName {
			continue
		}
		idx := start + offset
		if idx >= len(args) {
			return false
		}
		return args[idx] != ""
	}
	return false
}

// conveniencePositionalFields returns the manifest field names a
// command accepts as positional arguments even though the manifest
// keeps the field non-positional. Used to wire `runos tools domain-check
// example.com` (positional) alongside the canonical
// `--domain example.com` form (I13-H), without forcing a conductor
// manifest change. Returned in declaration order, mapped to body keys.
// Empty for every other command. Kept as an explicit allow-list so
// generic refactors don't accidentally widen the surface.
func conveniencePositionalFields(cmdDef manifest.Command) []string {
	switch cmdDef.Command {
	case "tools/domain-check":
		return []string{"domain"}
	}
	return nil
}

// multiPositionalIDFlagNames returns the canonical kebab-flag names of
// EVERY positional `*Id` field on a command with two or more such
// fields. Returns nil for single-`*Id` commands (the I12-L alias
// already maps `--id` to the only canonical kebab name there) and for
// commands with no `*Id` positional. Used to surface a hint on
// `--id`-rejected errors so the user knows which kebab name to type.
// Regression target: I13-D.
func multiPositionalIDFlagNames(cmdDef manifest.Command) []string {
	if cmdDef.Input == nil {
		return nil
	}
	var names []string
	for _, field := range cmdDef.Input.Fields {
		if !field.Positional || field.Name == "cid" || field.Name == "id" {
			continue
		}
		if strings.HasSuffix(field.Name, "Id") {
			names = append(names, "--"+flagNameFor(field.Name))
		}
	}
	if len(names) < 2 {
		return nil
	}
	return names
}

// makeFlagErrorFunc composes the account-scoped `--cid` hint (I12-S)
// and the multi-positional `--id` hint (I13-D) into one
// SetFlagErrorFunc closure. Cobra accepts a single FlagErrorFunc per
// command, so when both conditions apply (e.g. an account-scoped
// command that also has multiple positional `*Id` fields, like
// `integrations/dns/records`), the same closure handles both. Every
// other parse error passes through unchanged so legitimate diagnostics
// aren't masked.
func makeFlagErrorFunc(accountScoped bool, multiIDFlags []string) func(*cobra.Command, error) error {
	return func(cmd *cobra.Command, err error) error {
		if err == nil {
			return nil
		}
		msg := err.Error()
		if accountScoped && (strings.Contains(msg, "unknown flag: --cid") || (strings.Contains(msg, "unknown shorthand flag:") && strings.Contains(msg, "cid"))) {
			return fmt.Errorf("%s\n\n`%s` is account-scoped (no `:cid` in endpoint); --cid does not apply. Drop the flag, or check the command's docs for the per-cluster sibling.", msg, cmd.CommandPath())
		}
		if len(multiIDFlags) > 0 && strings.Contains(msg, "unknown flag: --id") {
			return fmt.Errorf("%s\n\n`%s` has multiple id positionals; --id is ambiguous here. Pass one of the canonical flag names: %s.", msg, cmd.CommandPath(), strings.Join(multiIDFlags, ", "))
		}
		return err
	}
}

// makeFlagNormalizer composes the camelCase→kebab normaliser with an
// optional `id → <primaryID>` alias. When primaryIDAlias is non-empty,
// any user-typed `--id` / `id` lookup is redirected to that kebab name
// (e.g. `--job-id` on jobs/show, `--override-id` on apps/overrides/*).
// Other names fall through to the camelCase→kebab rewrite. pflag accepts
// exactly one NormalizeFunc per FlagSet, so the combined behaviour ships
// as a single closure. Regression target: I12-L.
func makeFlagNormalizer(primaryIDAlias string) func(*pflag.FlagSet, string) pflag.NormalizedName {
	return func(fs *pflag.FlagSet, name string) pflag.NormalizedName {
		if primaryIDAlias != "" && (name == "id" || name == "Id" || name == "ID") {
			return pflag.NormalizedName(primaryIDAlias)
		}
		return normalizeCamelToKebab(fs, name)
	}
}

// containsUpper reports whether s has any ASCII uppercase rune. Used
// by normalizeCamelToKebab as a fast no-op gate.
func containsUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// sanitizeFlagDescription strips Markdown backticks before passing a
// description to pflag. pflag interprets the first backtick-quoted word
// in a flag's Usage as a value-type override (an undocumented feature
// meant for descriptions like "use `int` here"); manifest descriptions
// use backticks as plain Markdown code formatting, so without stripping
// them a description like `"... the CLI `--add` string-list flag ..."`
// renders as `--add --add` in the flag header (the second `--add` lands
// in the column where `strings` normally appears). Regression target:
// I6-H.
func sanitizeFlagDescription(s string) string {
	return strings.ReplaceAll(s, "`", "")
}

// isPodLogsCommand reports whether the manifest command path projects a
// pod-logs reader whose shape matches apps/logs: array of
// `{timestamp, podName, containerName, message}` entries, with
// `tail` + `since` body fields and a `previous` flag. Pre-I17-F this
// match was hard-coded to `apps/logs`; every services/<type>/{id}/logs
// command has the same wire shape but missed the `--follow` poll-loop
// and the duration-string `--since` widening. Centralising the match
// here lets `buildLeafCommand`, `flagTypeOverride`, `descriptionSuffixFor`,
// and the executor's diagnostic + follow gates all agree on a single
// allow-list without re-listing service types. Regression target: I17-F.
func isPodLogsCommand(cmdPath string) bool {
	if cmdPath == "apps/logs" {
		return true
	}
	return strings.HasPrefix(cmdPath, "services/") && strings.HasSuffix(cmdPath, "/{id}/logs")
}

// flagTypeOverride returns a non-empty string when a specific
// (command, field) pair should be registered with a different cobra
// flag type than the manifest's declared type. Currently used for
// integer fields that accept additional CLI-side shapes: pod-logs
// `since` is wire-side `int seconds` but the CLI registers it as a
// string flag so `--since 5m` (duration) and `--since 300` (int)
// both parse without cobra's `strconv.ParseInt` raw error firing on
// the duration form. The executor's collectInput re-parses the string
// value at body-build time. Returns "" for the common case so generic
// flag registration runs unchanged. Regression targets: I14-F, I17-F.
func flagTypeOverride(cmdPath, fieldName string) string {
	if isPodLogsCommand(cmdPath) && fieldName == "since" {
		return "string"
	}
	return ""
}

// extraFieldsFor returns CLI-side body fields the manifest doesn't
// declare but the conductor still accepts on the wire. These get
// registered as kebab-cased flags + copied into the request body when
// set, mirroring the generic addFieldFlags / collectInput path so a
// flag form is available alongside the existing `-f body.yaml` path.
//
// Use sparingly. The default invariant is that the manifest is the
// source of truth for both the wire body and the flag surface; this
// hook exists only for fields the conductor accepts but hasn't yet
// advertised in the manifest schema. Each entry should have a
// matching follow-up to land the field in the manifest, after which
// the entry can be removed (the generic path will pick the field up
// for free).
//
// Empty by default: every conductor-accepted body field should land
// in the manifest schema instead of being maintained here. The I24-U
// entry for `apps_add --provision-ci-variables` was removed in
// conductor 15.0.0 (the auto-provision flow itself was rolled back as
// a design decision; PAT setup is now a one-time manual step). Leave
// the hook in place so the next field that arrives ahead of its
// manifest entry has a documented home.
func extraFieldsFor(_ string) []manifest.Field {
	return nil
}

// descriptionSuffixFor appends CLI-side context to specific manifest-driven
// flag descriptions where the manifest's text doesn't capture a behaviour
// the user needs to know at flag-discovery time. Returned suffix is appended
// verbatim to `sanitizeFlagDescription(field.Description)` so it shows up
// in `--help`, completion descriptions, and any tooling that reads
// `flag.Usage`. Kept as a small explicit allow-list to avoid drift.
// Regression target: I11-R (`apps logs --tail 0` floors to 1 line at the
// k8s API; surface the floor at flag-discovery time so CI / LLM callers
// don't think `--tail 0` returns zero rows).
func descriptionSuffixFor(cmdPath, fieldName string) string {
	switch {
	case isPodLogsCommand(cmdPath) && fieldName == "tail":
		return " (floors to 1 line at the k8s API minimum; pass 100+ for meaningful history)"
	}
	return ""
}

func addFieldFlags(cmd *cobra.Command, fields []manifest.Field, cmdPath string) {
	for _, field := range fields {
		// Positional fields are also exposed as flags so users can write
		// `runos apps status --id y2w1y` interchangeably with the
		// positional form. The endpoint builder already prefers the
		// positional arg when both are present and falls back to the
		// flag value otherwise. Skip enum/required gating for the flag
		// variant: required-ness is still enforced by the per-leaf
		// missing-arg check (which now also consults the flag), and
		// MarkFlagRequired would force the flag form even when the
		// positional was supplied.
		//
		// `cid` is special: every command whose endpoint contains
		// `:cid` already gets a dedicated `--cid` flag registered
		// below in buildLeafCommand. Don't double-register.
		flagName := flagNameFor(field.Name)
		description := sanitizeFlagDescription(field.Description) + descriptionSuffixFor(cmdPath, field.Name)
		if field.Positional {
			if field.Name == "cid" {
				continue
			}
			// Register a `--<name>` flag form for every type of positional
			// field, not just strings. Pre-fix, integer-typed positionals
			// (e.g. `account/notify-keys/update --id 42`) had to be
			// supplied positionally because no flag was registered. The
			// flag form is symmetric with string-typed positionals and
			// keeps the surface uniform for LLM/MCP consumers. Regression
			// target: I12-I.
			switch field.Type {
			case "string":
				cmd.Flags().String(flagName, "", description)
			case "integer":
				cmd.Flags().Int(flagName, 0, description)
			case "boolean":
				cmd.Flags().Bool(flagName, false, description)
			}
			continue
		}

		// Some integer fields accept additional surface shapes from the
		// CLI side that the manifest's bare `integer` type doesn't
		// describe (e.g. `apps/logs.since` accepts a `--since 5m` duration
		// string AND a `--since 300` integer-seconds form; the manifest
		// is int-only because the wire body has to land as int). For
		// those pairs register the flag as a STRING so cobra accepts
		// either shape, and let collectInput convert at body-build time.
		// Regression target: I14-F.
		if flagTypeOverride(cmdPath, field.Name) == "string" {
			cmd.Flags().String(flagName, "", description)
			continue
		}
		switch field.Type {
		case "string":
			defaultVal := ""
			if field.Default != nil {
				if v, ok := field.Default.(string); ok {
					defaultVal = v
				}
			}
			cmd.Flags().String(flagName, defaultVal, description)

		case "integer":
			defaultVal := 0
			if field.Default != nil {
				switch v := field.Default.(type) {
				case int:
					defaultVal = v
				case float64:
					defaultVal = int(v)
				}
			}
			cmd.Flags().Int(flagName, defaultVal, description)

		case "array":
			// I25-B: pflag's StringSlice splits on commas inside the value,
			// so `--service-port-mappings '[{"port":3000,"standardHttps":true}]'`
			// failed with `parse error on line 1, column 2: bare " in
			// non-quoted-field`. StringArray takes each `--flag <value>`
			// invocation verbatim (no CSV splitting). The executor then
			// JSON-coerces the elements so a single `--flag '[{...},{...}]'`
			// expands to the full array on the wire, while
			// `--flag a --flag b` stays a string list as before.
			cmd.Flags().StringArray(flagName, nil, description)

		case "boolean":
			defaultVal := false
			if field.Default != nil {
				if v, ok := field.Default.(bool); ok {
					defaultVal = v
				}
			}
			cmd.Flags().Bool(flagName, defaultVal, description)
		}

		// Don't call cmd.MarkFlagRequired here. Cobra's required-flag check
		// fires before our RunE, so a user supplying every required value
		// through `-f body.yaml` got rejected with `required flag(s) ...
		// not set` and the file was never inspected (I11-A). The leaf's
		// own missing-input check (in buildLeafCommand RunE) honours -f,
		// positional slots, and the explicit flag form together, so the
		// required contract is still enforced — just after the file is
		// considered.
		_ = field.Required
	}
}

func addBoolFlags(cmd *cobra.Command, flags []manifest.Flag) {
	for _, flag := range flags {
		cmd.Flags().Bool(flagNameFor(flag.Name), flag.Default, sanitizeFlagDescription(flag.Description))
	}
}

func showEnumOptions(cmd *cobra.Command, field manifest.Field) error {
	fmt.Printf("Available options for <%s>:\n\n", field.Name)
	for _, option := range field.Enum {
		fmt.Printf("  %s\n", option)
	}
	fmt.Printf("\nUsage: %s <%s>\n", cmd.CommandPath(), field.Name)
	return nil
}

func hasJobIdOutput(cmdDef manifest.Command) bool {
	if cmdDef.Output == nil {
		return false
	}
	for _, field := range cmdDef.Output.Fields {
		if field.Name == "jobId" {
			return true
		}
	}
	return false
}
