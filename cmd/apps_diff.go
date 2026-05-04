package cmd

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/runos-official/cli/internal/apps"

	"github.com/spf13/cobra"
)

var appsDiffCmd = &cobra.Command{
	Use:   "diff [yaml-file]",
	Short: "Show drift between local synced config and the cluster",
	Long: `Compare a local yaml manifest + the files it references (env, secret
files, overrides) against the current server state. Nothing is written;
the command exits non-zero when any drift is detected so it can be used
as a CI gate.

If no yaml file is passed, diff scans the current directory for a unique
runos*.yaml that parses as a pulled-app manifest (so "cd into the per-app
dir, run runos apps diff" works with no arguments). Zero or multiple
matches produce a directly-actionable error.

The yaml file is the manifest. Diff reads the cid + id + aid from inside
it. You must still supply --cid (or have a default set) as a context
guard, it's cross-checked against the yaml's cid: field so you can't
diff a yaml against the wrong cluster.

Examples:
  cd runos.mycluster3.appid4 && runos apps diff                   # auto-detect
  runos apps diff runos.mycluster3.appid4/runos.yaml              # explicit path
  runos apps diff <yaml> --json                            # machine-readable
  runos apps diff <yaml> --show-secrets                    # decoded diff for drifted secrets`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAppsDiff,
}

func init() {
	appsDiffCmd.Flags().String("cid", "", "cluster ID (context guard; cross-checked against the yaml)")
	appsDiffCmd.Flags().Bool("show-secrets", false, "also print decoded content diff for drifted secret files (sensitive)")
	appsDiffCmd.Flags().BoolP("json", "j", false, "emit diff report as JSON")
	appsDiffCmd.Flags().Bool("redact-secrets", false, "replace env values in the env section's unified diff with <redacted> markers (used by the MCP wrapper to keep secrets out of LLM context)")
}

func runAppsDiff(cmd *cobra.Command, args []string) error {
	ctx, err := prepareAppsCmd(cmd)
	if err != nil {
		return err
	}

	yamlPath, err := resolveYamlArg(args, "diff")
	if err != nil {
		return err
	}

	localApp, err := apps.LoadLocalApp(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("yaml file %q not found", yamlPath)
		}
		return fmt.Errorf("read yaml: %w", err)
	}
	if localApp.ID == "" || localApp.CID == "" || localApp.AID == "" {
		return fmt.Errorf("yaml at %q is missing id/cid/aid, pull again to refresh", yamlPath)
	}

	jsonOutput, _ := cmd.Flags().GetBool("json")
	svc := ctx.svc

	report, err := apps.BuildDiffReport(svc, localApp, yamlPath, ctx.cfg.AccountID, ctx.cid)
	if err != nil {
		return err
	}

	showSecrets, _ := cmd.Flags().GetBool("show-secrets")
	if showSecrets && report.SecretFiles.Status != apps.StatusInSync {
		if err := apps.EnrichSecretFileDiffsWithContent(svc, localApp.ID, &report.SecretFiles); err != nil {
			return fmt.Errorf("fetch secret content: %w", err)
		}
	}

	// Mutually exclusive: --show-secrets is a deliberate user opt-in;
	// --redact-secrets is the MCP-driven safe default. If both are set
	// the redaction wins (we never want to leak values into an LLM
	// context just because the wrapper happened to also set show-secrets).
	if redact, _ := cmd.Flags().GetBool("redact-secrets"); redact {
		if report.Env.UnifiedDiff != "" {
			report.Env.UnifiedDiff = apps.RedactEnvUnifiedDiff(report.Env.UnifiedDiff)
		}
		// Secret-file content can only land in the report via
		// --show-secrets above; if --redact-secrets is set we strip it
		// out here as a belt-and-suspenders.
		for i := range report.SecretFiles.Entries {
			report.SecretFiles.Entries[i].UnifiedDiff = ""
		}
	}

	if jsonOutput {
		if err := emitJSON(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("Comparing %s (%s) against cluster %s\n", report.AppName, report.AppID, report.CID)
		printDiffReport(report)
		fmt.Println()
		if report.HasDrift() {
			fmt.Println("Drift detected.")
		} else {
			fmt.Println("No drift.")
		}
	}

	if report.HasDrift() {
		// Non-zero exit for CI use. Cobra renders error messages, so use
		// SilenceUsage + SilenceErrors to keep the output clean.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return fmt.Errorf("drift detected")
	}
	return nil
}

// sectionRuleWidth is the total visual width of a section-divider line.
// Box-drawing runes render at 1 column each, so rune count == column count.
const sectionRuleWidth = 64

// printDiffReport renders only the sections that have something
// noteworthy. Sections that are entirely in-sync collapse into a single
// trailing line ("In sync: yaml, env, ..."). Per-entry sections only show
// drifted / missing entries; the remaining in-sync entries are summarized
// as a small "+ N in sync" footer.
//
// Callers are responsible for any context lead-in (e.g. "Comparing X
// against cluster Y") and trailer (e.g. "Drift detected.").
func printDiffReport(r *apps.DiffReport) {
	var inSync []string

	if r.YAML.Status == apps.StatusInSync {
		inSync = append(inSync, "yaml")
	} else {
		printSection("yaml", r.YAML)
	}
	if r.Env.Status == apps.StatusInSync {
		inSync = append(inSync, "env")
	} else {
		printSection("env", r.Env)
	}
	if r.SecretFiles.Status == apps.StatusInSync {
		inSync = append(inSync, "secretFiles")
	} else {
		printSecretFilesSection(r.SecretFiles)
	}
	if r.Overrides.Status == apps.StatusInSync {
		inSync = append(inSync, "overrides")
	} else {
		printOverridesSection(r.Overrides)
	}

	switch {
	case r.Code == nil:
		// No baseline; don't add to either bucket, we have no opinion.
	case r.Code.IsStale():
		printCodeSection(r.Code)
	case !r.Code.RecordedFound:
		printCodeSection(r.Code)
	default:
		inSync = append(inSync, "code")
	}

	if len(inSync) > 0 {
		fmt.Println()
		fmt.Printf("In sync: %s\n", strings.Join(inSync, ", "))
	}
}

// printCodeSection renders the source-version status. Only called when
// there's something noteworthy to report: stale or anchor-missing.
func printCodeSection(c *apps.CodeVersionStatus) {
	fmt.Println()
	switch {
	case !c.RecordedFound:
		fmt.Println(sectionRule("code", "anchor missing"))
		fmt.Printf("  recorded: %s (not in server's archive list, purged or never persisted)\n", c.Recorded)
		if c.Latest != "" {
			fmt.Printf("  latest:   %s\n", c.Latest)
		}
	case c.IsStale():
		fmt.Println(sectionRule("code", fmt.Sprintf("%d newer", c.NewerCount)))
		fmt.Printf("  recorded: %s\n", c.Recorded)
		fmt.Printf("  latest:   %s\n", c.Latest)
		fmt.Println()
		for _, a := range c.NewerArchives {
			fmt.Printf("  + %s  %s\n", a.PushTime, a.CliUploadID)
		}
		fmt.Println()
		fmt.Println("  (run 'runos apps pull <yaml> --code --force' to refresh local source)")
	}
}

// printSection renders one of the simple (yaml / env) sections: a header
// rule, optionally the path and a unified diff body, then a blank spacer.
// Only called when status is not in_sync.
func printSection(name string, s apps.SectionDiff) {
	fmt.Println()
	fmt.Println(sectionRule(name, statusLabel(s.Status)))

	switch s.Status {
	case apps.StatusLocalMissing:
		if s.Path != "" {
			fmt.Printf("  %s\n", s.Path)
		}
		fmt.Println("  (local file missing)")
	case apps.StatusDrift:
		if s.Path != "" {
			fmt.Printf("  %s\n", s.Path)
		}
		if s.UnifiedDiff != "" {
			fmt.Println()
			printIndented(trimTrailingBlankLines(s.UnifiedDiff), "  ")
		}
	}
}

// printSecretFilesSection renders only drifted / missing entries; in-sync
// entries are rolled up into a "+ N in sync" footer. When an entry was
// enriched via --show-secrets the decoded unified diff is printed below
// the per-file line. Only called when the section's aggregate status
// isn't in_sync.
func printSecretFilesSection(s apps.SecretFilesDiff) {
	fmt.Println()
	fmt.Println(sectionRule("secretFiles", statusLabel(s.Status)))
	inSync := 0
	hasContent := false
	for _, e := range s.Entries {
		switch e.Status {
		case apps.StatusInSync:
			inSync++
		case apps.StatusDrift:
			fmt.Printf("  %-30s drift  (server md5: %s, local md5: %s)\n", e.Filename, e.ServerMd5, e.LocalMd5)
		case apps.StatusLocalMissing:
			fmt.Printf("  %-30s missing locally\n", e.Filename)
		}
		if e.UnifiedDiff != "" {
			hasContent = true
			fmt.Println()
			printIndented(trimTrailingBlankLines(e.UnifiedDiff), "    ")
			fmt.Println()
		}
	}
	if inSync > 0 {
		fmt.Printf("  + %s in sync\n", plural(inSync, "other", "others"))
	}
	if !hasContent {
		// Tell the user how to actually see the content diff. Only print
		// the hint when the section drifted but no entry carried a body.
		fmt.Println("  (re-run with --show-secrets to print decoded content diff)")
	}
}

// printOverridesSection mirrors printSecretFilesSection, but unlike
// secret files, override bodies are non-sensitive YAML, so the unified
// diff is always rendered inline (no opt-in flag).
func printOverridesSection(s apps.OverridesDiff) {
	fmt.Println()
	fmt.Println(sectionRule("overrides", statusLabel(s.Status)))
	inSync := 0
	for _, e := range s.Entries {
		label := overrideLabel(e)
		switch e.Status {
		case apps.StatusInSync:
			inSync++
		case apps.StatusDrift:
			fmt.Printf("  %-30s drift  (server md5: %s, local md5: %s)\n", label, e.ServerMd5, e.LocalMd5)
		case apps.StatusLocalMissing:
			fmt.Printf("  %-30s missing locally\n", label)
		}
		if e.UnifiedDiff != "" {
			fmt.Println()
			printIndented(trimTrailingBlankLines(e.UnifiedDiff), "    ")
			fmt.Println()
		}
	}
	if inSync > 0 {
		fmt.Printf("  + %s in sync\n", plural(inSync, "other", "others"))
	}
}

func overrideLabel(e apps.OverrideDiff) string {
	if e.Name == "" {
		return e.ID
	}
	return e.Name
}

// sectionRule builds a line of the form "─ <name> · <status> ───────…"
// padded out to sectionRuleWidth.
func sectionRule(name, status string) string {
	title := fmt.Sprintf("─ %s · %s ", name, status)
	pad := sectionRuleWidth - utf8.RuneCountInString(title)
	if pad < 0 {
		pad = 0
	}
	return title + strings.Repeat("─", pad)
}

func statusLabel(s apps.DiffStatus) string {
	switch s {
	case apps.StatusInSync:
		return "in sync"
	case apps.StatusDrift:
		return "drift"
	case apps.StatusLocalMissing:
		return "local missing"
	}
	return string(s)
}

// trimTrailingBlankLines removes any run of empty lines from the end of a
// block of text so unified diffs don't leave a dangling blank row.
func trimTrailingBlankLines(s string) string {
	s = strings.TrimRight(s, "\n")
	// Strip trailing lines that are whitespace-only (single space + newline
	// is what difflib emits for an unchanged empty line).
	for {
		idx := strings.LastIndex(s, "\n")
		tail := s[idx+1:]
		if strings.TrimSpace(tail) != "" {
			break
		}
		if idx < 0 {
			return ""
		}
		s = s[:idx]
	}
	return s
}

func printIndented(s, prefix string) {
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == "" {
			continue
		}
		fmt.Print(prefix + line)
		if !strings.HasSuffix(line, "\n") {
			fmt.Println()
		}
	}
}

