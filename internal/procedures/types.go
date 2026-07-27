// Package procedures is the client side of Conductor's deterministic
// Procedure surface: the catalog, planning and creating an operation,
// reading the approval render, deciding, revoking, and the two operator
// controls (kill switches and scope freezes).
//
// It exists as its own package rather than as more `cmd/` code because
// three of its rules are security properties that belong in one tested
// place rather than repeated per command:
//
//   - a PAT may invoke, approve, reject and revoke (Q&A 131) and may not
//     release a kill switch or a scope freeze, which Q&A 131 left as they
//     were (auth.Kind(cfg).IsPAT(); see RefuseStoredSecret);
//   - a decision names the exact plan hash the human was shown, which is
//     the whole content of "the decision is bound to the exact plan";
//   - the approval render carries no url, token, link, button or image,
//     and this client must not manufacture one.
//
// The HTTP contract is the foreman handbook, `conductor-api/system`,
// starting at the article `procedures-overview`.
package procedures

// UntrustedText is a string Conductor has marked as NOT its own fact:
// proposer narrative, cited evidence, or an operator's written reason.
// It arrives HTML-escaped from the producer.
//
// The CLI writes to a terminal, so the escaping buys nothing here and
// costs a little readability (`&amp;` where the operator typed `&`). It
// is still rendered verbatim: decoding it back would move the escaping
// boundary out of Conductor and into every consumer, which is exactly
// what the producer-side escaping exists to prevent, and a terminal is
// not the only place a caller pipes this output.
type UntrustedText struct {
	Untrusted bool   `json:"untrusted"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// ArgSpec is one declared Procedure argument. Types are declared rather
// than inferred because Conductor performs NO coercion: `"3"` is not 3,
// and a path that quietly accepts one spelling and canonicalises it to
// another is a path where the approved plan and the requested plan can
// differ while both look right. CoerceArgs uses this to convert the
// terminal's strings before they reach the wire.
type ArgSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
	Values      []string `json:"values"`
	Redacted    bool     `json:"redacted"`
}

// CatalogEntry is one Procedure as the catalog read reports it.
type CatalogEntry struct {
	Ref            string    `json:"ref"`
	ID             string    `json:"id"`
	Version        string    `json:"version"`
	Owner          string    `json:"owner"`
	Summary        string    `json:"summary"`
	RiskFloor      string    `json:"riskFloor"`
	SREEligibility string    `json:"sreEligibility"`
	Exposure       []string  `json:"exposure"`
	Approval       string    `json:"approval"`
	Roles          []string  `json:"roles"`
	Args           []ArgSpec `json:"args"`
}

// Plan is the caller-visible projection of an immutable ActionPlan.
type Plan struct {
	PlanID          string `json:"planId"`
	PlanHash        string `json:"planHash"`
	PreflightDigest string `json:"preflightDigest"`
	Procedure       struct {
		Ref    string `json:"ref"`
		Digest string `json:"digest"`
	} `json:"procedure"`
	Args    map[string]any `json:"args"`
	Targets []struct {
		Kind       string `json:"kind"`
		ID         string `json:"id"`
		Generation string `json:"generation"`
		Shared     bool   `json:"shared"`
	} `json:"targets"`
	Classification Classification `json:"classification"`
	Preflight      []Check        `json:"preflight"`
	Stages         []Stage        `json:"stages"`
	Decision       Decision       `json:"decision"`
	// ApprovalRequired is `fresh_human` or `direct_caller_authority`.
	ApprovalRequired string `json:"approvalRequired"`
	DeadlineMs       int64  `json:"deadlineMs"`
}

// Classification carries the computed class AND the derivation, because
// a render showing only the class hides why it is above the floor.
type Classification struct {
	Cls    string `json:"cls"`
	Floor  string `json:"floor"`
	Raises []struct {
		Rule   string `json:"rule"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	} `json:"raises"`
}

// Check is one preflight result.
type Check struct {
	Check   string `json:"check"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

// Stage is one ordered step of the plan.
type Stage struct {
	Name        string `json:"name"`
	Targets     string `json:"targets"`
	Description string `json:"description"`
}

// Decision is the policy outcome and every reason behind it.
type Decision struct {
	Outcome        string   `json:"outcome"`
	Reasons        []string `json:"reasons"`
	PolicyRevision string   `json:"policyRevision"`
}

// CreatedOperation is the 202 from creating an operation.
type CreatedOperation struct {
	OperationID  string `json:"operationId"`
	RootChangeID string `json:"rootChangeId"`
	State        string `json:"state"`
	Plan         Plan   `json:"plan"`
	Note         string `json:"note"`
}

// DryRun is the 200 from `?dryRun=true`: a rendered plan and no row.
type DryRun struct {
	DryRun bool `json:"dryRun"`
	Plan   Plan `json:"plan"`
}

// Blocked is the 409 a deterministic safety gate produces. Every field
// matters to the human reading it, and Reasons carries EVERY failing
// check rather than the first.
type Blocked struct {
	Error          string   `json:"error"`
	Reasons        []string `json:"reasons"`
	Classification string   `json:"classification"`
	PlanHash       string   `json:"planHash"`
	Note           string   `json:"note"`
}

// InvalidArgs is the 400 from the declared argument spec.
type InvalidArgs struct {
	Error   string   `json:"error"`
	Reasons []string `json:"reasons"`
}

// Operation is the operation read: state plus, when the Procedure is
// still registered, the full approval render.
//
// ApprovalRequest is a POINTER because it is genuinely absent when the
// plan's Procedure has been retired from the catalog, and Note then says
// so. A value type would render that case as a screen of zeroes.
type Operation struct {
	OperationID     string           `json:"operationId"`
	State           string           `json:"state"`
	Procedure       string           `json:"procedure"`
	Classification  string           `json:"classification"`
	Attempts        int              `json:"attempts"`
	LastError       *string          `json:"lastError"`
	CreatedAt       string           `json:"createdAt"`
	DeadlineAt      string           `json:"deadlineAt"`
	Note            string           `json:"note"`
	ApprovalRequest *ApprovalRequest `json:"approvalRequest"`
}

// ApprovalRequest is the security object a human reads before deciding.
// Everything on it except the Untrusted block is Conductor-derived.
type ApprovalRequest struct {
	OperationID     string `json:"operationId"`
	PlanID          string `json:"planId"`
	PlanHash        string `json:"planHash"`
	PreflightDigest string `json:"preflightDigest"`
	Procedure       struct {
		Ref     string `json:"ref"`
		Digest  string `json:"digest"`
		Summary string `json:"summary"`
		Owner   string `json:"owner"`
	} `json:"procedure"`
	Scope struct {
		Aid string `json:"aid"`
		Cid string `json:"cid"`
	} `json:"scope"`
	Targets []struct {
		Key        string `json:"key"`
		Kind       string `json:"kind"`
		ID         string `json:"id"`
		Generation string `json:"generation"`
		Shared     bool   `json:"shared"`
	} `json:"targets"`
	Args           map[string]any `json:"args"`
	Classification Classification `json:"classification"`
	Preflight      []Check        `json:"preflight"`
	Effects        []struct {
		Kind        string `json:"kind"`
		Owner       string `json:"owner"`
		Description string `json:"description"`
	} `json:"effects"`
	Stages   []Stage `json:"stages"`
	Recovery struct {
		Kind        string `json:"kind"`
		Description string `json:"description"`
	} `json:"recovery"`
	Postconditions []struct {
		Check             string `json:"check"`
		Description       string `json:"description"`
		StabilityWindowMs int64  `json:"stabilityWindowMs"`
	} `json:"postconditions"`
	Decision Decision `json:"decision"`
	Approval struct {
		Required string `json:"required"`
		// Nonce is null until a decision window is minted, deliberately
		// rather than a placeholder: a field that reads like a nonce and
		// is not one is what a consumer treats as a nonce.
		Nonce     *string  `json:"nonce"`
		ExpiresAt string   `json:"expiresAt"`
		Roles     []string `json:"roles"`
	} `json:"approval"`
	Freezes           []string `json:"freezes"`
	CumulativeHistory struct {
		Entries []struct {
			OperationID      string   `json:"operationId"`
			State            string   `json:"state"`
			Classification   string   `json:"classification"`
			ProcedureVersion *string  `json:"procedureVersion"`
			Targets          []string `json:"targets"`
			RecordedAt       string   `json:"recordedAt"`
		} `json:"entries"`
		Truncated bool   `json:"truncated"`
		Note      string `json:"note"`
	} `json:"cumulativeHistory"`
	Untrusted *Untrusted `json:"untrusted"`
}

// Untrusted is the proposer's half of the render: present only for an
// SRE-mediated operation, never for a direct human one.
type Untrusted struct {
	Rationale          UntrustedText `json:"rationale"`
	ExpectedOutcome    UntrustedText `json:"expectedOutcome"`
	VerificationIntent UntrustedText `json:"verificationIntent"`
	Evidence           []struct {
		Ref     UntrustedText `json:"ref"`
		Summary UntrustedText `json:"summary"`
	} `json:"evidence"`
	Proposer struct {
		AccountSRE         string `json:"accountSre"`
		IncidentID         string `json:"incidentId"`
		InvestigationEpoch string `json:"investigationEpoch"`
		Profile            string `json:"profile"`
		ProposalID         string `json:"proposalId"`
	} `json:"proposer"`
}

// DecisionResult is the 200 from approving or rejecting.
type DecisionResult struct {
	OperationID string `json:"operationId"`
	State       string `json:"state"`
	ExpiresAt   string `json:"expiresAt"`
}

// KillSwitch is one live account-scoped kill switch. Reason is untrusted
// because an account admin wrote it and another account admin reads it.
type KillSwitch struct {
	SwitchID    string        `json:"switchId"`
	Scope       string        `json:"scope"`
	Cid         *string       `json:"cid"`
	ProcedureID *string       `json:"procedureId"`
	Reason      UntrustedText `json:"reason"`
	EngagedAt   string        `json:"engagedAt"`
}

// ScopeFreeze is one live scope freeze. Reason is a PLAIN string here,
// unlike a kill switch's: Conductor's own verifier wrote it, so marking
// it untrusted would tell a reader to discount Conductor's account of
// why it stopped.
type ScopeFreeze struct {
	FreezeID    string `json:"freezeId"`
	ScopeKey    string `json:"scopeKey"`
	Cid         string `json:"cid"`
	OperationID string `json:"operationId"`
	Cause       string `json:"cause"`
	Reason      string `json:"reason"`
	FrozenAt    string `json:"frozenAt"`
}
