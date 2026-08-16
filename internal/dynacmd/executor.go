package dynacmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/term"

	"github.com/runos-official/cli/internal/apitimeout"
	"github.com/runos-official/cli/internal/apps"
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

// NewExecutor creates a new command executor.
//
// The http.Client carries NO Timeout: every dispatch sets its own
// deadline via apitimeout.For, because a client-wide 30 s cut killed
// synchronous endpoints conductor lets run for up to 600 s (goal 19 A4).
func NewExecutor(baseURL string) *Executor {
	return &Executor{
		baseURL:    baseURL,
		httpClient: &http.Client{},
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

// Error renders the failure as one line a person can act on. The formatting lives in
// apierror_message.go; see there for what survives and why.
func (e *APIError) Error() string {
	return describeAPIError(e.StatusCode, e.Body)
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

	// Refuse `--cid` values that break the path-segment shape (slash,
	// other punctuation, runaway length) before the request hits the
	// wire. Conductor 500s on these inputs instead of returning a clean
	// 400, which trips LLM/CI retries on what is really a typo. Empty
	// cid is the account-scoped case and is checked separately by
	// buildEndpoint when the endpoint declares `:cid`.
	if err := validateClusterIDShape(cid); err != nil {
		return wrapPreNetwork(err)
	}

	// Refuse a PATCH whose body carries nothing but path-param fields.
	// Sixteen-plus `services <type> update` commands have all-optional
	// body fields, so `services postgresql update <id>` with no flags
	// produces an empty PATCH body that conductor crashes on (500),
	// trips LLM/CI retries, and looks transient. The sibling
	// `apps update` already returns a clean 400; this gate matches that
	// shape pre-network for the remaining service types.
	if err := validatePatchHasBody(cmdDef, body); err != nil {
		return wrapPreNetwork(err)
	}

	// Refuse off-enum values for any manifest field that declares an
	// enum. Issue 60: `services <type> add --resource-requirement-class-id
	// fake.tier.x` got accepted, queued a job, and burned cluster-agent
	// cycles before the per-type writer caught the bad RRC. The manifest
	// already declares the valid set; reading it at runtime keeps the
	// CLI in sync with conductor whenever a new preset is added, so
	// there's no drift risk. Strings, integers, and string-array fields
	// all participate; positional and flag slots both honoured.
	if err := validateEnumValues(args, cmdDef, body); err != nil {
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
		// I25-H / I25-I: 401s get an actionable hint with revoked-vs-malformed
		// distinction when the conductor's body carries the structured signal.
		// foreman #145: skip the RUNOS_API_KEY remediation for endpoints that
		// proxy to an upstream provider (integrations/add/*), because a 401
		// there means the user's PROVIDER token is bad (Hetzner / DigitalOcean
		// / etc), not their RUNOS PAT. The conductor's APIError body already
		// carries the provider message; let it through verbatim.
		if !is401UpstreamProxyCommand(cmdDef) {
			if msg, ok := formatAuthError(err); ok {
				err = fmt.Errorf("%s", msg)
			}
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

	// I27-AE residual: raw `exec-sql --read-write` with destructive DDL
	// (DROP ROLE / DROP USER / CREATE ROLE / ALTER ROLE / etc.) bypasses
	// the per-verb cache-invalidation hooks conductor 17.13.0 added to
	// grant-database / revoke-database / create-user. The query goes
	// straight to the live database, succeeds, but conductor's stored
	// user/role list (read by `services_<db>_users`) still reflects
	// pre-DDL state until something else triggers a refresh. The CLI
	// can't fix the cache from here, but it can warn the user that
	// they're about to see a stale listing if they query right after.
	warnIfRawDDLBypassedCache(cmd, cmdDef, body)

	// Handle --follow flag for commands that return jobs (detected by jobId in output)
	if hasJobIdOutput(cmdDef) {
		follow, _ := cmd.Flags().GetBool("follow")
		// Only follow when the response actually carries a jobId. Some commands
		// are conditionally async: e.g. `nodes/delete` returns a jobId only with
		// --delete-cloud-instance (the removeServer job); the plain delete
		// completes synchronously and returns {success, nid} with no job. In that
		// case fall through to normal result rendering rather than erroring on a
		// missing jobId for an operation that actually succeeded.
		if follow && responseHasJobID(respBody) {
			// Render the response BEFORE polling: it carries the ids the
			// caller needs (vmid, sid, gid, imgid) and --follow used to
			// swallow it (A3 / B11).
			if err := renderFollowResponse(cmdDef, respBody, jsonOutput); err != nil {
				return err
			}
			return e.followJob(respBody, followProgressWriter(cmd, jsonOutput))
		}
	}

	// Tag the default cluster entry in both text and JSON modes so
	// `--json` consumers can identify the local default without a
	// second roundtrip. Text mode also appends `*` to the cid string
	// for the table renderer; JSON mode adds the structured
	// `isDefault: true` field only.
	if cmdDef.Command == "clusters/list" {
		respBody = annotateDefaultCluster(respBody, cfg.DefaultClusterID, !jsonOutput)
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

	// `clusters/kubeconfig` returns {kubeconfig: <YAML body>} so the
	// caller can pipe the result straight to `kubectl --kubeconfig=-`
	// or `> ~/.kube/config`. The generic key:value formatter renders
	// that as `kubeconfig: <multiline YAML>`, which prefixes the YAML
	// with a stray top-level key and breaks the documented use case.
	// In plain-text mode print the kubeconfig field's value verbatim;
	// `--json` keeps the structured envelope so scripts can `-j | jq`
	// or read other future fields. Regression target: foreman #48.
	if !jsonOutput {
		if body, ok := extractRawSingleStringOutput(cmdDef, respBody); ok {
			fmt.Fprint(cmd.OutOrStdout(), body)
			if !strings.HasSuffix(body, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
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

	// foreman #147: jobs cancel returns 200 OK with `cancelled:false`
	// when the job already finished (status:completed/failed/cancelled),
	// so a CI gate keyed on $? misreads the noop as success. Flip the
	// exit code while keeping the structured body intact.
	if cmdDef.Command == "jobs/cancel" {
		if err := jobsCancelExitGate(respBody); err != nil {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			return err
		}
	}

	return nil
}

// jobsCancelExitGate parses the jobs/cancel response and returns a
// non-nil error when `cancelled` is explicitly false (the server
// acknowledges the request but the job was already terminal). The
// formatter has already emitted the structured body to stdout (json
// or table); this error exists only to flip the process exit code so
// CI / LLM gates keyed on $? can distinguish "actually cancelled"
// from "tried to cancel but it was already done". Regression target:
// foreman #147.
func jobsCancelExitGate(respBody []byte) error {
	var resp struct {
		Cancelled *bool  `json:"cancelled"`
		Message   string `json:"message"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil
	}
	if resp.Cancelled == nil || *resp.Cancelled {
		return nil
	}
	if resp.Message != "" {
		return fmt.Errorf("jobs cancel: %s", resp.Message)
	}
	if resp.Status != "" {
		return fmt.Errorf("jobs cancel: job is in terminal status %q and was not cancelled", resp.Status)
	}
	return fmt.Errorf("jobs cancel: job was not cancelled")
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

// rawSingleStringOutputCommands enumerates commands whose plain-text
// rendering should emit the body of a single string-typed output
// field verbatim, instead of running it through the generic
// key:value formatter. Maps command -> the field name to print raw.
// Used by `clusters/kubeconfig` so the output is a directly-usable
// admin kubeconfig (pipeable to `kubectl --kubeconfig=-`) rather
// than `kubeconfig: <multiline YAML>`. Regression target: foreman #48.
var rawSingleStringOutputCommands = map[string]string{
	"clusters/kubeconfig": "kubeconfig",
}

// extractRawSingleStringOutput reports whether cmdDef opts into raw
// single-string text rendering and, if so, returns the body of the
// designated field. Returns ("", false) when the command is not in
// the carve-out list, the response isn't a JSON object, the field is
// absent, or the field's value isn't a non-empty string. Callers
// should fall through to the generic formatter in the false case so
// other commands keep their existing rendering.
func extractRawSingleStringOutput(cmdDef manifest.Command, respBody []byte) (string, bool) {
	fieldName, ok := rawSingleStringOutputCommands[cmdDef.Command]
	if !ok {
		return "", false
	}
	var obj map[string]any
	if err := json.Unmarshal(respBody, &obj); err != nil {
		return "", false
	}
	val, ok := obj[fieldName].(string)
	if !ok || val == "" {
		return "", false
	}
	return val, true
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

// coerceArrayFlagValue interprets the raw `[]string` collected from a
// repeatable --flag (pflag's StringArray) and returns either:
//
//   - a `[]any` of decoded objects/arrays when every element parses as
//     JSON (handles `--flag '[{...},{...}]'` as well as
//     `--flag '{"a":1}' --flag '{"b":2}'`),
//   - the original `[]string` when no element parses as JSON (handles
//     bare string lists like `--tags one --tags two`).
//
// Single-invocation JSON arrays — `--service-port-mappings
// '[{"port":3000,"standardHttps":true}]'` — get the array unwrapped so
// the wire body carries a real `[]object`, not a one-element array of
// strings. This is the I25-B regression target: pflag's StringSlice
// previously split the value on commas inside the JSON and rejected it
// with `parse error on line 1, column 2: bare " in non-quoted-field`.
// Mirrors the MCP executor's `coerceJSONString` so CLI and MCP behave
// identically when the user (or LLM) hands over an object/array
// payload as a string.
// refuseAmbiguousKeyValueArray surfaces a pre-network error when the
// user passes `--<flag> key=value[,key2=value2]` to a non-`key_value`
// array field. Such fields (canonical case: `--service-port-mappings`)
// expect structured objects on the wire; bare strings reach conductor
// as `["port=3000"]` and get rejected with `must be an object (got
// string)`. The k=v form was the I25-B workaround when CSV parsing
// broke JSON; that workaround is obsolete now that StringArray +
// coerceArrayFlagValue accept JSON natively. Steer users to the JSON
// shape before the wire-level refusal so the error names the field, the
// canonical form, and the `-f` file route.
//
// Two guards keep the heuristic off fields that legitimately carry `=`
// inside a plain string (goal 19 A1: every ECDSA and RSA public key ends
// in base64 `=` padding, so `vms create --ssh-keys` was refused for any
// key but ed25519):
//
//   - itemType `string` is never gated. The manifest declares the
//     element shape, and a string-element field cannot want objects.
//   - an element with whitespace anywhere before its first `=` is never
//     gated. `key=value` has none, an SSH key has a space after the
//     algorithm name, so the two shapes are distinguishable without
//     knowing the field.
//
// Detection otherwise: any element containing `=` that doesn't parse as
// JSON trips the gate.
func refuseAmbiguousKeyValueArray(flagName string, raw []string, itemType string) error {
	if itemType == "string" {
		return nil
	}
	for _, elem := range raw {
		if !looksLikeKeyValueElement(elem) {
			continue
		}
		var probe any
		if err := json.Unmarshal([]byte(elem), &probe); err == nil {
			continue
		}
		return fmt.Errorf("--%s: element %q looks like `key=value` shape, but this field expects structured objects. Pass JSON instead, e.g. --%s '{\"port\":3000,\"standardHttps\":true}' (single object) or --%s '[{...},{...}]' (array). Repeat the flag to add multiple elements, or put the whole body in a YAML file and pass -f <file>.", flagName, elem, flagName, flagName)
	}
	return nil
}

// looksLikeKeyValueElement reports whether elem reads as `key=value`:
// it carries an `=` and no whitespace before the first one. The
// whitespace rule is what separates a real k=v pair from a string that
// merely contains `=`, most acutely an SSH public key whose base64 body
// ends in `=` padding and always has a space after the algorithm name.
func looksLikeKeyValueElement(elem string) bool {
	eq := strings.IndexByte(elem, '=')
	if eq < 0 {
		return false
	}
	return strings.IndexFunc(elem[:eq], unicode.IsSpace) < 0
}

func coerceArrayFlagValue(raw []string, itemType string) any {
	if len(raw) == 0 {
		return raw
	}
	// foreman #83: string-element arrays must NEVER JSON-coerce. The
	// I25-B unwrap was for `object` / `array` element fields like
	// `--service-port-mappings`. For `--command` (manifest declares
	// `itemType: string`), JSON-coercing `true` / `false` / `0` /
	// `1234` previously produced booleans + numbers on the wire and
	// conductor 400'd with `command[0] must be a non-empty string
	// (got boolean)`. Skip the coercion when the element shape is
	// declared string; every argv element survives as a literal
	// string regardless of content.
	if itemType == "string" {
		return raw
	}
	// Single invocation that itself parses as a JSON array: unwrap it.
	if len(raw) == 1 {
		var arr []any
		if err := json.Unmarshal([]byte(raw[0]), &arr); err == nil {
			return arr
		}
	}
	// Element-wise: try parsing each as JSON. Mixed results (some JSON,
	// some bare strings) stay as `[]string` to preserve the caller's
	// intent rather than silently producing a heterogeneous list.
	decoded := make([]any, len(raw))
	for i, elem := range raw {
		var v any
		if err := json.Unmarshal([]byte(elem), &v); err != nil {
			return raw
		}
		decoded[i] = v
	}
	return decoded
}

// is401UpstreamProxyCommand reports whether cmdDef's endpoint proxies
// to an external provider API (so a 401 reflects the user's PROVIDER
// token, not their RUNOS PAT). Callers should skip the RUNOS_API_KEY
// auth-refused rendering so the upstream provider message reaches the
// user verbatim. Regression target: foreman #145.
func is401UpstreamProxyCommand(cmdDef manifest.Command) bool {
	return strings.HasPrefix(cmdDef.Command, "integrations/add/")
}

// formatAuthError checks whether err is an *APIError carrying a 401
// auth refusal from conductor and returns a friendly multi-line
// rendering plus true; (_, false) for any other error. I25-H / I25-I:
// pre-fix, every 401 surfaced as a bare `API error (401):
// {"error":"Invalid token"}` with no actionable hint.
//
// Conductor 14.9.0 added a `reason: 'revoked' | 'expired'` field plus
// the matching `revokedAt` / `expiredAt` timestamp when conductor has
// the data AND the bearer parses as a known PAT. The `error: "Invalid
// token"` string stays for backwards compatibility. We render the
// reason + timestamp distinctly so CI logs spell out "revoked at <ts>"
// vs "token doesn't parse" without the user having to dig.
func formatAuthError(err error) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusUnauthorized {
		return "", false
	}
	var body struct {
		Error     string `json:"error"`
		Reason    string `json:"reason"`
		RevokedAt string `json:"revokedAt"`
		ExpiredAt string `json:"expiredAt"`
	}
	_ = json.Unmarshal(apiErr.Body, &body)
	msg := body.Error
	if msg == "" {
		msg = "unauthorized"
	}
	var sb strings.Builder
	sb.WriteString("authentication refused (401): ")
	sb.WriteString(msg)
	switch body.Reason {
	case "revoked":
		sb.WriteString(" [revoked")
		if body.RevokedAt != "" {
			sb.WriteString(" at ")
			sb.WriteString(body.RevokedAt)
		}
		sb.WriteString("]")
	case "expired":
		sb.WriteString(" [expired")
		if body.ExpiredAt != "" {
			sb.WriteString(" at ")
			sb.WriteString(body.ExpiredAt)
		}
		sb.WriteString("]")
	case "":
		// no structured signal; nothing extra
	default:
		sb.WriteString(" [")
		sb.WriteString(body.Reason)
		sb.WriteString("]")
	}
	sb.WriteString("\n")
	sb.WriteString("Check:\n")
	sb.WriteString("  - is RUNOS_API_KEY pointing at a current PAT? `runos account api-keys list`\n")
	sb.WriteString("  - is RUNOS_API_URL pointing at the same environment the PAT was minted on?\n")
	sb.WriteString("  - was the PAT revoked or rotated? mint a new one via `runos account api-keys add`")
	return sb.String(), true
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
	// The deadline covers the response read as well, so the cancel stays
	// live until this function has drained the body (A4).
	ctx, cancel := context.WithTimeout(context.Background(), apitimeout.For(cmdDef, body))
	defer cancel()
	resp, err := e.doRequest(ctx, cmdDef.Method, endpoint, requestBody, token)
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

// unwrapArrayEnvelope mirrors output.unwrapArrayEnvelope: returns the
// inner array bytes when `data` is a single-key JSON object whose
// value is itself an array, else returns `data` unchanged. Duplicated
// here (rather than importing from internal/output) so internal/output
// doesn't pull internal/dynacmd as a transitive dependency in the
// reverse direction. I26-O / I26-U: apps_logs and the rest of the
// list-style endpoints moved to envelope responses
// (`{entries: [...]}`); the follow loop's direct json.Unmarshal into
// `[]map[string]any` now reads through this helper.
func unwrapArrayEnvelope(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return data
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return data
	}
	if len(probe) != 1 {
		return data
	}
	for _, inner := range probe {
		innerTrim := bytes.TrimSpace(inner)
		if len(innerTrim) > 0 && innerTrim[0] == '[' {
			return inner
		}
	}
	return data
}

// seedSeenSet records each log entry's dedup key in `seen` so the
// follow loop's first poll doesn't re-emit lines from the initial
// batch. Silently skips malformed bodies — at worst the first poll
// double-prints, which is recoverable.
func seedSeenSet(respBody []byte, seen map[string]struct{}) {
	respBody = unwrapArrayEnvelope(respBody)
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
	respBody = unwrapArrayEnvelope(respBody)
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

// responseHasJobID reports whether a response body carries a non-empty jobId, so
// --follow only engages on genuinely async responses. Some commands are
// conditionally async (e.g. nodes/delete returns a jobId only with
// --delete-cloud-instance) and otherwise return a synchronous result with no
// job; following those would fail on a missing jobId for a call that succeeded.
func responseHasJobID(respBody []byte) bool {
	var r map[string]any
	if err := json.Unmarshal(respBody, &r); err != nil {
		return false
	}
	id, ok := r["jobId"].(string)
	return ok && id != ""
}

// followProgressWriter picks where `--follow` progress lines go.
//
// Text mode: stdout. The progress IS the output of a followed command, so
// sending it to stderr made `runos vms create --follow > log` write an
// empty log and put the only useful text on the terminal (review 2 item
// 17). JSON mode: stderr, so stdout stays one parseable document for a
// caller piping into jq (goal 19 A3 / B11).
func followProgressWriter(cmd *cobra.Command, jsonOutput bool) io.Writer {
	if jsonOutput {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

// followJob polls the job named in respBody until it terminates,
// writing one progress line per state change to progress.
//
// progress is a parameter rather than os.Stdout because the destination
// depends on the output mode; see followProgressWriter.
func (e *Executor) followJob(respBody []byte, progress io.Writer) error {
	// Extract jobId from response
	var response map[string]any
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	jobID, ok := response["jobId"].(string)
	if !ok {
		return fmt.Errorf("response does not contain jobId")
	}

	return jobs.FollowJobToWriter(jobID, progress)
}

// renderFollowResponse prints the dispatch response before `--follow`
// starts polling.
//
// Pre-fix the follow branch returned straight into the poll loop, so the
// response body was never rendered at all. Every id an async create
// returns (vmid, sid, gid, imgid) was lost, and `--json` was silently
// ignored: the caller got progress text on stdout and nothing to parse.
// Regression target: goal 19 A3 / goal 21 B11.
func renderFollowResponse(cmdDef manifest.Command, respBody []byte, jsonOutput bool) error {
	return output.NewFormatter(jsonOutput).Format(respBody, cmdDef.Output)
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
		if err := refuseUnknownBodyFileKeys(filePath, fileData, cmdDef); err != nil {
			return nil, err
		}
		// Coerce each -f value against the matching manifest field's
		// declared type. The flag-set path below constructs strongly
		// typed values via cobra.Flags().GetString/GetInt/etc.; without
		// the same coercion here, a YAML `queue_size: 128` reaches the
		// wire as a number, which set-*-config's
		// `Record<string,string>` validator rejects with "expected a
		// string but got number". Regression target: foreman #40.
		fieldTypes := bodyFileFieldTypes(cmdDef)
		for k, v := range fileData {
			if t, ok := fieldTypes[k]; ok {
				v = coerceBodyFileValue(t, v)
			}
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
			// foreman #82: --app is a visible alias for --id on
			// apps-scoped commands (e.g. `apps run --app appid8`).
			// Registered in buildLeafCommand only when the command is
			// apps-scoped, so the Lookup gate keeps this branch dormant
			// elsewhere. Conflict (both --id and --app set with
			// different values) surfaces as a pre-network refusal here.
			if field.Name == "id" && cmd.Flags().Lookup("app") != nil && cmd.Flags().Changed("app") {
				appVal, _ := cmd.Flags().GetString("app")
				if cmd.Flags().Changed(flagName) {
					idVal, _ := cmd.Flags().GetString(flagName)
					if idVal != appVal {
						return nil, fmt.Errorf("--id and --app both set with different values (%q vs %q); pass one or the other", idVal, appVal)
					}
				}
				result[field.Name] = appVal
				continue
			}
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
				continue
			}
			// Issue 122: when the user passed the positional via args
			// (not the --<name> flag), bind it into the body so
			// body-bound positionals like apps/prepare-cli-pull's
			// cliUploadId actually reach conductor. URL-substituted
			// positionals are filtered out by filterPathParamsFromBody
			// before the wire send, so this writes are safe for them
			// too. positionalArgForField returns "" when the slot is
			// empty.
			if posVal := positionalArgForField(args, cmdDef, field.Name); posVal != "" {
				switch field.Type {
				case "string":
					result[field.Name] = posVal
				case "integer":
					if n, err := strconv.Atoi(posVal); err == nil {
						result[field.Name] = n
					}
				case "boolean":
					if b, err := strconv.ParseBool(posVal); err == nil {
						result[field.Name] = b
					}
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
				val, _ := cmd.Flags().GetStringArray(flagName)
				if field.Format == "key_value" {
					result[field.Name] = parseKeyValueTags(val)
				} else {
					if err := refuseAmbiguousKeyValueArray(flagName, val, field.ItemType); err != nil {
						return nil, err
					}
					result[field.Name] = coerceArrayFlagValue(val, field.ItemType)
				}
			case "object":
				val, _ := cmd.Flags().GetStringArray(flagName)
				obj, err := parseObjectFlagValues(flagName, val, field)
				if err != nil {
					return nil, err
				}
				if obj != nil {
					result[field.Name] = obj
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

	// I24-U: per-command extra body fields the conductor accepts but
	// the manifest doesn't advertise (currently `apps/add`'s
	// `provisionCiVariables`). When the user sets the corresponding
	// kebab flag, copy the value into the body under the camelCase
	// field name the conductor expects. Mirrors the generic
	// addFieldFlags + collectInput pair scoped to fields registered
	// via extraFieldsFor.
	for _, field := range extraFieldsFor(cmdDef.Command) {
		flagName := flagNameFor(field.Name)
		if !cmd.Flags().Changed(flagName) {
			continue
		}
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
				value = QueryParamValue(val)
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
		queryParams := QueryParams(cmdDef, body)
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

// doRequest issues one authenticated request under ctx's deadline. ctx
// must stay live until the caller has read the response body.
func (e *Executor) doRequest(ctx context.Context, method, url string, body map[string]any, token string) (*http.Response, error) {
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

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
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

// isStdinPath reports whether path is one of the conventional aliases
// for "read from stdin": the kubectl-style `-` sentinel, or the
// platform-specific filesystem links `/dev/stdin` / `/dev/fd/0` /
// `/proc/self/fd/0`. I27-S/X root cause: pre-fix the cache was keyed
// on `-` only, so a caller passing `-f /dev/stdin` (the obvious
// kubectl-equivalent literal path) bypassed it. The missing-required
// gate calls bodyFileProvidesField (loadYAMLFile internally) BEFORE
// collectInput does, and the first read drained the herestring/pipe,
// so collectInput's read got an empty file and the wire body sent
// `null`. Conductor (correctly) returned a structured 400 on a real
// empty body, but on a `null` body fell through to its internal-error
// path and returned a bare 500. Aliasing every stdin shape to the
// cached `-` branch closes the gap for every reasonable CI invocation.
func isStdinPath(path string) bool {
	switch path {
	case "-", "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0":
		return true
	}
	return false
}

// loadYAMLFile parses the YAML body at path. The conventional `-`
// path (and its filesystem aliases /dev/stdin, /dev/fd/0,
// /proc/self/fd/0) reads from stdin (kubectl-style), enabling
// `runos <cmd> -f - <<EOF` (or `-f /dev/stdin <<<`) pipes in CI
// without an intermediate temp file. Repeated reads of any stdin
// alias return cached content so PreRunE checks see the same body
// as RunE (I27-S/X regression).
func loadYAMLFile(path string) (map[string]any, error) {
	if isStdinPath(path) {
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
// validateEnumValues refuses any field whose effective value (positional
// arg or body[fieldName]) is outside the manifest's declared `enum`. The
// manifest is the authoritative source: the CLI reads the allow-list at
// runtime so a new preset added on the server side automatically becomes
// valid after `runos manifest update`. Issue 60.
//
// Positional slots and body fields are both checked. Empty values pass
// through (the missing-required gate elsewhere owns absence). Array
// fields validate each element. The error names the offending field,
// the offending value, and the valid options so the user can pick a
// replacement without re-running --help.
func validateEnumValues(args []string, cmdDef manifest.Command, body map[string]any) error {
	if cmdDef.Input == nil {
		return nil
	}
	posIndex := 0
	for _, field := range cmdDef.Input.Fields {
		var resolved any
		if field.Positional {
			if posIndex < len(args) {
				resolved = args[posIndex]
			}
			posIndex++
		}
		if resolved == nil || resolved == "" {
			if v, ok := body[field.Name]; ok {
				resolved = v
			}
		}
		if resolved == nil {
			continue
		}
		if len(field.Enum) == 0 {
			continue
		}
		if err := checkEnumValue(field, resolved); err != nil {
			return err
		}
	}
	return nil
}

// checkEnumValue validates a single field's value against its manifest
// enum. Splits the array case out so validateEnumValues can iterate
// without rebuilding the option list per element.
func checkEnumValue(field manifest.Field, value any) error {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return enumMember(field, v)
	case []string:
		for _, s := range v {
			if s == "" {
				continue
			}
			if err := enumMember(field, s); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, raw := range v {
			s, ok := raw.(string)
			if !ok {
				continue
			}
			if s == "" {
				continue
			}
			if err := enumMember(field, s); err != nil {
				return err
			}
		}
		return nil
	case int, int64, float64, bool:
		return enumMember(field, fmt.Sprintf("%v", v))
	default:
		return nil
	}
}

func enumMember(field manifest.Field, value string) error {
	if slices.Contains(field.Enum, value) {
		return nil
	}
	return fmt.Errorf("--%s %q is not a valid value (allowed: %s)", flagNameFor(field.Name), value, strings.Join(field.Enum, ", "))
}

// refuseUnknownBodyFileKeys refuses a -f body.yaml that carries top-level
// keys outside the command's manifest (input.fields + input.flags +
// extraFieldsFor). Closes issue 53: update endpoints silently dropped
// typo'd fields on disk, so a misspelled body key never reached the
// server. Matches the strict-yaml stance the deploy verb adopted in
// issue 50, but at the dynacmd file-load step so every -f-accepting
// command gates uniformly. Pure helper for test coverage; returns nil
// when the file is well-formed.
func refuseUnknownBodyFileKeys(filePath string, fileData map[string]any, cmdDef manifest.Command) error {
	if len(fileData) == 0 {
		return nil
	}
	known := map[string]struct{}{}
	if cmdDef.Input != nil {
		for _, f := range cmdDef.Input.Fields {
			known[f.Name] = struct{}{}
		}
		for _, f := range cmdDef.Input.Flags {
			known[f.Name] = struct{}{}
		}
	}
	for _, f := range extraFieldsFor(cmdDef.Command) {
		known[f.Name] = struct{}{}
	}
	var unknown []string
	for k := range fileData {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("body file %s carries fields not accepted by %s: %s (run with --help to see valid fields)", filePath, cmdDef.Command, strings.Join(unknown, ", "))
}

// bodyFileFieldTypes returns the declared manifest type for every body
// key cmdDef accepts (input.fields + input.flags as boolean +
// extraFieldsFor). Used by collectInput to coerce -f YAML values
// against the wire contract; see coerceBodyFileValue.
func bodyFileFieldTypes(cmdDef manifest.Command) map[string]string {
	out := map[string]string{}
	if cmdDef.Input != nil {
		for _, f := range cmdDef.Input.Fields {
			out[f.Name] = f.Type
		}
		for _, f := range cmdDef.Input.Flags {
			out[f.Name] = "boolean"
		}
	}
	for _, f := range extraFieldsFor(cmdDef.Command) {
		out[f.Name] = f.Type
	}
	return out
}

// coerceBodyFileValue normalises a value loaded from a -f YAML body
// against its declared manifest field type. The flag-set path in
// collectInput already constructs strongly-typed values via
// cobra.Flags().GetString/GetInt/etc.; this mirrors that contract for
// the -f path so a user's `queue_size: 128` reaches the wire as the
// string the set-*-config Record<string,string> validator requires.
// Unconvertible values pass through untouched so the server can return
// its own typed error. Regression target: foreman #40.
func coerceBodyFileValue(fieldType string, v any) any {
	if v == nil {
		return v
	}
	switch fieldType {
	case "string":
		switch x := v.(type) {
		case string:
			return x
		case bool:
			return strconv.FormatBool(x)
		case int:
			return strconv.Itoa(x)
		case int64:
			return strconv.FormatInt(x, 10)
		case float64:
			if x == float64(int64(x)) {
				return strconv.FormatInt(int64(x), 10)
			}
			return strconv.FormatFloat(x, 'f', -1, 64)
		default:
			return v
		}
	case "integer":
		switch x := v.(type) {
		case string:
			if n, err := strconv.Atoi(x); err == nil {
				return n
			}
			return v
		case float64:
			if x == float64(int64(x)) {
				return int(x)
			}
			return v
		default:
			return v
		}
	case "boolean":
		switch x := v.(type) {
		case string:
			if b, err := strconv.ParseBool(x); err == nil {
				return b
			}
			return v
		default:
			return v
		}
	default:
		return v
	}
}

// validatePatchHasBody refuses a PATCH whose body — after stripping the
// fields that buildEndpoint substitutes into the URL path — carries no
// fields. Many of conductor's services update handlers crash with a 500
// on an empty PATCH instead of returning the clean 400 that
// `apps update` does (issue 48). The pre-flight delivers the right
// shape locally and keeps the request off the wire. Non-PATCH methods
// pass through, so POST/PUT/DELETE/GET semantics are unchanged.
//
// Pure-trigger PATCH endpoints (services {kafka,ollama,vllm} restart)
// declare a PATCH with no non-positional fields in the manifest — they
// are deliberately empty-body triggers, not updates. The gate skips
// commands whose manifest declares zero body fields (issue 69
// regression of #48); the gate is meaningful only for PATCHes whose
// manifest declares at least one body field that the user is
// supposed to fill in.
func validatePatchHasBody(cmdDef manifest.Command, body map[string]any) error {
	if !strings.EqualFold(cmdDef.Method, "PATCH") {
		return nil
	}
	if !patchDeclaresBodyFields(cmdDef) {
		return nil
	}
	if len(filterPathParamsFromBody(body, cmdDef)) > 0 {
		return nil
	}
	return fmt.Errorf("%s requires at least one field flag (run with --help to see the available flags)", cmdDef.Command)
}

// patchDeclaresBodyFields reports whether a manifest command has at
// least one non-positional input field — i.e. whether it is actually a
// body-bearing PATCH that the user can fill in. Pure trigger PATCHes
// (no body fields declared, just a positional id path param) return
// false. Pure helper so the regression test can exercise it without a
// full Execute dance.
func patchDeclaresBodyFields(cmdDef manifest.Command) bool {
	if cmdDef.Input == nil {
		return false
	}
	for _, f := range cmdDef.Input.Fields {
		if !f.Positional {
			return true
		}
	}
	return len(cmdDef.Input.Flags) > 0
}

// cidMaxLength caps the cluster id at 64 runes. Conductor mints 3-5
// character ids today; 64 leaves plenty of headroom for any future
// scheme while still rejecting the runaway-cid case (issue 47) that
// crashes conductor with a 500 instead of a clean 400.
const cidMaxLength = 64

// validateClusterIDShape refuses a `--cid` (or default-cluster) value
// that breaks the path-segment shape before the request hits the wire.
// Issue 47: conductor returns 500 on a cid with a slash, control char,
// or absurd length, which LLM/CI consumers misread as a transient
// 5xx and retry. The pre-flight delivers a 4xx-style local refusal
// instead. Empty cid is fine here — buildEndpoint produces the
// "cluster ID required" error separately for endpoints that need one.
func validateClusterIDShape(cid string) error {
	if cid == "" {
		return nil
	}
	if len(cid) > cidMaxLength {
		return fmt.Errorf("cluster id is %d characters long (max %d): check --cid or `runos config get cid`", len(cid), cidMaxLength)
	}
	return apps.ValidateIdentifier("cluster id", cid)
}

// Regression targets: I9-A, I9-E (client half).
func validateInputValues(args []string, cmdDef manifest.Command, body map[string]any) error {
	if cmdDef.Input == nil {
		return nil
	}
	// Either-or rules the manifest schema cannot express (B12).
	if err := refuseUnlessExactlyOne(cmdDef, body); err != nil {
		return err
	}
	posIndex := 0
	for _, field := range cmdDef.Input.Fields {
		// A page size of zero asks for nothing. Pre-fix only negatives
		// were refused, so `--limit 0` reached conductor, where zero
		// means "no rows" on one endpoint and "the default page" on the
		// next; neither is what a caller who typed 0 asked for (B17).
		// Scoped to `limit` on purpose: zero is a meaningful value for
		// most other integer fields.
		if field.Type == "integer" && field.Name == "limit" {
			if n, ok := integerBodyValue(body, field.Name); ok && n == 0 {
				return fmt.Errorf("--%s must be at least 1; omit it for the endpoint's default page size", flagNameFor(field.Name))
			}
		}
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
		// I27-AB: refuse `--tail 0` on pod-logs verbs. Kubelet's `--tail=0`
		// is "use default" rather than "no entries", so a CLI caller asking
		// for zero entries gets exactly one entry back, which surprises
		// every reasonable consumer. The negative-int gate above already
		// catches < 0; this gate plugs the only remaining ambiguous slot.
		// Bound is pod-logs-only so non-logs integer fields (replicas: 0
		// for scale-to-zero, since: 0 meaning "from the start") are
		// untouched.
		if isPodLogsCommand(cmdDef.Command) && field.Name == "tail" {
			if v, ok := body[field.Name]; ok {
				zero := false
				switch n := v.(type) {
				case int:
					zero = n == 0
				case int64:
					zero = n == 0
				case float64:
					zero = n == 0
				}
				if zero {
					return fmt.Errorf("--tail 0 is ambiguous (kubelet treats it as 'use default', returning ~1 entry). Pass --tail >= 1 to limit the slice, or omit --tail for the default")
				}
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
				explicitlyEmpty = false
			}
			if field.Required {
				// Explicit "" positional is always a user error,
				// even for `cid` where Execute's four-source
				// fallback otherwise resolves the default cluster.
				// Pre-fix, `runos clusters show ""` silently
				// targeted the configured default; CI scripts
				// doing `runos clusters show "$CID"` with an
				// unset CID masqueraded as a successful query
				// against the wrong cluster. Absence (positional
				// slot omitted entirely) still falls through to
				// the default for `cid` to preserve the
				// `runos clusters show` shorthand.
				if explicitlyEmpty {
					return fmt.Errorf("%s is required: pass as positional <%s> or --%s; got empty value", field.Name, field.Name, flagNameFor(field.Name))
				}
				if !present && field.Name != "cid" {
					return fmt.Errorf("%s is required: pass as positional <%s> or --%s; got empty value", field.Name, field.Name, flagNameFor(field.Name))
				}
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
			// I27-AC: a positional value of literal `true`/`false` almost
			// always means the user wrote a boolean flag in space-form
			// (e.g. `--enabled false`). Cobra parses `--enabled` as
			// no-value (true by presence) and the next token lands in the
			// positional slot, which then collides with whatever the
			// caller actually intended for the positional. Append a
			// pointer at the equals-form so the user isn't chasing the
			// wrong end of the diagnostic.
			if (posVal == "true" || posVal == "false") && hasBoolFlag(cmdDef) {
				return fmt.Errorf("ambiguous %s: positional %q and --%s=%q disagree; pass only one. If you meant a boolean flag, use --<flag>=%s (equals-form); space-separated values land in the next positional slot", field.Name, posVal, flagNameFor(field.Name), flagStr, posVal)
			}
			return fmt.Errorf("ambiguous %s: positional %q and --%s=%q disagree; pass only one", field.Name, posVal, flagNameFor(field.Name), flagStr)
		}
	}
	return nil
}

// hasBoolFlag reports whether cmdDef declares any boolean Flag, which is
// the precondition for the I27-AC "did you mean --flag=value?" hint.
// Object/string fields don't trigger the hint because their space-form
// works fine; the surprise is specific to no-value cobra booleans.
func hasBoolFlag(cmdDef manifest.Command) bool {
	if cmdDef.Input == nil {
		return false
	}
	if len(cmdDef.Input.Flags) > 0 {
		return true
	}
	for _, f := range cmdDef.Input.Fields {
		if f.Type == "boolean" {
			return true
		}
	}
	return false
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

// validateBodyFilePath surfaces a clear, typed error when the user
// passed `-f <path>` but the file is missing, unreadable, or parses to
// invalid YAML. Pre-fix, bodyFileProvidesField silently treated every
// loadYAMLFile error as "field absent" and let the missing-required
// gate fire the misleading "missing required argument: <field>"
// diagnostic, masking a typoed path as a real missing-field problem.
// Now the gate calls this helper first so ENOENT / parse errors land
// in the user's lap with the offending path quoted.
func validateBodyFilePath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("-f file not found: %s", path)
		}
		return fmt.Errorf("-f file %q is unreadable: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("-f path %q is a directory; pass a YAML file", path)
	}
	if _, err := loadYAMLFile(path); err != nil {
		return fmt.Errorf("-f file %q: %w", path, err)
	}
	return nil
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

// annotateDefaultCluster tags the cluster entry whose cid matches the
// caller's local default with `isDefault: true` so JSON consumers can
// identify the default without a second roundtrip to
// `runos config get cid`. In text mode (appendAsterisk=true) the cid
// also picks up a trailing `*` for the table renderer and a footer
// note explains the marker, but the `isDefault` field is set in both
// modes so the JSON contract is uniform.
//
// Pre-fix the helper only appended `*` to cid and ran exclusively in
// text mode, leaving --json users blind to which cluster was the
// configured default.
func annotateDefaultCluster(data []byte, defaultCID string, appendAsterisk bool) []byte {
	if defaultCID == "" {
		return data
	}

	// `clusters/list` answers `{"clusters":[...]}`, not a bare array.
	// Pre-fix this unmarshalled straight into []map and returned on the
	// error, so the caller's own default was never marked in either mode
	// and the whole helper was dead against the live response (B4).
	envelopeKey, items, ok := clusterListItems(data)
	if !ok {
		return data
	}

	for _, item := range items {
		cid, ok := item["cid"].(string)
		if !ok || cid != defaultCID {
			continue
		}
		item["isDefault"] = true
		if appendAsterisk {
			item["cid"] = cid + "*"
		}
	}

	var (
		result []byte
		err    error
	)
	if envelopeKey == "" {
		result, err = json.Marshal(items)
	} else {
		result, err = json.Marshal(map[string]any{envelopeKey: items})
	}
	if err != nil {
		return data
	}
	return result
}

// clusterListItems decodes a clusters/list response into its rows,
// accepting both the bare array and the single-key envelope conductor
// actually returns. The envelope key is reported back so the annotated
// rows can be re-wrapped in the shape the formatter expects.
func clusterListItems(data []byte) (envelopeKey string, items []map[string]any, ok bool) {
	if err := json.Unmarshal(data, &items); err == nil {
		return "", items, true
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope) != 1 {
		return "", nil, false
	}
	for key, raw := range envelope {
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", nil, false
		}
		return key, items, true
	}
	return "", nil, false
}

// isEmptyString reports whether v is a string-typed empty value. Used by
// the cli/version-check auto-injection so a manifest default of "" or a
// flag-default empty string still triggers the runtime fallback.
func isEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s == ""
}

// rawDDLPattern matches the destructive USER / ROLE / DATABASE DDL
// statements that bypass conductor's per-verb cache-invalidation hooks
// (added in 17.13.0 to grant-database / revoke-database / create-user
// etc.). When a user runs raw `exec-sql --read-write` carrying one of
// these, the SQL hits the live database but conductor's stored user /
// role list (read by `services_<db>_users`) still reflects pre-DDL
// state until something else triggers a refresh. We don't try to be
// exhaustive — false positives are a minor stderr nag, false negatives
// drop the warning silently. Both are recoverable. Regex is permissive
// (any-leading-whitespace, case-insensitive, single-statement only).
var rawDDLPattern = regexp.MustCompile(`(?is)^\s*(?:--[^\n]*\n\s*)*(DROP|CREATE|ALTER)\s+(ROLE|USER|DATABASE)\b`)

// queryHasRawUserRoleDDL reports whether a SQL query carries the
// destructive USER / ROLE / DATABASE DDL that bypasses conductor's
// per-verb cache-invalidation hooks. Pure helper, unit-testable.
func queryHasRawUserRoleDDL(query string) bool {
	return rawDDLPattern.MatchString(query)
}

// warnIfRawDDLBypassedCache emits a stderr hint after a successful
// `services/<db>/{id}/exec-sql --read-write` whose query carries
// destructive USER / ROLE / DATABASE DDL. Conductor's cached user
// listing won't reflect the change until something else nudges the
// cache, so a follow-up `services_<db>_users` call may misleadingly
// still show the role/user as present.
//
// No-op unless the command path matches the exec-sql endpoint pattern
// AND the body's `readWrite` flag is true (read-only queries can't
// mutate state, so the warning would be noise). I27-AE residual.
func warnIfRawDDLBypassedCache(cmd *cobra.Command, cmdDef manifest.Command, body map[string]any) {
	if !strings.HasSuffix(cmdDef.Command, "/exec-sql") {
		return
	}
	rw, ok := body["readWrite"].(bool)
	if !ok || !rw {
		return
	}
	query, _ := body["query"].(string)
	if query == "" {
		return
	}
	if !queryHasRawUserRoleDDL(query) {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(),
		"Warning: raw role/user/database DDL bypasses conductor's user-list cache. "+
			"`services_<db>_users` may show stale state until a managed verb (grant-database, "+
			"revoke-database, create-user, etc.) is called and refreshes the listing.",
	)
}
