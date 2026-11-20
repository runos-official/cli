package dynacmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/roidapi"
)

// stepUpEligibleCommands names the exact typed credential-lifecycle
// commands Conductor documents as needing a request-bound step-up
// (review finding 1194, story 347 criterion 6, S1): add and rotate for
// both typed families, and update for Personal Access Tokens. A
// Personal Access Token update only actually needs step-up when THIS
// particular change widens restrictions, which only Roid can decide
// from the token's current stored restrictions -- the client cannot
// tell in advance, so it sends the ordinary request first and reacts
// to the server's own step-up-required refusal, exactly like every
// other command here. Scoped to this exact list (not every mutation)
// so the retry path stays auditable: an unlisted command's 403, even
// one shaped like a step-up challenge, is never retried.
var stepUpEligibleCommands = map[string]bool{
	"account/personal-access-tokens/add":    true,
	"account/personal-access-tokens/rotate": true,
	"account/personal-access-tokens/update": true,
	"account/account-api-keys/add":          true,
	"account/account-api-keys/rotate":       true,
}

// completeStepUpIfRequired reacts to a step-up-required refusal from
// one of stepUpEligibleCommands by completing the EXISTING human
// confirmation flow (internal/roidapi/client.go, stepup.go) and
// retrying the IDENTICAL request once with the confirmed step-up id
// added as X-RunOS-Step-Up (story 347 S2: same body, same
// Idempotency-Key/If-Match headers -- requestBody and headers are the
// exact values the first attempt sent, not rebuilt). It does not write
// a second step-up implementation: detection (roidapi.Refusal) and
// waiting (Client.WaitForStepUp) are the exact functions
// internal/roidapi's own static access commands use.
//
// Returns handled=false when cmdDef is not step-up-eligible or the
// refusal is not the step-up shape. That covers story 347 S3: a
// machine credential's request to one of these human-only commands
// gets the server's plain human-only refusal, never the step-up shape
// (the manifest documents every one of these routes as human-only), so
// this function never attempts the human challenge for it -- there is
// nothing here that simulates a human; it only reacts to what the
// server actually sent.
func (e *Executor) completeStepUpIfRequired(
	ctx context.Context,
	cmdDef manifest.Command,
	endpoint string,
	requestBody map[string]any,
	headers http.Header,
	token string,
	options requestOptions,
	cfg *config.Config,
	apiErr *APIError,
) (respBody []byte, respHeaders http.Header, err error, handled bool) {
	if !stepUpEligibleCommands[cmdDef.Command] {
		return nil, nil, nil, false
	}
	refusal := roidapi.Refusal(&api.Result{StatusCode: apiErr.StatusCode, Body: apiErr.Body})
	su, ok := refusal.(*roidapi.StepUpRequiredError)
	if !ok || su.URL == "" {
		return nil, nil, nil, false
	}
	router, rerr := roidapi.RouterFor(cfg)
	if rerr != nil {
		return nil, nil, apiErr, true
	}
	client := roidapi.NewRouted(router, func() (string, error) { return token, nil })
	printStepUpPrompt(os.Stderr, su)
	if werr := client.WaitForStepUp(su.ID, roidapi.DefaultStepUpWait()); werr != nil {
		return nil, nil, werr, true
	}

	retryHeaders := headers.Clone()
	if retryHeaders == nil {
		retryHeaders = http.Header{}
	}
	retryHeaders.Set("X-RunOS-Step-Up", su.ID)
	resp, derr := e.doRequest(ctx, cmdDef.Method, endpoint, requestBody, retryHeaders, token, options)
	if derr != nil {
		return nil, nil, fmt.Errorf("request failed: %w", derr), true
	}
	defer resp.Body.Close()
	body, rerr2 := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if rerr2 != nil {
		return nil, nil, fmt.Errorf("failed to read response: %w", rerr2), true
	}
	if resp.StatusCode >= 400 {
		return nil, resp.Header, &APIError{StatusCode: resp.StatusCode, Body: body}, true
	}
	return body, resp.Header, nil, true
}

// printStepUpPrompt mirrors cmd/access.go's stepUpPrompt (same wording),
// without opening a browser: dynacmd cannot import cmd's openBrowser
// helper without an import cycle, and the security contract never
// depended on the browser auto-opening -- the URL is enough.
func printStepUpPrompt(w io.Writer, su *roidapi.StepUpRequiredError) {
	fmt.Fprintf(w, "This change needs a confirmation with your security key.\nOpen %s to confirm it. Waiting...\n", su.URL)
}
