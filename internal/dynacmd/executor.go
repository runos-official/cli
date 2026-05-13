package dynacmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/jobs"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/output"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Executor executes commands by calling the API
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// NewExecutor creates a new command executor
func NewExecutor(baseURL string) *Executor {
	return &Executor{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// APIError is returned by ExecuteWithInput (and the shared dispatch path)
// when the conductor responds with a non-2xx status. Callers can errors.As
// it to format the body specially (e.g. format the dependents list out of a
// 409 from services delete).
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error renders the same one-liner the historic Execute path emitted, so
// behaviour is unchanged for callers that don't unwrap.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, string(e.Body))
}

// BuildAPIErrorEnvelope returns the canonical JSON-error envelope shape
// for any error a CLI command's --json path might produce. Two cases:
//
//  1. err chain carries a typed *APIError whose body parses as
//     {"error": "..."}: envelope is {"error": <inner message>,
//     "statusCode": <code>}, plus every other top-level field the
//     conductor emitted (e.g. `upstream: {provider, status, body}` for
//     I15-C upstream-provider faults). The wrapper "API error (NNN): {...}"
//     prefix is dropped — pre-fix that produced the I10-G double-encode
//     where the inner JSON arrived as a JSON-encoded string inside the
//     top-level envelope, forcing CI consumers to `jq -r .error | jq`.
//  2. Anything else (non-APIError, or APIError with non-JSON body):
//     envelope is {"error": err.Error()} (plus statusCode when APIError).
//
// Pass-through of unknown top-level fields is deliberate forward-compat:
// any structured-error key conductor adds in the future surfaces to CI
// consumers without a CLI release.
//
// Used by both dynacmd's executor.emitJSONErrorAndSilence and
// cmd/apps_pull.go's emitJSONError so every --json failure path lands
// on the same shape.
func BuildAPIErrorEnvelope(err error) map[string]any {
	envelope := map[string]any{"error": err.Error()}
	var apiErr *APIError
	for unwrapped := err; unwrapped != nil; {
		if e, ok := unwrapped.(*APIError); ok {
			apiErr = e
			break
		}
		type unwrapper interface{ Unwrap() error }
		if uw, ok := unwrapped.(unwrapper); ok {
			unwrapped = uw.Unwrap()
			continue
		}
		break
	}
	if apiErr == nil {
		return envelope
	}
	envelope["statusCode"] = apiErr.StatusCode
	var inner map[string]any
	if jErr := json.Unmarshal(apiErr.Body, &inner); jErr == nil {
		// Forward unknown top-level fields from the conductor body.
		// `error` is overridden below (extracted as string for the I10-G
		// double-encode fix); `statusCode` is taken from the typed
		// APIError above (authoritative — comes from the HTTP status,
		// not the body which can lie or omit it).
		for k, v := range inner {
			if k == "error" || k == "statusCode" {
				continue
			}
			envelope[k] = v
		}
		if msg, ok := inner["error"].(string); ok && msg != "" {
			envelope["error"] = msg
		}
	}
	return envelope
}

// emitJSONErrorAndSilence prints a JSON error envelope to stdout and
// silences cobra's default plain-text "Error: ..." print so the
// caller's --json contract is respected even on the failure path
// (I4-G). Returns the original error so cobra still exits non-zero.
//
// Delegates to BuildAPIErrorEnvelope for the envelope shape; see that
// helper for the unwrapping rules.
func emitJSONErrorAndSilence(cmd *cobra.Command, err error) error {
	if cmd != nil {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
	}
	envelope := BuildAPIErrorEnvelope(err)
	out, mErr := json.MarshalIndent(envelope, "", "  ")
	if mErr != nil {
		fmt.Printf(`{"error":%q}`+"\n", err.Error())
		return err
	}
	fmt.Println(string(out))
	return err
}

// Execute runs the command
func (e *Executor) Execute(cmd *cobra.Command, args []string, cmdDef manifest.Command) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// wrapPreNetwork routes a pre-network refusal through the same
	// machine-parseable envelope shape as a dispatch failure when --json
	// is set. Pre-fix the I9-A/I9-E/empty-input refusals printed bare
	// "Error: ..." text on stderr under --json, forcing CI consumers to
	// dual-parse text-mode errors (I11-B). With this wrapper every leaf
	// refusal lands on the same JSON contract.
	wrapPreNetwork := func(err error) error {
		if err == nil {
			return nil
		}
		if jsonOutput {
			return emitJSONErrorAndSilence(cmd, err)
		}
		return err
	}

	// Get auth token
	cfg, err := config.Load()
	if err != nil {
		return wrapPreNetwork(fmt.Errorf("failed to load config: %w", err))
	}

	token, err := e.getAuthToken(cfg)
	if err != nil {
		return wrapPreNetwork(fmt.Errorf("authentication required: run 'runos login' first (%w)", err))
	}

	// Get cluster ID. Four sources in priority order, top wins:
	//   1. --cid flag (explicit override).
	//   2. positional [cid] arg, when the command's manifest declares cid
	//      as a positional field (e.g. clusters/show). Without this, a
	//      user typing `runos clusters show notreal` had the typo
	//      silently swallowed and the default cluster's data returned
	//      with exit 0. Regression target: I7-C / I7-D.
	//   3. CLI config default (`runos config set cid <id>`).
	//   4. (no source) -> buildEndpoint errors with "cluster ID required".
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = positionalArgForField(args, cmdDef, "cid")
	}
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}

	// Collect input
	body, err := e.collectInput(cmd, args, cmdDef)
	if err != nil {
		return wrapPreNetwork(fmt.Errorf("failed to collect input: %w", err))
	}

	// `user/permissions` keys on the caller's own Firebase uid (the
	// route accepts either the uid or the account-user docId per
	// iter-12 conductor R1 fix for I12-C). LLMs and CI users shouldn't
	// have to remember and paste their own uid every time, so default
	// the positional from the JWT when omitted. Regression target: I12-D.
	//
	// I12-C R2: only inject when NEITHER the positional slot NOR the
	// flag carry a value. Pre-fix the auto-fill ran whenever body["uid"]
	// was empty, but `body` doesn't see positional args — those are
	// substituted into the URL path by buildEndpoint. A user passing
	// the docId form as positional (a path conductor R1 explicitly
	// added support for) hit the I8-F ambiguity guard because the
	// auto-fill silently filled `--uid` with the firebase uid while
	// the positional carried the docId, and the two disagreed.
	if cmdDef.Command == "user/permissions" {
		if uidProvided(args, cmdDef, body, "uid") {
			// User-supplied; honour it verbatim and let conductor's
			// docId-or-uid lookup handle the resolution.
		} else if extracted := auth.ExtractFirebaseUID(token); extracted != "" {
			body["uid"] = extracted
		}
	}

	// Refuse positional/flag disagreement on the same field so a typo
	// in one slot doesn't silently let the other slot's value win and
	// target the wrong resource (I8-F). I7-C/D wired positional `cid`
	// through; this closes the sibling "both slots filled with
	// different values" case for every positional field (id, cid,
	// overrideId, filename, cliUploadId, ...).
	if err := validatePositionalFlagAgreement(args, cmdDef, body); err != nil {
		return wrapPreNetwork(err)
	}

	// Refuse negative values for non-negative integer fields and empty
	// strings for required string fields before the network hit. The
	// server side rejects these too (clear 400 since the iter-9 conductor
	// round) but a CLI-side gate gives a faster, more specific error and
	// keeps the request off the wire. Regression targets: I9-A (negative
	// --tail / --since on apps_logs), I9-E client half (empty --id ""
	// reaching the server with the :id placeholder).
	if err := validateInputValues(args, cmdDef, body); err != nil {
		return wrapPreNetwork(err)
	}

	// `cluster-domains show --id runos` targets the synthetic per-cluster
	// runos row, which is ambiguous without a cluster scope (the same id
	// exists once per cluster in the account). The endpoint is global
	// (/:aid/cluster-domains/:id), so even passing --cid here can't reach
	// the right scope. Refuse with a redirect to list-by-cluster instead
	// of letting the user hit an arbitrary cluster's row. Regression
	// target: I11-W.
	if cmdDef.Command == "cluster-domains/{id}/show" || cmdDef.Command == "cluster-domains/show" {
		if id, _ := body["id"].(string); id == "runos" {
			return wrapPreNetwork(fmt.Errorf("`runos` is a synthetic per-cluster cluster-domain; use `runos cluster-domains list-by-cluster --cid <cid>` to see it scoped to a specific cluster"))
		}
	}

	// Auto-inject CLI version + OS for `cli/version-check`. The MCP wrapper
	// already does this so the answer is correct under MCP; the bare CLI
	// path used to send empty version, which produced a misleading
	// `updateAvailable: true` and an alarming `releaseNotes` sentinel.
	// Injection mirrors the MCP wrapper's behaviour at internal/mcp/server.go.
	if cmdDef.Command == "cli/version-check" {
		if _, ok := body["version"]; !ok || isEmptyString(body["version"]) {
			body["version"] = cliRuntimeVersion()
		}
		if _, ok := body["os"]; !ok || isEmptyString(body["os"]) {
			body["os"] = cliRuntimeOS()
		}
	}

	respBody, err := e.dispatch(cmdDef, args, body, cid, cfg, token)
	if err != nil {
		// Conductor's services delete returns 409 with a structured
		// dependents list when other apps/services reference the
		// target. Render it as a multi-line message so the user
		// (and any LLM running this) immediately sees what's blocking
		// the delete instead of a JSON dump in the default
		// APIError.Error() form.
		if msg, ok := formatDependentsError(err); ok {
			err = fmt.Errorf("%s", msg)
		}
		// I4-G: with --json set, errors must be machine-parseable
		// stdout output too — pre-fix the error went to cobra's
		// default plain-text "Error: ..." stderr path, breaking CI
		// pipelines that pipe stdout into jq. Print the structured
		// envelope, suppress cobra's print, return the original error
		// so the exit code stays non-zero.
		if jsonOutput {
			return emitJSONErrorAndSilence(cmd, err)
		}
		return err
	}

	// Handle --follow flag for commands that return jobs (detected by jobId in output)
	if hasJobIdOutput(cmdDef) {
		follow, _ := cmd.Flags().GetBool("follow")
		if follow {
			return e.followJob(respBody)
		}
	}

	// Add default indicator for clusters/list (only for plain text output)
	if cmdDef.Command == "clusters/list" && !jsonOutput {
		respBody = markDefaultCluster(respBody, cfg.DefaultClusterID)
	}

	// Pod-logs readers (apps/logs and services/<type>/{id}/logs) with
	// `--previous` return a synthetic single-entry array with
	// `containerName: "<diagnostic>"` when no previous container instance
	// is available, mixing the CLI hint into the log stream so JSON
	// consumers see "1 log entry" but it's actually a diagnostic. Lift
	// the diagnostic out of the array into a top-level envelope under
	// --json (`{diagnostic, logs: []}`) and print it as a single notice
	// in text mode (skipping the table renderer entirely). Regression
	// targets: I11-S (apps/logs), I17-F (services/<type>/logs parity).
	if isPodLogsCommand(cmdDef.Command) {
		if diagMsg, ok := extractLogsDiagnostic(respBody); ok {
			if jsonOutput {
				envelope := map[string]any{
					"diagnostic": diagMsg,
					"logs":       []any{},
				}
				out, err := json.MarshalIndent(envelope, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStderr(), diagMsg)
			return nil
		}
	}

	formatter := output.NewFormatter(jsonOutput)

	err = formatter.Format(respBody, cmdDef.Output)
	if err != nil {
		return err
	}

	// I14-E + I17-F: pod-logs readers (apps/logs and
	// services/<type>/{id}/logs) with `--follow` poll every 2s and
	// stream new entries (kubectl `logs -f` mental model). The initial
	// batch was already printed by the formatter above; this loops
	// until the context is cancelled (^C) or an upstream error fires.
	// Dedup by timestamp + pod + container + message so an overlapping
	// window doesn't re-emit lines. JSON mode still emits one JSON
	// array per poll for downstream stream parsers.
	if isPodLogsCommand(cmdDef.Command) {
		if follow, _ := cmd.Flags().GetBool("follow"); follow {
			return e.followPodLogs(cmd, cmdDef, args, body, cid, cfg, token, respBody, jsonOutput)
		}
	}

	// `account api-keys add` returns a one-shot bearer token that can
	// never be retrieved again. The default key:value formatter rendered
	// the token and warning as just two more aligned lines, easy to scan
	// past in a busy terminal. Print an ASCII-framed banner under the
	// table so the user sees the warning visually before they exit the
	// shell. JSON consumers already get a structured field. Regression
	// target: I11-F.
	if cmdDef.Command == "account/api-keys/add" && !jsonOutput {
		printApiKeyTokenBanner(cmd.OutOrStdout(), respBody)
	}

	// `account notify-keys add` mirrors api-keys/add: the keyValue is
	// shown once and the server stores only a hash. Same banner so
	// terminal users don't miss it. JSON consumers already get a
	// structured `keyValue` field. Regression target: I12-H.
	if cmdDef.Command == "account/notify-keys/add" && !jsonOutput {
		printNotifyKeyBanner(cmd.OutOrStdout(), respBody)
	}

	// Print footer note for clusters/list (only in plain text mode to a terminal)
	if cmdDef.Command == "clusters/list" && !jsonOutput && cfg.DefaultClusterID != "" && term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println()
		fmt.Println("* default cluster - commands use this cluster if --cid is not specified")
	}

	// `tools/domain-check` returns 200 OK for every outcome (matched,
	// not-matched, degraded) so CI gates that rely on the exit code
	// silently treat "DNS doesn't point at the cluster" as success
	// (I11-J). Exit non-zero when matchStatus is anything other than
	// "healthy" so the command is usable as a wait-for-DNS step in a
	// pipeline. JSON / text output already printed; only the return
	// value flips.
	if cmdDef.Command == "tools/domain-check" {
		if err := domainCheckExitGate(respBody); err != nil {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			return err
		}
	}

	return nil
}

// printApiKeyTokenBanner prints an ASCII-framed reminder under the
// formatter's table output for `account api-keys add`. Surfaces the
// one-shot token + warning so terminal users don't miss them in a busy
// session. Silently no-ops when the response body lacks a token (e.g.
// when the conductor returned a partial response shape) to avoid
// blowing up the call site. Regression target: I11-F.
func printApiKeyTokenBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	token, _ := item["token"].(string)
	if token == "" {
		return
	}
	warning, _ := item["warning"].(string)
	if warning == "" {
		warning = "This token will not be shown again. Store it in a secret manager."
	}
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  ONE-SHOT TOKEN - save it now, it cannot be retrieved")
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+token)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+warning)
	fmt.Fprintln(w, bar)
}

// printNotifyKeyBanner prints the same banner shape as
// printApiKeyTokenBanner for `account notify-keys add`. Notify keys are
// the symmetric notification-webhook credential and have the same
// store-the-hash, return-the-secret-once contract. Silently no-ops
// when the response body lacks `keyValue` so a partial conductor shape
// can't blow up the call site. Regression target: I12-H (mirror of
// I11-F for PATs).
func printNotifyKeyBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	keyValue, _ := item["keyValue"].(string)
	if keyValue == "" {
		return
	}
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  ONE-SHOT NOTIFY KEY - save it now, it cannot be retrieved")
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+keyValue)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  This key will not be shown again. Store it in a secret manager.")
	fmt.Fprintln(w, bar)
}

// extractLogsDiagnostic recognises the conductor's synthetic
// diagnostic entry that apps/logs returns when --previous is set but no
// previous container instance is available. The shape is a single-entry
// array with `containerName == "<diagnostic>"`; everything else is a
// real log line. Returns the message and true on a match; ("", false)
// for any other body so the normal log formatter path runs.
// Regression target: I11-S.
func extractLogsDiagnostic(respBody []byte) (string, bool) {
	var items []map[string]any
	if err := json.Unmarshal(respBody, &items); err != nil {
		return "", false
	}
	if len(items) != 1 {
		return "", false
	}
	container, _ := items[0]["containerName"].(string)
	if container != "<diagnostic>" {
		return "", false
	}
	msg, _ := items[0]["message"].(string)
	if msg == "" {
		return "", false
	}
	return msg, true
}

// domainCheckExitGate parses the tools/domain-check response and
// returns a non-nil error when the result is anything other than a
// healthy match. Two ways to fail the gate: matchStatus is set and not
// "healthy" (degraded etc.), OR the response carries matched=false (the
// "domain doesn't resolve to any node in this account" shape has no
// matchStatus field at all). Callers silence cobra's error printing
// because the structured body (json or table) was already emitted by
// the formatter; the error exists only to flip the process exit code.
// Regression targets: I11-J R1 (matchStatus gate) + I11-J R2 (also fail
// when matched=false to cover the not-matched response shape).
func domainCheckExitGate(respBody []byte) error {
	var resp struct {
		Matched                *bool  `json:"matched"`
		MatchStatus            string `json:"matchStatus"`
		MatchStatusDescription string `json:"matchStatusDescription"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil
	}
	if resp.MatchStatus != "" && resp.MatchStatus != "healthy" {
		if resp.MatchStatusDescription != "" {
			return fmt.Errorf("domain check %s: %s", resp.MatchStatus, resp.MatchStatusDescription)
		}
		return fmt.Errorf("domain check %s", resp.MatchStatus)
	}
	if resp.Matched != nil && !*resp.Matched {
		return fmt.Errorf("domain check not-matched: DNS did not resolve to any node in this account")
	}
	return nil
}

// ExecuteWithInput drives a manifest command without going through cobra
// flag parsing. Used by static commands (e.g. services_pull / services_diff
// / services_sync) that already have their input as a typed map. Returns
// the raw response body on 2xx; on non-2xx, returns an *APIError that
// carries the status code and the raw body so callers can format it (e.g.
// 409 dependents list out of services delete).
//
// positionalArgs feeds the same buildEndpoint path that Execute uses, so
// fields marked positional in the manifest are substituted into the URL.
// input contains every non-positional value the command needs (PATCH/POST
// body fields, GET/DELETE query parameters); the dispatch path filters out
// keys that double as path parameters.
//
// cid empty falls back to the default cluster id from config, matching
// Execute's "no --cid means use default" behaviour.
func (e *Executor) ExecuteWithInput(cmdDef manifest.Command, positionalArgs []string, input map[string]any, cid string) ([]byte, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	token, err := e.getAuthToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("authentication required: run 'runos login' first (%w)", err)
	}
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	return e.dispatch(cmdDef, positionalArgs, input, cid, cfg, token)
}

// formatDependentsError checks whether err is an *APIError carrying a
// 409 with a structured dependents body (the shape conductor's services
// delete handler returns when other apps/services reference the target).
// Returns a friendly multi-line rendering plus true; (_, false) for any
// other error so callers can fall back to the default formatting.
//
// This is a generic helper; nothing about it is services-specific. Any
// future endpoint that surfaces a 409+dependents body gets the same
// treatment automatically.
func formatDependentsError(err error) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusConflict {
		return "", false
	}
	var body struct {
		Error      string `json:"error"`
		Dependents []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Alias string `json:"alias"`
		} `json:"dependents"`
	}
	if err := json.Unmarshal(apiErr.Body, &body); err != nil {
		return "", false
	}
	if len(body.Dependents) == 0 {
		return "", false
	}
	var sb strings.Builder
	if body.Error != "" {
		sb.WriteString("refused: ")
		sb.WriteString(body.Error)
		sb.WriteString("\n")
	} else {
		sb.WriteString("refused: this resource has dependents\n")
	}
	sb.WriteString("dependents:\n")
	for _, d := range body.Dependents {
		switch {
		case d.Alias != "" && d.Name != "":
			sb.WriteString(fmt.Sprintf("  - %s %s (%s), alias %q\n", d.Type, d.Name, d.ID, d.Alias))
		case d.Alias != "":
			sb.WriteString(fmt.Sprintf("  - %s (%s), alias %q\n", d.Type, d.ID, d.Alias))
		case d.Name != "":
			sb.WriteString(fmt.Sprintf("  - %s %s (%s)\n", d.Type, d.Name, d.ID))
		default:
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", d.Type, d.ID))
		}
	}
	sb.WriteString("Reconcile each dependent (e.g. update its requires: to point elsewhere, or delete it first) and re-run.")
	return sb.String(), true
}

// appendMergeQuery returns endpoint with `merge=true` appended as a
// query string parameter, preserving any existing query (e.g.
// `?foo=bar` becomes `?foo=bar&merge=true`). Idempotent: a second
// call doesn't double-add. Pure string operation; no URL parsing.
func appendMergeQuery(endpoint string) string {
	if strings.Contains(endpoint, "merge=true") {
		return endpoint
	}
	if strings.Contains(endpoint, "?") {
		return endpoint + "&merge=true"
	}
	return endpoint + "?merge=true"
}

// dispatch is the shared HTTP path used by both Execute and
// ExecuteWithInput. It builds the endpoint, filters out path-param fields
// from the body, sends the request, and reads the response. Non-2xx
// responses are returned as *APIError so callers can branch on status.
func (e *Executor) dispatch(cmdDef manifest.Command, args []string, body map[string]any, cid string, cfg *config.Config, token string) ([]byte, error) {
	endpoint, err := e.buildEndpoint(cmdDef.Endpoint, args, cmdDef, cfg, cid, body)
	if err != nil {
		return nil, err
	}
	// I4-K CLI follow-up: `apps update` is a partial-PATCH command (the
	// user supplies a few fields, e.g. `--replicas 3`). Without
	// `?merge=true` the conductor's pre-fix desired-state semantics
	// silently zero cpu/memory and clear the 5 healthCheck/metrics
	// fields whenever they're omitted. The conductor shipped the merge
	// param specifically for partial-PATCH callers; the dynacmd
	// dispatch path opts in here so every CLI surface (and the MCP
	// wrapper that shells via dynacmd) gets the safe semantics.
	if cmdDef.Command == "apps/update" {
		endpoint = appendMergeQuery(endpoint)
	}
	requestBody := filterPathParamsFromBody(body, cmdDef)
	resp, err := e.doRequest(cmdDef.Method, endpoint, requestBody, token)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: respBody}
	}
	// Defensive: some conductor handlers wrap their own error in the 200
	// response body instead of letting the framework's error middleware
	// emit a real 4xx status (observed on `apps builds` returning
	// `{"error":"App 'X' not found","statusCode":404}` with HTTP 200,
	// I11-Q). The CLI used to dump the envelope through the JSON
	// formatter and exit 0, silently passing a "not found" as success in
	// CI pipelines. Synthesise an *APIError from the embedded statusCode
	// so the standard error path runs and the exit code is non-zero.
	if apiErr := apiErrorFromBody(resp.StatusCode, respBody); apiErr != nil {
		return nil, apiErr
	}
	return respBody, nil
}

// apiErrorFromBody recognises the conductor's error-envelope shape
// (`{"error": string, "statusCode": int >= 400}`) inside an otherwise
// successful 2xx response. Returns nil for any other shape (so happy-path
// 2xx bodies pass through unchanged) and for any envelope whose embedded
// statusCode is not a client/server error code. The check requires both
// keys to avoid false positives on legitimate payloads that happen to
// include just one of them.
func apiErrorFromBody(httpStatus int, body []byte) *APIError {
	if httpStatus >= 400 {
		return nil
	}
	var envelope struct {
		Error      string `json:"error"`
		StatusCode int    `json:"statusCode"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	if envelope.Error == "" || envelope.StatusCode < 400 {
		return nil
	}
	return &APIError{StatusCode: envelope.StatusCode, Body: body}
}

// followPodLogs implements `--follow` as a poll-and-stream loop on top
// of the single-shot pod-logs endpoints: `/:aid/:cid/apps/:id/logs` and
// `/:aid/:cid/services/<type>/:id/logs`. The conductor doesn't expose a
// streaming surface for either, so the CLI polls every 2s, deduplicates
// emitted entries by their `timestamp + podName + containerName + message`
// key, and prints new rows as they appear. Exits cleanly on SIGINT (^C).
// Mirrors the kubectl-`logs -f` mental model. I14-E shipped this for
// apps/logs; I17-F extended it to every service-type logs reader so the
// surface is uniform.
//
// initialResp carries the first batch (already printed by the
// formatter above), so the seen-set starts seeded with those keys to
// avoid double-emitting on the first poll's overlap window.
func (e *Executor) followPodLogs(cmd *cobra.Command, cmdDef manifest.Command, args []string, body map[string]any, cid string, cfg *config.Config, token string, initialResp []byte, jsonOutput bool) error {
	const pollInterval = 2 * time.Second
	const pollOverlapSeconds = 3 // small window > pollInterval so we don't miss bursts

	seen := make(map[string]struct{})
	seedSeenSet(initialResp, seen)

	// Subsequent polls use a fresh, small `since` window so the conductor
	// returns only the recent slice. Replace the user's --tail / --since
	// with the small follow window; the initial batch already honoured
	// the user's args.
	pollBody := make(map[string]any, len(body))
	for k, v := range body {
		pollBody[k] = v
	}
	pollBody["since"] = pollOverlapSeconds
	pollBody["tail"] = 0 // empty/0 falls back to server default; we filter ourselves

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		respBody, err := e.dispatch(cmdDef, args, pollBody, cid, cfg, token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: follow poll failed: %v\n", err)
			continue
		}
		newRows := filterUnseenLogEntries(respBody, seen)
		if len(newRows) == 0 {
			continue
		}
		if jsonOutput {
			out, err := json.MarshalIndent(newRows, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			continue
		}
		// Plain text: one line per entry, matching streamLogEntries' shape.
		for _, it := range newRows {
			ts, _ := it["timestamp"].(string)
			pod, _ := it["podName"].(string)
			msg, _ := it["message"].(string)
			if pod != "" {
				fmt.Printf("%s [%s] %s\n", ts, pod, msg)
			} else {
				fmt.Printf("%s %s\n", ts, msg)
			}
		}
	}
}

// seedSeenSet records each log entry's dedup key in `seen` so the
// follow loop's first poll doesn't re-emit lines from the initial
// batch. Silently skips malformed bodies — at worst the first poll
// double-prints, which is recoverable.
func seedSeenSet(respBody []byte, seen map[string]struct{}) {
	var items []map[string]any
	if err := json.Unmarshal(respBody, &items); err != nil {
		return
	}
	for _, it := range items {
		seen[logEntryKey(it)] = struct{}{}
	}
}

// filterUnseenLogEntries returns the subset of respBody's entries that
// haven't been emitted before, and records the new ones in `seen`.
// Order is preserved. Used by followPodLogs's poll loop. Robust to
// malformed bodies: returns an empty slice rather than erroring.
func filterUnseenLogEntries(respBody []byte, seen map[string]struct{}) []map[string]any {
	var items []map[string]any
	if err := json.Unmarshal(respBody, &items); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		key := logEntryKey(it)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, it)
	}
	return out
}

// logEntryKey builds a stable dedup identity for a log entry. Combines
// the four fields the conductor populates so two byte-identical lines
// from the same pod at the same instant collide (rare but possible,
// and de-duping them on accident is the safe error: missing a real
// duplicate is just one repeated line, while emitting a duplicate
// looks like a bug to the user).
func logEntryKey(it map[string]any) string {
	ts, _ := it["timestamp"].(string)
	pod, _ := it["podName"].(string)
	ctr, _ := it["containerName"].(string)
	msg, _ := it["message"].(string)
	return ts + "\x00" + pod + "\x00" + ctr + "\x00" + msg
}

func (e *Executor) followJob(respBody []byte) error {
	// Extract jobId from response
	var response map[string]any
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	jobID, ok := response["jobId"].(string)
	if !ok {
		return fmt.Errorf("response does not contain jobId")
	}

	return jobs.FollowJob(jobID)
}

// getAuthToken resolves the bearer token for outgoing requests. Prefers
// RUNOS_API_KEY when set (CI/CD path); otherwise falls back to the
// Firebase refresh-token exchange that `runos login` set up.
func (e *Executor) getAuthToken(cfg *config.Config) (string, error) {
	return auth.ResolveToken(cfg)
}

func (e *Executor) collectInput(cmd *cobra.Command, args []string, cmdDef manifest.Command) (map[string]any, error) {
	result := make(map[string]any)

	if cmdDef.Input == nil {
		return result, nil
	}

	// 1. Apply defaults
	for _, field := range cmdDef.Input.Fields {
		if field.Default != nil && !field.Positional {
			result[field.Name] = field.Default
		}
	}
	for _, flag := range cmdDef.Input.Flags {
		result[flag.Name] = flag.Default
	}

	// 2. Load from file if -f provided
	filePath, _ := cmd.Flags().GetString("file")
	if filePath != "" {
		// I24-G: when the command has no body input (only positional path
		// params, e.g. `apps replace-manifest` takes just the app id),
		// refuse the file flag with a typed error rather than silently
		// merging fields that won't reach the wire. The CLI registers
		// `-f`/`--file` uniformly on every command (per I24-G), so this
		// gate replaces I11-U's prior strategy of skipping registration
		// entirely; the error now reads as "this command has no body
		// input" instead of cobra's misleading "unknown flag --file".
		if !hasNonPositionalInput(cmdDef) {
			return nil, fmt.Errorf("command %q has no body input; the -f / --file flag is inert here. Pass arguments via the documented positional / flag form instead (run with --help to see them)", cmdDef.Command)
		}
		fileData, err := loadYAMLFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load file: %w", err)
		}
		for k, v := range fileData {
			result[k] = v
		}
	}

	// 3. Override with flags. The flag-spelling and the body-key
	// diverge: the user types `--public-read` (kebab-case, idiomatic)
	// while the wire body still carries `publicRead` (manifest's
	// `field.Name`, what conductor expects). flagNameFor maps between
	// the two.
	for _, field := range cmdDef.Input.Fields {
		flagName := flagNameFor(field.Name)
		if field.Positional {
			// Honour the `--<name>` flag form for every typed positional
			// field (not just strings). The builder now registers
			// integer- and boolean-typed flag forms too so users can
			// write `notify-keys update --id 42 --name X` interchangeably
			// with the positional form (I12-I). The body carries the
			// typed value; buildEndpoint stringifies it for URL
			// substitution.
			if cmd.Flags().Changed(flagName) {
				switch field.Type {
				case "string":
					val, _ := cmd.Flags().GetString(flagName)
					result[field.Name] = val
				case "integer":
					val, _ := cmd.Flags().GetInt(flagName)
					result[field.Name] = val
				case "boolean":
					val, _ := cmd.Flags().GetBool(flagName)
					result[field.Name] = val
				}
			}
			continue
		}

		if cmd.Flags().Changed(flagName) {
			// I14-F: when the flag was registered with a type
			// override (currently `apps/logs.since` → string for
			// duration-string acceptance), read it as the override
			// type and convert back to the manifest's wire-side
			// shape. Errors here surface as pre-network refusals
			// so the user sees an actionable message instead of
			// cobra's raw `strconv.ParseInt` output.
			if override := flagTypeOverride(cmdDef.Command, field.Name); override == "string" && field.Type == "integer" {
				raw, _ := cmd.Flags().GetString(flagName)
				secs, err := parseDurationOrInt(raw)
				if err != nil {
					return nil, fmt.Errorf("--%s: %w", flagName, err)
				}
				result[field.Name] = secs
				continue
			}
			switch field.Type {
			case "string":
				val, _ := cmd.Flags().GetString(flagName)
				result[field.Name] = val
			case "integer":
				val, _ := cmd.Flags().GetInt(flagName)
				result[field.Name] = val
			case "array":
				val, _ := cmd.Flags().GetStringSlice(flagName)
				if field.Format == "key_value" {
					result[field.Name] = parseKeyValueTags(val)
				} else {
					result[field.Name] = val
				}
			case "boolean":
				val, _ := cmd.Flags().GetBool(flagName)
				result[field.Name] = val
			}
		}
	}

	// Override boolean flags (from flags array, separate from fields)
	for _, flag := range cmdDef.Input.Flags {
		flagName := flagNameFor(flag.Name)
		if cmd.Flags().Changed(flagName) {
			val, _ := cmd.Flags().GetBool(flagName)
			result[flag.Name] = val
		}
	}

	// Convenience-positional mappings (I13-H): commands like
	// `tools/domain-check` accept their primary required string field
	// (`domain`) as a trailing positional too, even though the manifest
	// keeps the field non-positional. The leaf RunE has already widened
	// MaximumNArgs to allow these slots; here we deposit the value into
	// the body under the field's canonical name so the GET query-string
	// builder picks it up. An explicit `--domain` flag still wins (we
	// only fill when body is empty for the field).
	conveniencePos := conveniencePositionalFields(cmdDef)
	if len(conveniencePos) > 0 {
		start := 0
		for _, field := range cmdDef.Input.Fields {
			if field.Positional {
				start++
			}
		}
		for offset, fieldName := range conveniencePos {
			idx := start + offset
			if idx >= len(args) {
				break
			}
			if v, ok := result[fieldName].(string); ok && v != "" {
				continue
			}
			result[fieldName] = args[idx]
		}
	}

	return result, nil
}

func (e *Executor) buildEndpoint(endpoint string, args []string, cmdDef manifest.Command, cfg *config.Config, cid string, body map[string]any) (string, error) {
	result := endpoint

	// Substitute :aid with account ID. GetAccountID() prefers the
	// RUNOS_ACCOUNT_ID env var so headless CI runs without a config
	// file's account_id field.
	if strings.Contains(result, ":aid") {
		aid := cfg.GetAccountID()
		if aid == "" {
			return "", fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
		}
		result = strings.ReplaceAll(result, ":aid", url.PathEscape(aid))
	}

	// Substitute :cid with cluster ID
	if strings.Contains(result, ":cid") {
		if cid == "" {
			return "", fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
		}
		result = strings.ReplaceAll(result, ":cid", url.PathEscape(cid))
	}

	// Substitute field placeholders from body (flags) and positional args
	if cmdDef.Input != nil {
		argIndex := 0
		for _, field := range cmdDef.Input.Fields {
			var value string

			if field.Positional && argIndex < len(args) {
				// Get value from positional arg
				value = args[argIndex]
				argIndex++
			} else if val, ok := body[field.Name]; ok {
				// Get value from body (flag input)
				value = fmt.Sprintf("%v", val)
			}

			if value != "" {
				// Substitute in endpoint path (URL-encode for safety)
				escapedValue := url.PathEscape(value)
				result = strings.ReplaceAll(result, "{"+field.Name+"}", escapedValue)
				result = strings.ReplaceAll(result, ":"+field.Name, escapedValue)
			}
		}
	}

	// For GET and DELETE requests, append every input the user can set
	// (non-positional Fields + all Flags) to the query string. There's
	// no request body for either method, so the query string is the only
	// place these inputs can land. Symmetric across methods now;
	// pre-fix the GET branch dropped `Input.Flags` silently, which meant
	// `apps_logs --previous=true` arrived as `?tail=N` and the server's
	// `if (previous)` gate never tripped. Regression target: I7-F.
	if (cmdDef.Method == http.MethodGet || cmdDef.Method == http.MethodDelete) && cmdDef.Input != nil {
		queryParams := buildQueryParams(cmdDef, body)
		if len(queryParams) > 0 {
			result = result + "?" + queryParams.Encode()
		}
	}

	return e.baseURL + result, nil
}

// filterPathParamsFromBody removes fields that are used in the URL path from the request body,
// and nests flag values inside a "flags" object.
// Fields like "id" that appear as :id in the endpoint should not be sent in the body.
func filterPathParamsFromBody(body map[string]any, cmdDef manifest.Command) map[string]any {
	if cmdDef.Input == nil {
		return body
	}

	result := make(map[string]any)
	flagsObj := make(map[string]any)

	// Build a set of flag names for quick lookup
	flagNames := make(map[string]bool)
	for _, flag := range cmdDef.Input.Flags {
		flagNames[flag.Name] = true
	}

	for key, value := range body {
		// Skip if this field appears in the endpoint path as :fieldName or {fieldName}
		if strings.Contains(cmdDef.Endpoint, ":"+key) || strings.Contains(cmdDef.Endpoint, "{"+key+"}") {
			continue
		}
		// If it's a flag, add to flags object
		if flagNames[key] {
			flagsObj[key] = value
		} else {
			result[key] = value
		}
	}

	// Add flags object if there are any flags
	if len(flagsObj) > 0 {
		result["flags"] = flagsObj
	}

	return result
}

// unflattenBody converts dot-notation keys into nested objects.
// e.g., {"providerConfig.location": "hel1"} becomes {"providerConfig": {"location": "hel1"}}
func unflattenBody(body map[string]any) map[string]any {
	result := make(map[string]any)

	for key, value := range body {
		parts := strings.Split(key, ".")
		if len(parts) == 1 {
			// No dot notation, keep as-is
			result[key] = value
		} else {
			// Navigate/create nested structure
			current := result
			for _, part := range parts[:len(parts)-1] {
				if _, exists := current[part]; !exists {
					current[part] = make(map[string]any)
				}
				// Check if existing value is a map
				if nested, ok := current[part].(map[string]any); ok {
					current = nested
				} else {
					// Conflict: existing value is not a map, create new map
					newMap := make(map[string]any)
					current[part] = newMap
					current = newMap
				}
			}
			// Set the final value
			current[parts[len(parts)-1]] = value
		}
	}

	return result
}

func (e *Executor) doRequest(method, url string, body map[string]any, token string) (*http.Response, error) {
	var bodyReader io.Reader

	if len(body) > 0 && (method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch) {
		// Convert dot-notation keys to nested objects
		nestedBody := unflattenBody(body)
		jsonBody, err := json.Marshal(nestedBody)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return e.httpClient.Do(req)
}

// stdinYAMLBodyCap caps the bytes we'll slurp from stdin so a runaway
// pipe can't consume unbounded memory. 10 MiB is well above any
// reasonable manifest-driven body payload.
const stdinYAMLBodyCap = 10 << 20

// Cache stdin reads so the multiple call sites (PreRunE required-field
// gate via bodyFileProvidesField / bodyFilePresentsField, and the
// collectInput body merge) all see the same content. Without this, the
// first reader consumes os.Stdin and subsequent readers see an empty
// body. Regression target: I15-B.
var (
	stdinYAMLOnce sync.Once
	stdinYAMLData map[string]any
	stdinYAMLErr  error
)

// loadYAMLFile parses the YAML body at path. The conventional `-`
// path reads from stdin (kubectl-style), enabling `runos <cmd> -f - <<EOF`
// pipes in CI without an intermediate temp file. Repeated reads of `-`
// return cached content so PreRunE checks see the same body as RunE.
func loadYAMLFile(path string) (map[string]any, error) {
	if path == "-" {
		stdinYAMLOnce.Do(func() {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, stdinYAMLBodyCap+1))
			if err != nil {
				stdinYAMLErr = fmt.Errorf("read stdin: %w", err)
				return
			}
			if len(data) > stdinYAMLBodyCap {
				stdinYAMLErr = fmt.Errorf("stdin body exceeds %d-byte cap", stdinYAMLBodyCap)
				return
			}
			if err := yaml.Unmarshal(data, &stdinYAMLData); err != nil {
				stdinYAMLErr = fmt.Errorf("parse stdin as YAML: %w", err)
				return
			}
		})
		return stdinYAMLData, stdinYAMLErr
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// buildQueryParams assembles the query string for GET/DELETE requests
// from a manifest command's input definition + the resolved body map.
// Includes every non-positional Field plus every Flag whose value is
// present in body; positional Fields are skipped (they substitute into
// the URL path elsewhere).
//
// Regression target: I7-F. Pre-fix the GET branch in buildEndpoint
// only iterated Fields, silently dropping Flags. `apps_logs --previous`
// reached the conductor without the previous=true query param, so the
// server-side `if (previous)` gate never fired. The DELETE branch
// already did include Flags; the helper makes both methods symmetric.
func buildQueryParams(cmdDef manifest.Command, body map[string]any) url.Values {
	queryParams := url.Values{}
	if cmdDef.Input == nil {
		return queryParams
	}
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			continue
		}
		if val, ok := body[field.Name]; ok {
			queryParams.Set(field.Name, fmt.Sprintf("%v", val))
		}
	}
	for _, flag := range cmdDef.Input.Flags {
		if val, ok := body[flag.Name]; ok {
			queryParams.Set(flag.Name, fmt.Sprintf("%v", val))
		}
	}
	return queryParams
}

// podLogsSinceCapSeconds is the upper bound on `--since` for pod-logs
// commands. 90 days * 86400 s/day = 7,776,000 s — comfortably larger
// than any realistic pod log retention window, and small enough that
// downstream int math (k8s --since-seconds, conductor int parsing)
// never overflows. Pre-fix, callers passing 10-digit seconds values
// got bare 500s from k8s. Regression target: I19-G.
const podLogsSinceCapSeconds = 90 * 24 * 60 * 60

// podLogsSinceSeconds extracts the `since` body value for pod-logs
// commands, normalising across the int / int64 / float64 shapes that
// `collectInput` may deposit (the field is int-typed in the manifest
// but the `apps/logs` duration-string override lands as int via
// parseDurationOrInt). Returns (0, false) when absent so the caller
// can skip the cap check.
func podLogsSinceSeconds(body map[string]any) (int64, bool) {
	v, ok := body["since"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// validateInputValues refuses CLI inputs that would land an obviously-
// invalid request on the server: negative values for non-negative integer
// fields, empty strings for required string fields (positional or flag).
// Pre-fix the server caught these (`apps logs --tail -5` -> API 500;
// `apps logs --id ""` -> 404 with the literal `:id` placeholder in the
// error), but a client-side gate is faster, names the offending field,
// and avoids the network roundtrip.
//
// Negative-value rule: any manifest field declared `type: integer` is
// rejected when its body value is < 0. The conductor's integer fields
// (tail/since/replicas/cpu*/memory*/storageMb/...) are all non-negative
// by convention; if a future field legitimately accepts negative values,
// add an opt-out marker to the manifest.
//
// Empty-required rule: any manifest field declared `required: true` (or
// any positional field) is rejected when its resolved value is the empty
// string. Resolved value = the positional arg slot if supplied, else
// body[field.Name] (set by the flag).
//
// Regression targets: I9-A, I9-E (client half).
func validateInputValues(args []string, cmdDef manifest.Command, body map[string]any) error {
	if cmdDef.Input == nil {
		return nil
	}
	posIndex := 0
	for _, field := range cmdDef.Input.Fields {
		// Negative-integer rule.
		if field.Type == "integer" {
			if v, ok := body[field.Name]; ok {
				switch n := v.(type) {
				case int:
					if n < 0 {
						return fmt.Errorf("--%s must be non-negative, got %d", flagNameFor(field.Name), n)
					}
				case int64:
					if n < 0 {
						return fmt.Errorf("--%s must be non-negative, got %d", flagNameFor(field.Name), n)
					}
				case float64:
					if n < 0 {
						return fmt.Errorf("--%s must be non-negative, got %v", flagNameFor(field.Name), n)
					}
				}
			}
		}
		// Pod-logs `--since` upper-bound rule (I19-G). The conductor
		// passes the value to k8s `--since-seconds`; absurdly large
		// values (10-digit seconds = decades) trip an internal-server-
		// error rather than a clean 400. 90 days is comfortably more
		// than any realistic pod log retention window, so cap there and
		// emit an actionable refusal. Applies uniformly to apps/logs +
		// every services/<type>/{id}/logs via isPodLogsCommand (I17-F
		// predicate), so a future service-type logs reader inherits the
		// gate without code change.
		if isPodLogsCommand(cmdDef.Command) && field.Name == "since" {
			if since, ok := podLogsSinceSeconds(body); ok && since > podLogsSinceCapSeconds {
				return fmt.Errorf("--since %d seconds exceeds the %d-second (90-day) cap; pod log retention rotates well before that. Pass a smaller window or omit --since for the default", since, podLogsSinceCapSeconds)
			}
		}
		// Empty-required rule for positional fields. Resolve the
		// effective value: positional slot first, then body (flag).
		// Integer- and boolean-typed positional flags (I12-I — e.g.
		// `notify-keys update --id 47`) deposit a typed non-string value
		// into the body, so this check has to recognise both shapes
		// before deciding "empty". Pre-fix only the string branch fired,
		// which made `--id 47` fail the empty-required gate even though
		// the body already carried `id: 47`.
		//
		// I9-A mirror: AllowEmpty: true treats an explicitly-empty value
		// (positional `""` slot OR body[field.Name] = "") as a
		// meaningful user intent. The empty-required gate still fires
		// when the user supplies NEITHER the positional nor the flag
		// (true absence), only the "supplied-but-empty" refusal flips
		// off. Mirror of the non-positional carve-out below; same
		// I13-K-style AllowEmpty manifest marker controls both.
		if field.Positional {
			present := false
			explicitlyEmpty := false
			var resolved string
			if posIndex < len(args) {
				resolved = args[posIndex]
				if resolved != "" {
					present = true
				} else {
					explicitlyEmpty = true
				}
			} else if v, ok := body[field.Name]; ok {
				switch t := v.(type) {
				case string:
					resolved = t
					if t != "" {
						present = true
					} else {
						explicitlyEmpty = true
					}
				case int, int64, float64, bool:
					present = true
				}
			}
			posIndex++
			if field.AllowEmpty && explicitlyEmpty {
				present = true
			}
			if field.Required && !present && field.Name != "cid" {
				// `cid` is handled by the existing four-source
				// resolution path in Execute and has its own
				// "cluster ID required" error there; skip here.
				return fmt.Errorf("%s is required: pass as positional <%s> or --%s; got empty value", field.Name, field.Name, flagNameFor(field.Name))
			}
			_ = resolved
			continue
		}
		// Empty-required rule for non-positional required string fields.
		// `AllowEmpty: true` in the manifest opts a field out: empty
		// string is the desired value (e.g. `nodes/rename --name ""`
		// clears the node display name back to the bootstrap default).
		// The field is still required at the cobra/leaf-RunE layer (the
		// user has to type `--name ""`, not omit it), only the
		// empty-string refusal is skipped here. Regression target:
		// I13-K.
		if field.Required && field.Type == "string" && !field.AllowEmpty {
			if v, ok := body[field.Name]; ok {
				if s, isStr := v.(string); isStr && s == "" {
					return fmt.Errorf("--%s is required, got empty value", flagNameFor(field.Name))
				}
			}
		}
	}
	return nil
}

// parseDurationOrInt accepts either an integer-seconds string ("300")
// or a Go duration string ("5m", "1h30m", "90s") and returns the
// equivalent number of seconds. Used to widen `apps/logs --since`'s
// accepted shapes per I14-F: pre-fix `--since 5m` emitted cobra's
// raw `strconv.ParseInt: parsing "5m": invalid syntax`, which left
// `kubectl logs`-trained users with no path forward; the manifest's
// wire-side `since` is int-seconds, so the conversion happens here.
// Returns a typed error mentioning both accepted shapes so the
// up-stack `--<flag>:` prefix gives a complete diagnostic.
func parseDurationOrInt(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty value (expected integer seconds like 300, or a duration like 5m / 1h)")
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("must be non-negative, got %d", n)
		}
		return n, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q (expected integer seconds like 300, or a duration like 5m / 1h)", raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("must be non-negative, got %s", d)
	}
	return int(d.Seconds()), nil
}

// uidProvided reports whether the named field has been set by the user
// via EITHER its positional slot OR the explicit `--<flag>` form. Used
// by the `user/permissions` auto-fill (I12-C R2 / I12-D) to skip
// injecting the session firebase uid when the user already supplied a
// value. Pre-fix the auto-fill ran whenever body[name] was empty, but
// `body` doesn't carry positional args (those are URL-path-substituted
// by buildEndpoint), so the auto-fill clobbered the positional path
// with a flag value and tripped the I8-F ambiguity guard. Returns true
// when the value at the named field's positional index is non-empty,
// or when body[name] is a non-empty string.
func uidProvided(args []string, cmdDef manifest.Command, body map[string]any, name string) bool {
	if cmdDef.Input != nil {
		argIndex := 0
		for _, field := range cmdDef.Input.Fields {
			if !field.Positional {
				continue
			}
			if field.Name == name {
				if argIndex < len(args) && args[argIndex] != "" {
					return true
				}
				break
			}
			argIndex++
		}
	}
	if v, ok := body[name].(string); ok && v != "" {
		return true
	}
	return false
}

// validatePositionalFlagAgreement refuses commands where the same
// positional field receives different values via the positional slot
// and its `--<name>` flag form. Equal values are fine (redundant but
// unambiguous); a mismatch is always a typo and silently letting one
// slot win lands the request against the wrong resource. Returning a
// descriptive error here stops the command before dispatch.
//
// `body` is the already-collected input map (from collectInput); a
// flag value lives at body[field.Name] iff the flag was explicitly
// set (see collectInput's positional branch).
//
// Regression target: I8-F. Extends I7-C/D, which wired the positional
// slot through for `cid`; this closes the sibling "both slots filled,
// values disagree" case for every positional field across the dynacmd
// surface.
func validatePositionalFlagAgreement(args []string, cmdDef manifest.Command, body map[string]any) error {
	if cmdDef.Input == nil {
		return nil
	}
	argIndex := 0
	for _, field := range cmdDef.Input.Fields {
		if !field.Positional {
			continue
		}
		// Track positional slot for every positional field, even when
		// no positional arg was supplied at that slot.
		if argIndex >= len(args) {
			argIndex++
			continue
		}
		posVal := args[argIndex]
		argIndex++
		flagVal, ok := body[field.Name]
		if !ok {
			continue
		}
		flagStr, isStr := flagVal.(string)
		if !isStr || flagStr == "" {
			continue
		}
		if flagStr != posVal {
			return fmt.Errorf("ambiguous %s: positional %q and --%s=%q disagree; pass only one", field.Name, posVal, flagNameFor(field.Name), flagStr)
		}
	}
	return nil
}

// positionalArgForField returns the value passed at the positional slot
// matching the named manifest field, or "" if the field isn't declared
// positional or no arg was supplied at that slot. Positional slots are
// counted in declaration order among fields with `positional: true`.
//
// Used so a command whose manifest declares a positional `cid` (e.g.
// `clusters/show`) honours `runos clusters show <cid>` instead of
// silently ignoring the arg and falling through to the default cluster.
// Regression target: I7-C / I7-D.
func positionalArgForField(args []string, cmdDef manifest.Command, name string) string {
	if cmdDef.Input == nil {
		return ""
	}
	idx := 0
	for _, f := range cmdDef.Input.Fields {
		if !f.Positional {
			continue
		}
		if f.Name == name {
			if idx < len(args) {
				return args[idx]
			}
			return ""
		}
		idx++
	}
	return ""
}

// bodyFileProvidesField reports whether the YAML file at path parses
// successfully and contains a non-empty value for fieldName at the top
// level. Used by the pre-execution required-arg check so a `-f body.yaml`
// path that already carries an `id:` doesn't also need the positional or
// the `--id` flag.
//
// File-read or parse errors return false (the executor's own load step
// will surface the real diagnostic if the user actually relies on the
// file). Empty paths return false. Non-string scalars (numbers, bools)
// count as provided; only empty strings and nil are treated as absent.
func bodyFileProvidesField(path, fieldName string) bool {
	if path == "" || fieldName == "" {
		return false
	}
	body, err := loadYAMLFile(path)
	if err != nil {
		return false
	}
	val, ok := body[fieldName]
	if !ok || val == nil {
		return false
	}
	if s, ok := val.(string); ok && s == "" {
		return false
	}
	return true
}

// bodyFilePresentsField reports whether the named field appears in the
// `-f` body file with any value, including empty string. Used by the
// leaf-RunE missing-required gate to honour `AllowEmpty: true` fields
// (I13-K): `nodes rename --name ""` and `-f file.yaml` with `name: ""`
// both clear the display name back to bootstrap, so the gate has to
// recognise an explicit empty value as "supplied". The plain
// bodyFileProvidesField returns false on empty strings (because most
// callers want "real value"), so this is a sibling helper rather than
// a flag — keep the two surfaces distinct so a future caller doesn't
// accidentally weaken the default contract.
func bodyFilePresentsField(path, fieldName string) bool {
	if path == "" || fieldName == "" {
		return false
	}
	body, err := loadYAMLFile(path)
	if err != nil {
		return false
	}
	_, ok := body[fieldName]
	return ok
}

func parseKeyValueTags(tags []string) []map[string]string {
	result := make([]map[string]string, 0, len(tags))
	for _, tag := range tags {
		parts := strings.SplitN(tag, ":", 2)
		if len(parts) == 2 {
			result = append(result, map[string]string{
				"key":   parts[0],
				"value": parts[1],
			})
		} else {
			result = append(result, map[string]string{
				"key": parts[0],
			})
		}
	}
	return result
}

func markDefaultCluster(data []byte, defaultCID string) []byte {
	if defaultCID == "" {
		return data
	}

	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		return data
	}

	for _, item := range items {
		cid, ok := item["cid"].(string)
		if ok && cid == defaultCID {
			item["cid"] = cid + "*"
		}
	}

	result, err := json.Marshal(items)
	if err != nil {
		return data
	}
	return result
}

// isEmptyString reports whether v is a string-typed empty value. Used by
// the cli/version-check auto-injection so a manifest default of "" or a
// flag-default empty string still triggers the runtime fallback.
func isEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s == ""
}
