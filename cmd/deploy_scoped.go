package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/jobs"
)

// validateScopedDeployTarget applies story 346 S1/S2 locally, before
// any request to the server: a scoped-mode deploy must already be
// pinned to an existing app with an explicit deployType, and every
// requires.<alias> entry must already carry an id, because the
// app-scoped preparation route can neither create a new app nor
// create or link a service inline (handbook 992). Checking this here,
// right after the yaml loads and before the deployType-inference
// GetApp call and the FindAppByName lookup, is what keeps both of
// those reads from ever firing in scoped mode: an empty ID or a
// requires entry with no id refuses here first, with zero requests.
func validateScopedDeployTarget(deployConfig *deploy.DeployConfig, configPath string) error {
	if deployConfig.ID == "" {
		return fmt.Errorf("scoped mode requires a pinned app: add `id: <app-id>` to %s (use --app for a VCS deploy with no local yaml), since an app-scoped deploy grant cannot create a new app", configPath)
	}
	if deployConfig.DeployType == "" {
		return fmt.Errorf("scoped mode requires an explicit deployType in %s: add `deployType: cli` or `deployType: vcs` (scoped mode never reads app detail to infer it)", configPath)
	}
	for alias, req := range deployConfig.Requires {
		if req.ID == "" {
			return fmt.Errorf("scoped mode requires every requires.<alias> entry to already have an id; %q has none. An app-scoped deploy grant cannot create or link a service: create or link it with a credential that holds broader authority, then add its id to %s", alias, configPath)
		}
	}
	return nil
}

// followDeployJob is the story 346 S4 (scoped) / default mode
// (unchanged from before this story) follow dispatch shared by
// CLI-upload --follow (cmd/deploy.go) and VCS deploy --follow
// (cmd/deploy_vcs.go). The primitive is chosen by the declared mode,
// never probed for: scoped mode reads only GET
// /jobs/:jobId/deployment-progress, with every refusal final, first
// read and later poll alike, never a retry through the general
// job/work-item/log follow. Default mode is exactly
// jobs.FollowJobToWriter's pre-story behavior.
func followDeployJob(ctx context.Context, svc *jobs.Service, jobID string, w io.Writer, scopedMode bool) error {
	if scopedMode {
		_, err := jobs.FollowDeploymentProgressToWriterResult(ctx, svc, jobID, w)
		return err
	}
	return jobs.FollowJobWithServiceToWriter(ctx, svc, jobID, w)
}

// hintScopedModeOn401Or403 appends one line to err, when it carries a
// 401 or 403 status, telling the caller that an app-scoped Account
// API Key must run with --scoped or RUNOS_SCOPED=1. Story 346
// acceptance criterion 6, DEFAULT MODE's one required addition
// (finding 1435): the deploy that needs the declaration is exactly
// the one that fails with 401/403 without it, so the refusal itself
// is where the caller discovers the flag. Default-mode call sites
// only (the broad preparation route and the three preflight reads);
// one line, no retry, no mode switch -- scoped mode has already
// declared itself, so there is nothing left to discover.
func hintScopedModeOn401Or403(err error) error {
	status, ok := apiErrorStatusCode(err)
	if !ok || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return err
	}
	return fmt.Errorf("%w (if this credential is a scoped Account API Key, re-run with --scoped or set RUNOS_SCOPED=1)", err)
}

// apiErrorStatusCode extracts the HTTP status code from either of
// this repo's typed API-error shapes that a default-mode deploy gate
// can return: deploy.APIError (the deploy package's own reads and the
// preparation routes) and apps.APIError (the drift/domain gates,
// which call through internal/apps).
func apiErrorStatusCode(err error) (int, bool) {
	var deployErr *deploy.APIError
	if errors.As(err, &deployErr) {
		return deployErr.StatusCode, true
	}
	var appsErr *apps.APIError
	if errors.As(err, &appsErr) {
		return appsErr.StatusCode, true
	}
	return 0, false
}
