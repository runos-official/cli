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
	// A REQUIRED BOOLEAN MUST NAME ITS FALSE FORM (goal 21, O21). Saying only "pass --vm-host"
	// reads as though the flag can only ever set true, so opting a node OUT of hosting VMs, which
	// is half of what configure-virt-shape is for, looks impossible from the CLI. `--flag=false`
	// has always worked and appeared in neither the help nor this error. The failure is silent in
	// the worst direction: an agent that concludes it cannot be set false simply does not do it.
	if field.Type == "boolean" {
		return fmt.Sprintf("%s (pass --%s=true or --%s=false, or include %s: in -f file)",
			field.Name, flagName, flagName, field.Name)
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
//
// foreman #132: when the user passes --expires-at explicitly, also
// validate that the value carries a timezone. Bare RFC 3339 like
// "2026-08-01T00:00:00" was silently interpreted as local time by the
// server, producing keys that expired up to 14 hours off the user's
// intent on non-UTC operators. Refuse client-side with a message that
// names the two accepted shapes.
func applyCLIDefaults(c *cobra.Command, cmdDef manifest.Command) error {
	if cmdDef.Command != "account/api-keys/add" {
		return nil
	}
	const flagName = "expires-at"
	if c.Flags().Changed(flagName) {
		raw, _ := c.Flags().GetString(flagName)
		if err := validateExpiresAt(raw); err != nil {
			return err
		}
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

// validateExpiresAt refuses RFC 3339 timestamps without a timezone
// designator. time.Parse(time.RFC3339, ...) already requires a Z or
// numeric offset, so a successful parse is the contract. The error
// message names both accepted shapes so the user knows the recovery
// path. Regression target: foreman #132.
func validateExpiresAt(raw string) error {
	if raw == "" {
		return fmt.Errorf("--expires-at: empty value (use RFC 3339 with timezone, e.g. 2026-08-01T00:00:00Z or 2026-08-01T00:00:00+02:00)")
	}
	if _, err := time.Parse(time.RFC3339, raw); err != nil {
		return fmt.Errorf("--expires-at %q: must be RFC 3339 with a timezone (e.g. 2026-08-01T00:00:00Z or 2026-08-01T00:00:00+02:00); bare local-time values are refused so the key doesn't silently expire on a different wall clock", raw)
	}
	return nil
}

// placeholderRegex matches {name} patterns in command paths
var placeholderRegex = regexp.MustCompile(`^\{(\w+)\}$`)

// urlPlaceholderRegex matches `{name}` anywhere in a string (not
// anchored, unlike placeholderRegex which checks whole path segments).
// Used to extract URL-path slot names from the manifest command path
// so the builder can flip the matching field to Positional:true.
var urlPlaceholderRegex = regexp.MustCompile(`\{(\w+)\}`)

// promoteURLPlaceholderFieldsToPositional walks cmdDef.Command for
// `{<name>}` placeholders and sets Positional:true on every matching
// input field. Pre-fix this depended on conductor's manifest spelling
// (apps/{id}/show declared positional:true on `id`; services/<type>/
// {id}/show didn't), which surfaced as
// `services postgresql show lw0vp` → "accepts at most 0 arg(s)".
// Mutates cmdDef.Input.Fields in place. Idempotent: a field already
// flagged positional stays positional.
func promoteURLPlaceholderFieldsToPositional(cmdDef *manifest.Command) {
	if cmdDef == nil || cmdDef.Input == nil {
		return
	}
	matches := urlPlaceholderRegex.FindAllStringSubmatch(cmdDef.Command, -1)
	if len(matches) == 0 {
		return
	}
	placeholders := make(map[string]bool, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			placeholders[m[1]] = true
		}
	}
	for i := range cmdDef.Input.Fields {
		if placeholders[cmdDef.Input.Fields[i].Name] {
			cmdDef.Input.Fields[i].Positional = true
		}
	}
}

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

	// Normalize fields: any field whose name matches a `{<name>}`
	// placeholder in the command path is a URL-path param and MUST be
	// positional. The conductor manifest is inconsistent on this — some
	// commands declare positional:true for the URL slot (apps/{id}/show,
	// clusters/show.cid), others don't (services/<type>/{id}/show.id,
	// status, logs). Pre-fix this inconsistency surfaced as a UX
	// papercut: `services postgresql show lw0vp` errored with
	// "accepts at most 0 arg(s)" while the same shape worked for
	// `apps show`. Promote unconditionally so the surface is uniform
	// regardless of manifest spelling.
	for i := range b.manifest.Commands {
		promoteURLPlaceholderFieldsToPositional(&b.manifest.Commands[i])
	}

	for _, cmdDef := range b.manifest.Commands {
		// Remap manifest entries whose CLI namespace is misclassified
		// onto the correct one before building the tree. Currently:
		// `clusters/mcp/show|update` are account-scoped on the wire
		// (endpoint /:aid/clusters/mcp-docs, no :cid), so the CLI
		// surface lives under `account/mcp/*` instead. Only the cobra
		// tree path moves; the manifest cmdDef.Command stays untouched
		// for endpoint substitution + `manifest show <original>`
		// lookups (ResolveAliasToCanonical handles the alias direction
		// for `manifest show <new>`). Drops the duplicate registration
		// that previously surfaced both spellings (#31).
		treeCmd := cmdDef
		if remapped := canonicalCLIPathFor(cmdDef.Command); remapped != cmdDef.Command {
			treeCmd.Command = remapped
		}
		b.buildCommandTree(treeCmd, parents)
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
	parts := strings.Split(displayPathFor(cmdDef.Command), "/")

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

			// Confirmation guard for destructive endpoints. Pre-fix any
			// DELETE-method command (`apps delete`, `services postgresql
			// delete`, ...) reached the wire on the first keystroke
			// with no prompt and no --yes flag; a typo on the id was
			// unrecoverable. Mirrors `runos deploy`'s -y/--yes surface.
			// Auto-skips when stdin is not a TTY (CI) or --json is set
			// (machine consumers).
			if destructivePromptApplies(c, cmdDef) {
				if err := confirmDestructive(c, cmdDef, args); err != nil {
					return wrapJSONIfSet(err)
				}
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
				// Pre-validate the -f path so a typoed/unreadable file
				// surfaces as a clear ENOENT instead of the misleading
				// "missing required argument: <field>" the gate below
				// would otherwise emit. bodyFileProvidesField silently
				// swallows loadYAMLFile errors (returns false), letting
				// the missing-required path fire as if -f wasn't passed.
				// Validate up-front for non-stdin paths against commands
				// that actually consume body input (no-body commands
				// surface their own "flag inert" diagnostic via the
				// executor path).
				if bodyFilePath != "" && !isStdinPath(bodyFilePath) && hasNonPositionalInput(cmdDef) {
					if err := validateBodyFilePath(bodyFilePath); err != nil {
						return wrapJSONIfSet(err)
					}
				}
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
							// foreman #82: --app is a visible alias for
							// --id on apps-scoped commands; treat it as
							// satisfying the positional `id` requirement
							// here so the missing-arg gate doesn't fire
							// when the user supplied --app instead of
							// --id. The executor's conflict-detection
							// path covers the both-set case.
							if field.Name == "id" && c.Flags().Lookup("app") != nil && c.Flags().Changed("app") {
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
							hint := fmt.Sprintf("%s (or pass --%s, or include %s: in -f file)", field.Name, flagName, field.Name)
							if field.Name == "id" && c.Flags().Lookup("app") != nil {
								hint = fmt.Sprintf("%s (or pass --%s / --app, or include %s: in -f file)", field.Name, flagName, field.Name)
							}
							missing = append(missing, hint)
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

	// Register -y/--yes on destructive endpoints so users have a
	// CI-friendly opt-out from the confirmation prompt. The prompt
	// itself fires inside the RunE based on TTY detection +
	// --json/--yes flags; the flag declaration just exposes the
	// surface so cobra accepts the syntax.
	if isDestructiveCommand(cmdDef) {
		cmd.Flags().BoolP("yes", "y", false, "skip the destructive-action confirmation prompt")
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

	// foreman #80 / #82: on app-scoped commands (endpoint
	// /:aid/:cid/apps/:id[/...]), register `--app` as a visible
	// alias for the `id` positional so the natural CLI convention from
	// `runos deploy --app <id>` and `runos apps build --app <id>` (both
	// hand-coded with explicit `--app`) also works on the
	// manifest-driven app commands (`apps run`, `apps show`,
	// `apps logs`, ...). Scoped to /apps/:id endpoints so
	// services/<type>/show (where `id` is a service id, not an app id)
	// still errors with "unknown flag: --app". Registering as a real
	// flag (rather than the #80 normalizer-only redirect) makes the
	// alias visible in `--help`, addressing the #82 follow-up. The
	// existing `--id` registration in addFieldFlags is untouched, so
	// `--id` keeps working for back-compat. Conflict resolution (both
	// flags set with different values) fires in the executor's input
	// path, not here.
	if isAppsScopedCommand(cmdDef) {
		cmd.Flags().String("app", "", "Application ID (alias for --id; same value)")
	}

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

// flagSpellingOverrides maps a manifest field name to a preferred CLI
// flag spelling when a naive kebab of the field name diverges from the
// flag form the command's UX wants. The field name remains the wire
// body key (conductor's fixed contract); only the user-facing flag
// changes. flagNameFor consults this first, and both flag registration
// (buildLeafCommand) and flag reading (collectInput) route through
// flagNameFor, so an entry here keeps them consistent automatically.
//
// services/postgresql/clone-database: the body keys sourcePgOsid /
// sourceDatabase / targetDatabase are fixed by conductor (the async
// dispatcher spreads them straight into the orchestration's jobInput),
// but the agreed CLI shape is the shorter --source-osid / --source-db /
// --target-db. These three field names appear in no other manifest
// command, so a name-keyed override is unambiguous. sourceCid (->
// --source-cid) and owner (-> --owner) already kebab naturally and need
// no override. Regression target: clone-database flag spelling.
//
// maintenance-scripts/*/run: every script declares NODE_NID (the body key
// the script runner reads). The CLI spells the node id --nid everywhere
// else, so the flag is --nid here too (goal 23 review). No manifest
// command declares both NODE_NID and nid, so the override is unambiguous.
// legacyFlagAliases keeps the old --node_nid spelling working.
var flagSpellingOverrides = map[string]string{
	"sourcePgOsid":   "source-osid",
	"sourceDatabase": "source-db",
	"targetDatabase": "target-db",
	"NODE_NID":       "nid",
}

// legacyFlagAliases maps a flag spelling that older scripts used to the
// canonical flag. normalizeCamelToKebab consults it before the kebab
// rewrite. `node_nid` was the pre-fix rendering of NODE_NID (goal 23
// review); the override above moved that flag to --nid.
var legacyFlagAliases = map[string]string{
	"node_nid": "nid",
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
// produce. A flagSpellingOverrides hit short-circuits all of this.
// Regression target: I2-3c (TEST_LOG.md), I13-D / I13-G (acronym
// handling for `minIOOsid` and similar).
//
// `_` is a word boundary and becomes `-` (`REBOOT_TIMEOUT_SECONDS` →
// `reboot-timeout-seconds`, `shared_buffers` → `shared-buffers`). Pre-fix
// (goal 23 review) the maintenance-script and postgres/vllm config fields
// were the only underscore flags on the surface. normalizeCamelToKebab
// still accepts the underscore spelling so old scripts keep working.
func flagNameFor(name string) string {
	if name == "" {
		return name
	}
	if override, ok := flagSpellingOverrides[name]; ok {
		return override
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
		if r == '_' {
			b.WriteByte('-')
			continue
		}
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
//
// Underscore spellings (`--node_nid`, `--checkpoint_completion_target`)
// are aliases too: they were the canonical flags before flagNameFor
// learned to split on `_` (goal 23 review), and scripts written against
// them must keep working. legacyFlagAliases handles the one spelling
// that a plain kebab rewrite cannot reach (`node_nid` → `nid`).
func normalizeCamelToKebab(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	if alias, ok := legacyFlagAliases[name]; ok {
		return pflag.NormalizedName(alias)
	}
	if !containsUpper(name) && !strings.Contains(name, "_") {
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

// displayPathFor rewrites a manifest command path into the
// user-facing cobra path. The manifest is authoritative for endpoint
// semantics (executor.Execute keys off `cmdDef.Command`), but the
// command-tree shape it implies is sometimes user-hostile. Currently:
//
//   - `cli/version-check` hoists to `version-check`. The `cli` parent
//     wrapped a single subcommand with the tautological help text
//     "Manage cli", which is uninformative (the user is already in the
//     CLI) and ate a top-level slot for no payoff. Hoisting brings the
//     verb up alongside `runos update` / `runos version`, the other
//     self-management commands. The manifest entry's
//     `Command: "cli/version-check"` stays unchanged so the executor's
//     auto-injection (executor.go: localVersion + os) keeps firing.
//
// Keep the allow-list small and explicit. The default remains the
// identity mapping: a manifest path is the cobra path.
func displayPathFor(originalPath string) string {
	switch originalPath {
	case "cli/version-check":
		return "version-check"
	}
	return originalPath
}

// cliNamespaceRemap maps a manifest command path to the CLI namespace
// where it should actually live, when the manifest's namespace is
// misclassified. Single source of truth driving:
//   - canonicalCLIPathFor: tree-build uses the remapped path so only
//     the corrected spelling exists in the cobra tree.
//   - ResolveAliasToCanonical: `manifest show <new>` resolves back to
//     the manifest's original path for the JSON dump.
//
// Currently: clusters/mcp/{show,update} live at /:aid/clusters/mcp-docs
// (account-scoped — no :cid), so the CLI surfaces them under
// `account mcp *` instead of the misleading `clusters mcp *`.
// Regression target: I12-B (correct namespace) + #31 (drop the
// duplicate `clusters mcp *` registration).
var cliNamespaceRemap = map[string]string{
	"clusters/mcp/show":   "account/mcp/show",
	"clusters/mcp/update": "account/mcp/update",
}

// canonicalCLIPathFor returns the CLI tree path a manifest command
// should occupy. Defaults to the manifest path; entries in
// cliNamespaceRemap relocate to the corrected namespace.
func canonicalCLIPathFor(manifestPath string) string {
	if remapped, ok := cliNamespaceRemap[manifestPath]; ok {
		return remapped
	}
	return manifestPath
}

// ResolveAliasToCanonical returns the manifest path for a remapped CLI
// path spelling. Used by `runos manifest show <path>` so a user typing
// the corrected CLI namespace (`account/mcp/show`) gets the underlying
// manifest entry whose `command` field still reads `clusters/mcp/show`.
// Returns the input unchanged when no remap matches.
func ResolveAliasToCanonical(aliasPath string) string {
	for manifestPath, cliPath := range cliNamespaceRemap {
		if cliPath == aliasPath {
			return manifestPath
		}
	}
	return aliasPath
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
//
// `--app` no longer goes through the normalizer (foreman #82 moved it
// to a visible flag registered in buildLeafCommand). `--App` / `--APP`
// still work via the camelCase pass-through, which lowercases them to
// `app` so cobra resolves to the registered flag.
func makeFlagNormalizer(primaryIDAlias string) func(*pflag.FlagSet, string) pflag.NormalizedName {
	return func(fs *pflag.FlagSet, name string) pflag.NormalizedName {
		if primaryIDAlias != "" && (name == "id" || name == "Id" || name == "ID") {
			return pflag.NormalizedName(primaryIDAlias)
		}
		return normalizeCamelToKebab(fs, name)
	}
}

// isAppsScopedCommand reports whether a manifest command targets an
// app via the canonical `/apps/:id[/...]` endpoint shape AND its
// primary positional is named `id` (so accepting `--app` as an alias
// for `--id` is semantically right). Used by makeFlagNormalizer to
// gate the `app → id` alias to /apps/ endpoints so a
// `services/postgresql/show --id <svc>` doesn't silently accept
// `--app <svc>` and confuse the surface. Foreman #80.
func isAppsScopedCommand(cmdDef manifest.Command) bool {
	if !strings.Contains(cmdDef.Endpoint, "/apps/:id") {
		return false
	}
	if cmdDef.Input == nil {
		return false
	}
	for _, field := range cmdDef.Input.Fields {
		if field.Positional && field.Name == "id" {
			return true
		}
	}
	return false
}

// containsUpper reports whether s has any ASCII uppercase rune. Used
// by normalizeCamelToKebab (with an underscore check) as a fast no-op gate.
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
		// The manifest's own constraints go into the help text (goal 21, O1). Without this the
		// enum was declared, carried, and dropped at the last step, so the only way to learn the
		// allowed values was to send a wrong one and read the refusal: one wasted round trip per
		// enum field, every time, for every agent.
		description := sanitizeFlagDescription(field.Description) +
			describeConstraints(field) +
			descriptionSuffixFor(cmdPath, field.Name)
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
