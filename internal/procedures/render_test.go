package procedures

import (
	"regexp"
	"strings"
	"testing"
)

// forbiddenInApprovalPath are the shapes hardening requirement 4 bars
// from an approval render: "Model narrative, retrieved text, log
// fragments, labels, annotations, and remote content are visibly marked
// as untrusted evidence and cannot create buttons, links, hidden fields,
// remote images, or approval semantics", and "they never contain
// one-click, signed-link, reply-to-approve, or remote-content approval
// controls".
//
// Asserted as a TEST rather than left as a convention, because a
// convention is what the next person adding a "click here to approve"
// convenience line will not have read.
var forbiddenInApprovalPath = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"a url scheme", regexp.MustCompile(`(?i)\b(https?|ftp|file|data|javascript)\s*:`)},
	{"a bare host", regexp.MustCompile(`(?i)\bwww\.`)},
	{"an html anchor or button", regexp.MustCompile(`(?i)<\s*(a|button|form|img|iframe|input)\b`)},
	{"a markdown link or image", regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`)},
	{"a one-click invitation", regexp.MustCompile(`(?i)click (here|to)|tap (here|to)|follow (this|the) link`)},
	{"a reply-to-approve invitation", regexp.MustCompile(`(?i)reply (with|to) (yes|approve)`)},
}

func assertNoApprovalControls(t *testing.T, label, rendered string) {
	t.Helper()
	for _, forbidden := range forbiddenInApprovalPath {
		if match := forbidden.pattern.FindString(rendered); match != "" {
			t.Errorf("%s contains %s (%q); the approval path carries no url, token, link, button or image", label, forbidden.name, match)
		}
	}
}

// fullOperation is deliberately hostile: every field a proposer or an
// operator can influence carries a payload that would become a control
// if any of it were treated as markup or as a fact.
func fullOperation() *Operation {
	lastError := "the effect returned no receipt"
	nonce := "nonce-value-should-not-be-printed"
	operation := &Operation{
		OperationID:    "op-1",
		State:          "pending_authorization",
		Procedure:      "runos.valkey.delete@1.1.0",
		Classification: "A3",
		Attempts:       1,
		LastError:      &lastError,
		CreatedAt:      "2026-07-27T08:00:00.000Z",
		DeadlineAt:     "2026-07-27T09:00:00.000Z",
		ApprovalRequest: &ApprovalRequest{
			OperationID:     "op-1",
			PlanID:          "plan-1",
			PlanHash:        "hash-abc",
			PreflightDigest: "digest-xyz",
			Classification: Classification{
				Cls:   "A3",
				Floor: "A2",
				Raises: []struct {
					Rule   string `json:"rule"`
					To     string `json:"to"`
					Reason string `json:"reason"`
				}{{Rule: "scope_frozen", To: "A3", Reason: "the scope carries a live freeze"}},
			},
			Preflight: []Check{{Check: "target_resolved", Outcome: "pass", Detail: "one target"}},
			Stages:    []Stage{{Name: "delete", Targets: "valkey-ab2cd", Description: "remove the namespace"}},
			Args:      map[string]any{"osid": "valkey-ab2cd", "confirm": true},
			Freezes:   []string{"scope a/b/service/valkey-ab2cd is frozen (verification_failed) by operation op-0"},
			Untrusted: &Untrusted{
				Rationale: UntrustedText{
					Untrusted: true, Kind: "model_narrative",
					// Conductor escapes at the producer, so this is what an
					// injected payload actually looks like on arrival.
					Text: "&lt;a href=&quot;https://evil.example/approve&quot;&gt;approve now&lt;/a&gt;",
				},
				ExpectedOutcome:    UntrustedText{Untrusted: true, Kind: "model_narrative", Text: "the cache is empty"},
				VerificationIntent: UntrustedText{Untrusted: true, Kind: "model_narrative", Text: "check the key count"},
				Evidence: []struct {
					Ref     UntrustedText `json:"ref"`
					Summary UntrustedText `json:"summary"`
				}{{
					Ref:     UntrustedText{Untrusted: true, Kind: "cited_evidence", Text: "evidence-1"},
					Summary: UntrustedText{Untrusted: true, Kind: "cited_evidence", Text: "memory rising", Truncated: true},
				}},
			},
		},
	}
	operation.ApprovalRequest.Procedure.Ref = "runos.valkey.delete@1.1.0"
	operation.ApprovalRequest.Procedure.Summary = "Delete a Valkey service"
	operation.ApprovalRequest.Procedure.Owner = "runos"
	operation.ApprovalRequest.Scope.Aid = "acct"
	operation.ApprovalRequest.Scope.Cid = "clus"
	operation.ApprovalRequest.Recovery.Kind = "safe_stop"
	operation.ApprovalRequest.Recovery.Description = "stops at deleted_from_cluster"
	operation.ApprovalRequest.Approval.Required = "fresh_human"
	operation.ApprovalRequest.Approval.Nonce = &nonce
	operation.ApprovalRequest.Approval.ExpiresAt = "2026-07-27T08:15:00.000Z"
	operation.ApprovalRequest.Approval.Roles = []string{"owner", "admin"}
	operation.ApprovalRequest.Decision.Outcome = "requires_human"
	operation.ApprovalRequest.Decision.PolicyRevision = "c5.1"
	operation.ApprovalRequest.CumulativeHistory.Note = "Conductor does not instrument every direct write."
	return operation
}

func TestRenderApprovalCarriesNoApprovalControl(t *testing.T) {
	assertNoApprovalControls(t, "the approval render", RenderApproval(fullOperation()))
}

func TestRenderBlockedCarriesNoApprovalControl(t *testing.T) {
	rendered := RenderBlocked(&Blocked{
		Error:          "Blocked by a deterministic safety gate",
		Reasons:        []string{"cluster_topology_supported is unknown", "redundancy_preserved is unknown"},
		Classification: "A3",
		PlanHash:       "hash-abc",
		Note:           "A human approval cannot waive a failed or unknown deterministic gate.",
	})
	assertNoApprovalControls(t, "the blocked render", rendered)
}

func TestRenderCatalogCarriesNoApprovalControl(t *testing.T) {
	rendered := RenderCatalog([]CatalogEntry{{
		Ref: "runos.valkey.delete@1.1.0", Summary: "Delete a Valkey service",
		RiskFloor: "A3", Approval: "fresh_human", Roles: []string{"owner"},
		Args: []ArgSpec{{Name: "osid", Type: "string", Required: true, Description: "the service"}},
	}})
	assertNoApprovalControls(t, "the catalog render", rendered)
}

// The nonce is on the object and must not reach the screen: a value a
// user can read is a value they can be talked into pasting somewhere,
// and the CLI never sends it because the decision is bound by the plan
// hash and the authenticated session.
func TestRenderApprovalDoesNotPrintTheNonce(t *testing.T) {
	if strings.Contains(RenderApproval(fullOperation()), "nonce-value-should-not-be-printed") {
		t.Fatal("the render printed the decision nonce")
	}
}

// The escaped form arrives escaped and leaves escaped. Decoding it in
// the consumer would move the boundary out of Conductor, which is the
// one thing producer-side escaping exists to prevent.
func TestRenderApprovalDoesNotDecodeUntrustedText(t *testing.T) {
	rendered := RenderApproval(fullOperation())
	if !strings.Contains(rendered, "&lt;a href=") {
		t.Fatal("the escaped untrusted text was not rendered verbatim")
	}
	if strings.Contains(rendered, "<a href=") {
		t.Fatal("the render decoded Conductor's escaping back into markup")
	}
}

// The human must be told which half of the screen is a Conductor fact
// and which half is somebody else's text.
func TestRenderApprovalMarksTheUntrustedHalf(t *testing.T) {
	rendered := RenderApproval(fullOperation())
	if !strings.Contains(rendered, "UNTRUSTED") {
		t.Fatal("the untrusted block is not visibly marked")
	}
	if !strings.Contains(rendered, "model_narrative") {
		t.Fatal("the untrusted kind is not shown, so a reader cannot tell narrative from cited evidence")
	}
	if !strings.Contains(rendered, "truncated by Conductor") {
		t.Fatal("truncation is not disclosed, so a reader cannot tell a short summary from a cut one")
	}
}

// An empty history is exactly where the note matters: Conductor does not
// instrument every direct write, so absence is not evidence of quiet.
func TestRenderApprovalAlwaysPrintsTheHistoryNote(t *testing.T) {
	operation := fullOperation()
	operation.ApprovalRequest.CumulativeHistory.Entries = nil
	rendered := RenderApproval(operation)
	if !strings.Contains(rendered, "does not instrument every direct write") {
		t.Fatal("the history note was dropped when the history was empty")
	}
}

// The retired-Procedure branch is genuinely absent rather than empty,
// and must not render as a screen of blank fields.
func TestRenderApprovalReportsAMissingRender(t *testing.T) {
	rendered := RenderApproval(&Operation{
		OperationID: "op-2", State: "pending_authorization",
		Note: "the Procedure runos.gone@1.0.0 is no longer registered",
	})
	if !strings.Contains(rendered, "No approval render is available") {
		t.Fatal("a missing approval render was not reported as one")
	}
	if !strings.Contains(rendered, "no longer registered") {
		t.Fatal("Conductor's reason for the missing render was dropped")
	}
	if strings.Contains(rendered, "PLAN") {
		t.Fatal("an absent render still printed plan headings")
	}
}

// The declared floor and the computed class are different facts and a
// render that showed only one of them would mislead about the other.
func TestRenderApprovalShowsWhyTheClassIsAboveTheFloor(t *testing.T) {
	rendered := RenderApproval(fullOperation())
	if !strings.Contains(rendered, "declared floor A2") {
		t.Fatal("the declared floor was not distinguished from the computed class")
	}
	if !strings.Contains(rendered, "raised to A3 by scope_frozen") {
		t.Fatal("the derivation of the raise was dropped")
	}
}

// Go randomises map iteration, so an unsorted render reorders arguments
// between two reads of the same unchanged plan.
func TestRenderApprovalOrdersArgumentsStably(t *testing.T) {
	operation := fullOperation()
	operation.ApprovalRequest.Args = map[string]any{"zeta": 1, "alpha": 2, "mid": 3, "beta": 4, "yankee": 5}
	first := RenderApproval(operation)
	for range 20 {
		if RenderApproval(operation) != first {
			t.Fatal("the render is not stable across calls; arguments reorder")
		}
	}
	alpha := strings.Index(first, "alpha =")
	zeta := strings.Index(first, "zeta =")
	if alpha < 0 || zeta < 0 || alpha > zeta {
		t.Fatal("arguments are not in sorted order")
	}
}

// A blocked plan's whole value is that it reports every failing check.
func TestRenderBlockedShowsEveryReason(t *testing.T) {
	blocked := &Blocked{
		Reasons:        []string{"first check", "second check", "third check"},
		Classification: "A3",
		PlanHash:       "hash-abc",
		Note:           "Nothing was created.",
	}
	rendered := RenderBlocked(blocked)
	for _, reason := range blocked.Reasons {
		if !strings.Contains(rendered, reason) {
			t.Fatalf("reason %q was dropped from the blocked render", reason)
		}
	}
	if !strings.Contains(rendered, "3 failing check(s)") {
		t.Fatal("the blocked render does not say how many checks failed")
	}
	if !strings.Contains(rendered, "Nothing was created") {
		t.Fatal("the blocked render does not say that nothing was created")
	}
}

// A dry run must not read like something happened.
func TestRenderPlanSaysNothingWasCreated(t *testing.T) {
	rendered := RenderPlan(&Plan{PlanHash: "hash-abc", ApprovalRequired: "fresh_human"})
	if !strings.Contains(rendered, "no operation was created") {
		t.Fatal("the dry-run render does not say that nothing was created")
	}
	assertNoApprovalControls(t, "the dry-run render", rendered)
}

// The floor is a floor. Presenting it as the class would understate an
// operation nobody has planned yet.
func TestRenderCatalogNamesTheFloorAsAFloor(t *testing.T) {
	rendered := RenderCatalog([]CatalogEntry{{
		Ref: "runos.valkey.delete@1.1.0", RiskFloor: "A3", Approval: "fresh_human",
	}})
	if !strings.Contains(rendered, "risk floor A3") {
		t.Fatal("the catalog render does not name the floor as a floor")
	}
}

// The defect this pins was found by the no-controls test above, and it
// is a real one: Conductor's HTML escaping stops untrusted text becoming
// markup in an HTML consumer and does nothing at all about a terminal
// that auto-linkifies a bare `https://host`. Without defanging, a
// proposer's rationale is a place to put a clickable link into the
// middle of an approval screen.
func TestRenderApprovalDefangsURLsInUntrustedTextOnly(t *testing.T) {
	operation := fullOperation()
	operation.ApprovalRequest.Untrusted.Rationale = UntrustedText{
		Untrusted: true, Kind: "model_narrative",
		Text: "see https://evil.example/approve and http://other.example for detail",
	}
	rendered := RenderApproval(operation)

	if strings.Contains(rendered, "https://evil.example") {
		t.Fatal("a url in untrusted text reached the render intact and a terminal would linkify it")
	}
	// Defanged, NOT stripped: the human still reads what was attempted,
	// which is what makes an injection attempt legible.
	if !strings.Contains(rendered, "evil.example/approve") {
		t.Fatal("the host was removed; a defanged url must stay readable")
	}
	if !strings.Contains(rendered, "https[://]evil.example") {
		t.Fatalf("expected the scheme to be defanged, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "http[://]other.example") {
		t.Fatal("only the first url was defanged")
	}
}

// Conductor's own facts carry no urls and must not be rewritten:
// defanging Conductor output would misrepresent it.
func TestRenderApprovalDoesNotRewriteConductorFacts(t *testing.T) {
	operation := fullOperation()
	operation.ApprovalRequest.Preflight = []Check{
		{Check: "endpoint_reachable", Outcome: "pass", Detail: "the service answers"},
	}
	rendered := RenderApproval(operation)
	if !strings.Contains(rendered, "the service answers") {
		t.Fatal("a Conductor preflight detail was altered")
	}
}

func TestDefangURLsLeavesOrdinaryTextAlone(t *testing.T) {
	for _, text := range []string{"", "no urls here", "a colon: and a slash /", "ratio 3://4 is not a scheme"} {
		if got := defangURLs(text); got != text {
			t.Errorf("defangURLs(%q) = %q, want it unchanged", text, got)
		}
	}
}

// Conductor sends `now + the decision TTL` when no decision window has been
// minted, so that timestamp is a LENGTH and not a deadline. The nonce is the
// field that separates the two cases, and printing an expiry from the
// synthesized value would show a countdown that has not started.
func TestRenderApprovalDoesNotAssertAWindowThatHasNotOpened(t *testing.T) {
	operation := fullOperation()
	operation.ApprovalRequest.Approval.Nonce = nil
	operation.ApprovalRequest.Approval.ExpiresAt = "2026-07-27T09:15:00.000Z"
	rendered := RenderApproval(operation)
	if strings.Contains(rendered, "2026-07-27T09:15:00.000Z") {
		t.Fatal("the render printed a synthesized expiry as a real deadline")
	}
	if !strings.Contains(rendered, "no decision window has been opened yet") {
		t.Fatalf("the render does not say the window has not opened:\n%s", rendered)
	}
}

func TestRenderApprovalShowsTheExpiryOnceAWindowExists(t *testing.T) {
	operation := fullOperation()
	nonce := "a-real-nonce"
	operation.ApprovalRequest.Approval.Nonce = &nonce
	operation.ApprovalRequest.Approval.ExpiresAt = "2026-07-27T08:15:00.000Z"
	rendered := RenderApproval(operation)
	if !strings.Contains(rendered, "window expires   2026-07-27T08:15:00.000Z") {
		t.Fatal("a real decision window was not shown")
	}
	if strings.Contains(rendered, "a-real-nonce") {
		t.Fatal("the render printed the nonce")
	}
}

// The bare 409 shape carries only the reasons. It is the SAME deterministic
// refusal with less context, so it keeps the same heading and simply omits the
// fields Conductor did not send rather than printing blanks where a
// classification and a hash belong.
func TestRenderBlockedOmitsFieldsTheBareShapeDoesNotCarry(t *testing.T) {
	rendered := RenderBlocked(&Blocked{Reasons: []string{"only reason"}})
	if !strings.Contains(rendered, "BLOCKED by a deterministic safety gate") {
		t.Fatal("the bare shape lost the heading")
	}
	if !strings.Contains(rendered, "only reason") {
		t.Fatal("the bare shape lost its reason")
	}
	if strings.Contains(rendered, "plan hash") {
		t.Fatal("an empty plan hash line was printed")
	}
	if strings.Contains(rendered, "classification") {
		t.Fatal("an empty classification line was printed")
	}
	assertNoApprovalControls(t, "the bare blocked render", rendered)
}
