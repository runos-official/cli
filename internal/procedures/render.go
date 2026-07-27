package procedures

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RenderApproval writes the approval request as plain text for a human
// reading a terminal.
//
// WHAT THIS FUNCTION MAY NOT CONTAIN, and a test asserts it: a url, a
// token, a link, a button, an image or any other one-click control.
// "Notifications are informational and deep-link to canonical
// Console/CLI state; they never contain one-click, signed-link,
// reply-to-approve, or remote-content approval controls." A render is
// something to read. The decision is a separate authenticated command
// the human types.
//
// The NONCE is deliberately absent from the output even though the
// render carries one. A nonce on screen is a value a user can be talked
// into pasting somewhere, and it buys the reader nothing: the CLI never
// sends it, because the decision is bound by the plan hash and by the
// authenticated session, not by a secret the human retypes.
//
// The two halves are visually separated because they are different
// kinds of thing. Everything above `UNTRUSTED` is a fact conductor
// derived and can be relied on; everything below it is text somebody
// else wrote and is shown for the human's judgement alone.
func RenderApproval(operation *Operation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Operation %s\n", operation.OperationID)
	fmt.Fprintf(&b, "  state          %s\n", operation.State)
	fmt.Fprintf(&b, "  procedure      %s\n", operation.Procedure)
	fmt.Fprintf(&b, "  classification %s\n", operation.Classification)
	fmt.Fprintf(&b, "  created        %s\n", operation.CreatedAt)
	fmt.Fprintf(&b, "  deadline       %s\n", operation.DeadlineAt)
	if operation.Attempts > 0 {
		fmt.Fprintf(&b, "  attempts       %d\n", operation.Attempts)
	}
	if operation.LastError != nil && *operation.LastError != "" {
		fmt.Fprintf(&b, "  last error     %s\n", *operation.LastError)
	}

	if operation.ApprovalRequest == nil {
		// The genuinely absent case: the plan's Procedure is no longer
		// registered. Reported as its own state rather than as an empty
		// render, because "nothing to show" and "this can never run
		// again" are not the same message.
		b.WriteString("\nNo approval render is available.\n")
		if operation.Note != "" {
			fmt.Fprintf(&b, "%s\n", operation.Note)
		}
		return b.String()
	}

	render := operation.ApprovalRequest
	fmt.Fprintf(&b, "\nPLAN\n")
	fmt.Fprintf(&b, "  plan hash        %s\n", render.PlanHash)
	fmt.Fprintf(&b, "  preflight digest %s\n", render.PreflightDigest)
	fmt.Fprintf(&b, "  procedure        %s (%s)\n", render.Procedure.Ref, render.Procedure.Summary)
	fmt.Fprintf(&b, "  owner            %s\n", render.Procedure.Owner)
	fmt.Fprintf(&b, "  scope            account %s, cluster %s\n", render.Scope.Aid, render.Scope.Cid)

	b.WriteString("\nTARGETS\n")
	if len(render.Targets) == 0 {
		b.WriteString("  (none resolved)\n")
	}
	for _, target := range render.Targets {
		shared := ""
		if target.Shared {
			shared = "  SHARED"
		}
		fmt.Fprintf(&b, "  %s %s  generation %s%s\n", target.Kind, target.ID, target.Generation, shared)
	}

	b.WriteString("\nARGUMENTS\n")
	writeArgs(&b, render.Args)

	fmt.Fprintf(&b, "\nRISK  %s (declared floor %s)\n", render.Classification.Cls, render.Classification.Floor)
	for _, raise := range render.Classification.Raises {
		fmt.Fprintf(&b, "  raised to %s by %s: %s\n", raise.To, raise.Rule, raise.Reason)
	}

	b.WriteString("\nPREFLIGHT\n")
	for _, check := range render.Preflight {
		fmt.Fprintf(&b, "  [%s] %s: %s\n", check.Outcome, check.Check, check.Detail)
	}

	b.WriteString("\nEFFECTS\n")
	for _, effect := range render.Effects {
		fmt.Fprintf(&b, "  %s (%s): %s\n", effect.Kind, effect.Owner, effect.Description)
	}

	b.WriteString("\nSTAGES\n")
	for i, stage := range render.Stages {
		fmt.Fprintf(&b, "  %d. %s -> %s: %s\n", i+1, stage.Name, stage.Targets, stage.Description)
	}

	fmt.Fprintf(&b, "\nRECOVERY  %s\n  %s\n", render.Recovery.Kind, render.Recovery.Description)

	b.WriteString("\nPOSTCONDITIONS\n")
	if len(render.Postconditions) == 0 {
		b.WriteString("  (none declared)\n")
	}
	for _, postcondition := range render.Postconditions {
		fmt.Fprintf(&b, "  %s: %s (stability window %dms)\n",
			postcondition.Check, postcondition.Description, postcondition.StabilityWindowMs)
	}

	fmt.Fprintf(&b, "\nDECISION  %s (policy %s)\n", render.Decision.Outcome, render.Decision.PolicyRevision)
	for _, reason := range render.Decision.Reasons {
		fmt.Fprintf(&b, "  %s\n", reason)
	}

	fmt.Fprintf(&b, "\nAPPROVAL  requires %s\n", render.Approval.Required)
	fmt.Fprintf(&b, "  authorized roles %s\n", strings.Join(render.Approval.Roles, ", "))
	// Conductor sends `now + the decision TTL` when no decision window has been
	// minted, so that timestamp is a LENGTH and not a deadline. Printing it as
	// an expiry would show a human a countdown that has not started, which is
	// the defect conductor avoided by sending a null nonce rather than a
	// placeholder one. The nonce is the field that says which case this is.
	if render.Approval.Nonce != nil {
		fmt.Fprintf(&b, "  window expires   %s\n", render.Approval.ExpiresAt)
	} else {
		b.WriteString("  no decision window has been opened yet; one opens when a decision is made, and it is short\n")
	}

	if len(render.Freezes) > 0 {
		b.WriteString("\nSCOPE FREEZES IN FORCE\n")
		for _, freeze := range render.Freezes {
			fmt.Fprintf(&b, "  %s\n", freeze)
		}
	}

	b.WriteString("\nRECENT OPERATIONS ON THIS PROCEDURE\n")
	if len(render.CumulativeHistory.Entries) == 0 {
		b.WriteString("  (none recorded)\n")
	}
	for _, entry := range render.CumulativeHistory.Entries {
		fmt.Fprintf(&b, "  %s  %s  %s  %s\n", entry.RecordedAt, entry.State, entry.Classification, entry.OperationID)
	}
	// Printed unconditionally, including under an empty list, which is
	// exactly where it matters: conductor does not instrument every
	// direct write, so an absent entry is not evidence nothing happened.
	fmt.Fprintf(&b, "  %s\n", render.CumulativeHistory.Note)

	if render.Untrusted != nil {
		writeUntrusted(&b, render.Untrusted)
	}

	return b.String()
}

// writeArgs prints arguments in a stable order. Map iteration in Go is
// randomised, and a plan whose arguments reorder between two reads is
// one a human cannot diff against what they read a minute ago.
func writeArgs(b *strings.Builder, args map[string]any) {
	if len(args) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(b, "  %s = %v\n", name, args[name])
	}
}

func writeUntrusted(b *strings.Builder, untrusted *Untrusted) {
	rule := strings.Repeat("-", 70)
	fmt.Fprintf(b, "\n%s\n", rule)
	b.WriteString("UNTRUSTED: written by the proposer, not by Conductor.\n")
	b.WriteString("It reaches no check, no classification, no policy and no argument.\n")
	fmt.Fprintf(b, "Proposer: %s, incident %s, epoch %s, profile %s, proposal %s\n",
		untrusted.Proposer.AccountSRE, untrusted.Proposer.IncidentID,
		untrusted.Proposer.InvestigationEpoch, untrusted.Proposer.Profile, untrusted.Proposer.ProposalID)
	fmt.Fprintf(b, "%s\n", rule)
	writeUntrustedField(b, "rationale", untrusted.Rationale)
	writeUntrustedField(b, "expected outcome", untrusted.ExpectedOutcome)
	// Rendered and never used: a proposer says how it expects the outcome
	// to be checked, and conductor checks the postconditions the
	// DEFINITION declares. Showing the intent lets a human notice a
	// proposer that expected something conductor is not going to look at.
	writeUntrustedField(b, "verification intent (rendered, never used)", untrusted.VerificationIntent)
	for i, evidence := range untrusted.Evidence {
		writeUntrustedField(b, fmt.Sprintf("evidence %d ref", i+1), evidence.Ref)
		writeUntrustedField(b, fmt.Sprintf("evidence %d summary", i+1), evidence.Summary)
	}
}

func writeUntrustedField(b *strings.Builder, label string, value UntrustedText) {
	truncated := ""
	if value.Truncated {
		truncated = " (truncated by Conductor)"
	}
	fmt.Fprintf(b, "  %s [%s]%s:\n", label, value.Kind, truncated)
	for line := range strings.SplitSeq(defangURLs(value.Text), "\n") {
		fmt.Fprintf(b, "    %s\n", line)
	}
}

// urlScheme matches a scheme followed by its colon, in the untrusted
// text only.
var urlScheme = regexp.MustCompile(`(?i)\b(https?|ftp|file|data|javascript)://`)

// defangURLs breaks the scheme in untrusted text so a terminal cannot
// turn it into a clickable control.
//
// THIS IS A SECOND CONSUMER'S ESCAPING PROBLEM AND CONDUCTOR CANNOT SOLVE
// IT. Conductor HTML-escapes untrusted text at the producer, which stops
// it becoming markup in an HTML consumer and is the right boundary
// there. A terminal is a different consumer with a different linkifier:
// iTerm2, Windows Terminal, GNOME Terminal and VS Code all auto-detect a
// bare `https://host` and make it clickable, and HTML escaping does not
// touch that at all. Without this, a proposer's rationale is a place to
// put a link into the middle of an approval screen, which is exactly
// what "untrusted content cannot create buttons, links, hidden fields or
// remote images" bars.
//
// It DEFANGS rather than strips, so the human still reads what was
// attempted. The host stays visible, which is what makes an injection
// attempt legible instead of silently missing.
//
// Conductor-derived facts are deliberately NOT run through this: they
// carry no urls, and defanging one would misrepresent Conductor's own
// output. Only text somebody else wrote is treated as hostile.
func defangURLs(text string) string {
	return urlScheme.ReplaceAllString(text, "$1[://]")
}

// RenderBlocked writes a 409 from a deterministic safety gate.
//
// EVERY reason, never the first, and it says plainly that nothing was
// created. A caller that fixes one blocked check and resubmits into the
// next one learns the shape of the gate one round trip at a time, which
// is how a deterministic refusal comes to feel arbitrary.
func RenderBlocked(blocked *Blocked) string {
	var b strings.Builder
	b.WriteString("BLOCKED by a deterministic safety gate. Nothing was created.\n\n")
	// Conductor answers this refusal in two shapes: the route's own, carrying
	// the classification, the plan hash and a note, and a bare one from
	// `createOperation` re-checking the same condition and carrying only the
	// reasons. They are the SAME refusal with different context, so the heading
	// does not change; the fields simply do not print when they are absent.
	// Printing an empty "plan hash" line would show a human a blank where a
	// hash belongs, which reads as a missing value rather than as one Conductor
	// did not send.
	if blocked.Classification != "" {
		fmt.Fprintf(&b, "  classification %s\n", blocked.Classification)
	}
	if blocked.PlanHash != "" {
		fmt.Fprintf(&b, "  plan hash      %s\n", blocked.PlanHash)
	}
	fmt.Fprintf(&b, "\n%d failing check(s):\n", len(blocked.Reasons))
	for _, reason := range blocked.Reasons {
		fmt.Fprintf(&b, "  - %s\n", reason)
	}
	if blocked.Note != "" {
		fmt.Fprintf(&b, "\n%s\n", blocked.Note)
	}
	return b.String()
}

// RenderPlan writes the plan a dry run produced, with the fact that
// nothing was persisted stated rather than implied.
func RenderPlan(plan *Plan) string {
	var b strings.Builder
	b.WriteString("DRY RUN. A plan was built and rendered; no operation was created.\n\n")
	fmt.Fprintf(&b, "  procedure        %s\n", plan.Procedure.Ref)
	fmt.Fprintf(&b, "  plan hash        %s\n", plan.PlanHash)
	fmt.Fprintf(&b, "  preflight digest %s\n", plan.PreflightDigest)
	fmt.Fprintf(&b, "  classification   %s (declared floor %s)\n", plan.Classification.Cls, plan.Classification.Floor)
	for _, raise := range plan.Classification.Raises {
		fmt.Fprintf(&b, "    raised to %s by %s: %s\n", raise.To, raise.Rule, raise.Reason)
	}
	fmt.Fprintf(&b, "  approval         %s\n", plan.ApprovalRequired)

	b.WriteString("\nTARGETS\n")
	if len(plan.Targets) == 0 {
		b.WriteString("  (none resolved)\n")
	}
	for _, target := range plan.Targets {
		fmt.Fprintf(&b, "  %s %s  generation %s\n", target.Kind, target.ID, target.Generation)
	}

	b.WriteString("\nARGUMENTS\n")
	writeArgs(&b, plan.Args)

	b.WriteString("\nPREFLIGHT\n")
	for _, check := range plan.Preflight {
		fmt.Fprintf(&b, "  [%s] %s: %s\n", check.Outcome, check.Check, check.Detail)
	}

	b.WriteString("\nSTAGES\n")
	for i, stage := range plan.Stages {
		fmt.Fprintf(&b, "  %d. %s -> %s: %s\n", i+1, stage.Name, stage.Targets, stage.Description)
	}

	fmt.Fprintf(&b, "\nDECISION  %s (policy %s)\n", plan.Decision.Outcome, plan.Decision.PolicyRevision)
	for _, reason := range plan.Decision.Reasons {
		fmt.Fprintf(&b, "  %s\n", reason)
	}
	return b.String()
}

// RenderCatalog writes the Procedure catalog as a readable list.
func RenderCatalog(entries []CatalogEntry) string {
	if len(entries) == 0 {
		return "No Procedures are registered in this cluster.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d Procedure(s):\n", len(entries))
	for _, entry := range entries {
		fmt.Fprintf(&b, "\n  %s\n", entry.Ref)
		fmt.Fprintf(&b, "    %s\n", entry.Summary)
		// The floor is named as a floor. The class a plan actually gets
		// is computed at plan time from live topology, cumulative use and
		// any freeze, and may be higher; presenting the floor as the
		// class would understate an operation nobody has planned yet.
		fmt.Fprintf(&b, "    risk floor %s   approval %s   roles %s\n",
			entry.RiskFloor, entry.Approval, strings.Join(entry.Roles, ", "))
		fmt.Fprintf(&b, "    SRE eligibility %s\n", entry.SREEligibility)
		if len(entry.Args) > 0 {
			b.WriteString("    arguments:\n")
			for _, spec := range entry.Args {
				required := ""
				if spec.Required {
					required = " (required)"
				}
				enum := ""
				if len(spec.Values) > 0 {
					enum = " one of: " + strings.Join(spec.Values, ", ")
				}
				fmt.Fprintf(&b, "      %s: %s%s%s  %s\n", spec.Name, spec.Type, required, enum, spec.Description)
			}
		}
	}
	return b.String()
}
