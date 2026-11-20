package dynacmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// Story 347 criterion 6: the one test over add, show, update, rotate and
// revoke for both typed credential families (personal-access-tokens,
// account-api-keys), including a widening update, proving S1 (the
// documented challenge completes through the existing human flow), S2
// (the confirmed retry carries the identical body and Idempotency-Key/
// If-Match values) and the lifecycle header/ETag transport story 343/347
// already prove on the cobra path, now exercised through an actual
// step-up challenge and retry.
//
// Fixtures copied from Conductor's own field constants
// (personalAccessTokens.manifest.ts, accountApiKeys.manifest.ts, commit
// 4a7af046, read with repo_file), the same ones
// credential_transport_output_parity_test.go (internal/mcp) uses, so the
// two surfaces cannot silently diverge on what a real manifest entry
// looks like.
var (
	stepUpIdempotencyKeyField = manifest.Field{Name: "idempotencyKey", Type: "string", Required: true, Location: "header"}
	stepUpIfMatchField        = manifest.Field{Name: "ifMatch", Type: "string", Required: true, Location: "header"}
)

type stepUpCase struct {
	name         string
	cmdDef       manifest.Command
	body         map[string]any
	headerFields map[string]string // body field name -> canonical header name
	challenged   bool              // the fake server issues a step-up challenge before succeeding
	isIssuance   bool
	secretField  string
	secretValue  string
	// slowConfirmation makes the fake /cli/stepup/:id poller answer
	// "pending" long enough that the accumulated real WaitForStepUp
	// sleep exceeds apitimeout.Default, so the confirmed retry only
	// succeeds if it runs under a deadline created AFTER the wait,
	// never the challenged request's own (finding 1466).
	slowConfirmation bool
}

var stepUpTestCases = []stepUpCase{
	{
		name: "account/personal-access-tokens/add",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/add", Method: http.MethodPost,
			Endpoint: "/:aid/account/personal-access-tokens",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name", Type: "string", Required: true},
				{Name: "expiresAt", Type: "string", Required: true},
				stepUpIdempotencyKeyField,
			}},
		},
		body:         map[string]any{"name": "ci", "expiresAt": "2027-01-01T00:00:00Z", "idempotencyKey": "11111111-2222-3333-4444-555555555555"},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key"},
		challenged:   true, isIssuance: true, secretField: "secret",
		secretValue: fakeTypedPAT,
	},
	{
		name: "account/personal-access-tokens/show",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/show", Method: http.MethodGet,
			Endpoint: "/:aid/account/personal-access-tokens/:credential",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
			}},
		},
		body: map[string]any{"credential": "pat_abc"},
	},
	{
		name: "account/personal-access-tokens/update",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/update", Method: http.MethodPatch,
			Endpoint: "/:aid/account/personal-access-tokens/:credential",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				{Name: "name", Type: "string"},
				stepUpIfMatchField,
			}},
		},
		body:         map[string]any{"credential": "pat_abc", "name": "renamed", "ifMatch": `"etag-precondition-value"`},
		headerFields: map[string]string{"ifMatch": "If-Match"},
		// A widening update (broader restrictions.actionClasses) is
		// exactly the case Roid documents as needing step-up; the client
		// cannot tell in advance, so this row exercises the challenge
		// path the same as any other eligible command.
		challenged: true,
	},
	{
		name: "account/personal-access-tokens/rotate",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/rotate", Method: http.MethodPost,
			Endpoint: "/:aid/account/personal-access-tokens/:credential/rotate",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				stepUpIdempotencyKeyField, stepUpIfMatchField,
			}},
		},
		body: map[string]any{
			"credential": "pat_abc", "idempotencyKey": "11111111-2222-3333-4444-555555555555",
			"ifMatch": `"etag-precondition-value"`,
		},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key", "ifMatch": "If-Match"},
		challenged:   true, isIssuance: true, secretField: "secret",
		secretValue: fakeTypedPAT,
	},
	{
		name: "account/personal-access-tokens/revoke",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/revoke", Method: http.MethodPost,
			Endpoint: "/:aid/account/personal-access-tokens/:credential/revoke",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				stepUpIfMatchField,
			}},
		},
		body:         map[string]any{"credential": "pat_abc", "ifMatch": `"etag-precondition-value"`},
		headerFields: map[string]string{"ifMatch": "If-Match"},
		// Not in stepUpEligibleCommands: Conductor's revoke command
		// documents no step-up requirement (one-way, immediate).
	},
	{
		name: "account/account-api-keys/add",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/add", Method: http.MethodPost,
			Endpoint: "/:aid/account/account-api-keys",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name", Type: "string", Required: true},
				stepUpIdempotencyKeyField,
			}},
		},
		body:         map[string]any{"name": "ci-key", "idempotencyKey": "22222222-3333-4444-5555-666666666666"},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key"},
		challenged:   true, isIssuance: true, secretField: "secret",
		secretValue: fakeTypedKey,
	},
	{
		name: "account/account-api-keys/show",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/show", Method: http.MethodGet,
			Endpoint: "/:aid/account/account-api-keys/:credential",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
			}},
		},
		body: map[string]any{"credential": "key_abc"},
	},
	{
		name: "account/account-api-keys/update",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/update", Method: http.MethodPatch,
			Endpoint: "/:aid/account/account-api-keys/:credential",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				{Name: "name", Type: "string"},
				stepUpIfMatchField,
			}},
		},
		body:         map[string]any{"credential": "key_abc", "name": "renamed", "ifMatch": `"etag-precondition-value"`},
		headerFields: map[string]string{"ifMatch": "If-Match"},
		// Account API Keys never need step-up for update (no action
		// classes to widen); not in stepUpEligibleCommands.
	},
	{
		name: "account/account-api-keys/rotate",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/rotate", Method: http.MethodPost,
			Endpoint: "/:aid/account/account-api-keys/:credential/rotate",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				stepUpIdempotencyKeyField, stepUpIfMatchField,
			}},
		},
		body: map[string]any{
			"credential": "key_abc", "idempotencyKey": "22222222-3333-4444-5555-666666666666",
			"ifMatch": `"etag-precondition-value"`,
		},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key", "ifMatch": "If-Match"},
		challenged:   true, isIssuance: true, secretField: "secret",
		secretValue: fakeTypedKey,
	},
	{
		name: "account/account-api-keys/revoke",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/revoke", Method: http.MethodPost,
			Endpoint: "/:aid/account/account-api-keys/:credential/revoke",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				stepUpIfMatchField,
			}},
		},
		body:         map[string]any{"credential": "key_abc", "ifMatch": `"etag-precondition-value"`},
		headerFields: map[string]string{"ifMatch": "If-Match"},
	},
}

func TestStepUpAndLifecycleTransport(t *testing.T) {
	// Exhaustive by the acceptance criterion's own enumeration: add,
	// show, update, rotate, revoke, for both typed families. A row
	// added or removed here must keep this count in step, so a typed
	// mutation silently dropped from the table is caught immediately.
	if len(stepUpTestCases) != 10 {
		t.Fatalf("stepUpTestCases has %d rows, want 10 (5 verbs x 2 families)", len(stepUpTestCases))
	}
	for _, tc := range stepUpTestCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runStepUpCase(t, tc)
		})
	}
}

// stepUpPendingPollsBeforeProven is the number of "pending" polls the
// slowConfirmation server sends before "proven". DefaultStepUpWait's
// interval is 2s, and WaitForStepUp sleeps BEFORE each retry poll, so
// the Nth poll happens after (N-1)*interval of accumulated real sleep:
// 16 pending polls put the 17th (proven) poll at ~32s elapsed, past
// apitimeout.Default (30s) -- exactly the window finding 1466 needs to
// distinguish a fresh retry deadline from the challenged request's
// stale one.
const stepUpPendingPollsBeforeProven = 16

func runStepUpCase(t *testing.T, tc stepUpCase) {
	t.Helper()
	const stepUpID = "su_12345"

	var mu sync.Mutex
	var businessHeaders []http.Header
	var businessBodies [][]byte
	callCount := 0
	stepUpPolls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cli/stepup/") {
			mu.Lock()
			stepUpPolls++
			n := stepUpPolls
			mu.Unlock()
			state := "proven"
			if tc.slowConfirmation && n <= stepUpPendingPollsBeforeProven {
				state = "pending"
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"` + state + `"}`))
			return
		}
		mu.Lock()
		callCount++
		n := callCount
		b, _ := io.ReadAll(r.Body)
		businessBodies = append(businessBodies, b)
		businessHeaders = append(businessHeaders, r.Header.Clone())
		mu.Unlock()

		if tc.challenged && n == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"requirement":"step_up","step_up_id":"` + stepUpID + `","step_up_url":"https://issuer.example.test/stepup/` + stepUpID + `"}}`))
			return
		}
		w.Header().Set("ETag", `W/"server-etag-value"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(lifecycleResponseBody(tc)))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{ConductorURL: srv.URL, AuthIssuer: srv.URL, AccountID: "acct1"}
	body := map[string]any{}
	for k, v := range tc.body {
		body[k] = v
	}

	respBody, respHeaders, err := NewExecutor(srv.URL).dispatch(tc.cmdDef, nil, body, "", cfg, "test-token")
	if err != nil {
		t.Fatalf("dispatch returned error: %v", err)
	}
	// dispatch is the lower-level method Execute calls before applying
	// the output guard and ETag read-back (executor.go); apply them
	// here the same way, in the same order, so this test exercises the
	// real production composition rather than a copy of it.
	if err := GuardCredentialResponse(tc.cmdDef, respBody); err != nil {
		t.Fatalf("GuardCredentialResponse refused a legitimate response: %v", err)
	}
	respBody, err = InjectETagIntoCredentialResponse(tc.cmdDef, respBody, respHeaders)
	if err != nil {
		t.Fatalf("InjectETagIntoCredentialResponse returned an error: %v", err)
	}

	wantCalls := 1
	if tc.challenged {
		wantCalls = 2
	}
	if callCount != wantCalls {
		t.Fatalf("business endpoint called %d times, want %d", callCount, wantCalls)
	}

	last := businessHeaders[len(businessHeaders)-1]
	lastBody := businessBodies[len(businessBodies)-1]
	var decodedLastBody map[string]any
	_ = json.Unmarshal(lastBody, &decodedLastBody)

	// Lifecycle headers: present as their canonical header, absent from
	// the body.
	for field, canonical := range tc.headerFields {
		want, _ := tc.body[field].(string)
		if got := last.Get(canonical); got != want {
			t.Errorf("header %s = %q, want %q", canonical, got, want)
		}
		if _, present := decodedLastBody[field]; present {
			t.Errorf("header-located field %q also appeared in the decoded body %s", field, lastBody)
		}
	}

	if tc.challenged {
		if got := last.Get("X-RunOS-Step-Up"); got != stepUpID {
			t.Errorf("retry missing X-RunOS-Step-Up, got %q", got)
		}
		firstBody, firstHeaders := businessBodies[0], businessHeaders[0]
		// S2: identical request body and identical Idempotency-Key/If-Match
		// values across the challenged request and the confirmed retry.
		if string(firstBody) != string(lastBody) {
			t.Errorf("retry body differs from the challenged request:\n  first: %s\n  retry: %s", firstBody, lastBody)
		}
		for _, canonical := range tc.headerFields {
			if firstHeaders.Get(canonical) != last.Get(canonical) {
				t.Errorf("header %s differs between the challenged request and the retry: %q vs %q", canonical, firstHeaders.Get(canonical), last.Get(canonical))
			}
		}
	}

	// ETag round trip: the response's ETag header reaches the output as
	// a top-level "etag" field.
	if !strings.Contains(string(respBody), `"etag":"W/\"server-etag-value\""`) {
		t.Errorf("output did not carry the response ETag: %s", respBody)
	}

	if tc.isIssuance {
		if n := strings.Count(string(respBody), tc.secretValue); n != 1 {
			t.Errorf("one-shot secret appeared %d times in output, want exactly 1: %s", n, respBody)
		}
	}
}

func lifecycleResponseBody(tc stepUpCase) string {
	obj := map[string]any{"credential_id": "cred_abc", "name": "a-normal-value"}
	if tc.isIssuance {
		obj[tc.secretField] = tc.secretValue
	}
	encoded, _ := json.Marshal(obj)
	return string(encoded)
}

// TestStepUpRetryUsesFreshDeadlineAfterSlowConfirmation is finding
// 1466's closure test: completeStepUpIfRequired previously reused the
// CHALLENGED request's own context (apitimeout.Default, 30s for every
// step-up-eligible command) for the confirmed retry, created before the
// human confirmation wait. A real confirmation routinely takes longer
// than the residual budget left in that 30s window, so the retry failed
// with a deadline-exceeded error even though the step-up had already
// succeeded and been spent on Roid's side -- refusing legitimate,
// already-confirmed work.
//
// Runs one step-up-eligible command (account/personal-access-tokens/add)
// through a real, un-shortened WaitForStepUp: the fake /cli/stepup/
// poller answers "pending" for stepUpPendingPollsBeforeProven (16) real
// 2-second polls -- about 32s of actual elapsed time, past the 30s
// apitimeout.Default -- before answering "proven". This test takes
// about 32 seconds to run by design: that elapsed time IS the defect
// window the fix must survive.
//
// Fails on the pre-fix code (the retry's deadline, inherited from the
// challenged request, has already expired) and passes after it (the
// retry gets its own fresh deadline, started after the wait returns).
func TestStepUpRetryUsesFreshDeadlineAfterSlowConfirmation(t *testing.T) {
	tc := stepUpTestCases[0] // account/personal-access-tokens/add
	if tc.name != "account/personal-access-tokens/add" || !tc.challenged {
		t.Fatalf("stepUpTestCases[0] is %q (challenged=%v); this test expects the PAT add row at index 0", tc.name, tc.challenged)
	}
	tc.slowConfirmation = true
	runStepUpCase(t, tc)
}

// TestStepUpNeverAttemptedForIneligibleCommand proves S3 at the
// mechanism level available to a unit test: a step-up-SHAPED refusal
// from a command stepUpEligibleCommands does not name is never
// retried, so the server's structured refusal (whatever shape it took)
// reaches the caller unchanged and the human-confirmation flow never
// runs. This is the same protection that keeps a machine credential's
// 403 (which the server sends as a plain human-only refusal, never the
// step-up shape, for every command in this family) from ever reaching
// the human challenge: nothing here simulates a human for ANY refusal
// outside the exact list story 347 S1 names.
func TestStepUpNeverAttemptedForIneligibleCommand(t *testing.T) {
	cmdDef := manifest.Command{
		Command: "account/api-keys/add", Method: http.MethodPost,
		Endpoint: "/:aid/account/api-keys",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "name", Type: "string", Required: true},
		}},
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"requirement":"step_up","step_up_id":"su_x","step_up_url":"https://issuer.example.test/stepup/su_x"}}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{ConductorURL: srv.URL, AuthIssuer: srv.URL, AccountID: "acct1"}
	_, _, err := NewExecutor(srv.URL).dispatch(cmdDef, nil, map[string]any{"name": "ci"}, "", cfg, "test-token")
	if err == nil {
		t.Fatal("expected the step-up-shaped refusal to be returned, not silently resolved")
	}
	if calls != 1 {
		t.Fatalf("business endpoint called %d times, want exactly 1 (no retry for an ineligible command)", calls)
	}
	if !strings.Contains(err.Error(), "step_up") && !strings.Contains(err.Error(), "403") {
		t.Errorf("error does not look like the server's own refusal: %v", err)
	}
}
