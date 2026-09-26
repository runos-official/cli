package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/runos-official/cli/internal/deploy"
)

// stampSynthesizedResources fills in the resource class + cpu/memory
// fields on deployConfig when the user has nothing set locally and the
// server has populated values (the resolveRRC synthesis path), then
// rewrites the local yaml so the manifest is self-describing.
//
// Never overwrites user-set values: the local yaml stays the source of
// truth for anything the user explicitly typed. Best-effort: any I/O
// failure just leaves the local yaml absent of these fields, which is
// the pre-fix status quo. Errors warn rather than propagate.
//
// Called only after the deploy is observed successful (--follow mode)
// or right after prepare/upload in fire-and-forget mode. The pre-deploy
// syncAppState path deliberately does NOT call this so a user who
// omits RRC on purpose has the prepare endpoint reapply that omission
// rather than silently round-tripping a synthesized value.
func stampSynthesizedResources(svc *deploy.Service, c *deploy.DeployConfig, configPath string, humanOut io.Writer) {
	if c == nil || c.ID == "" || hasResourceFields(c) {
		return
	}
	app, err := svc.GetApp(c.ID)
	if err != nil {
		// First-deploy fire-and-forget: the AppDocument hasn't
		// settled yet and a 404 here just means the synthesis hasn't
		// run. The user will see the synthesized class on their next
		// `apps_pull` once the orchestration finishes (I3-D).
		if !isAPINotFound(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch synthesized resource class: %v\n", err)
		}
		return
	}
	if !applySynthesizedResources(c, app) {
		return
	}
	if err := deploy.SaveConfig(configPath, c); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record synthesized resource class to local yaml: %v\n", err)
		return
	}
	fmt.Fprintf(humanOut, "Recorded synthesized resourceRequirementClassId=%q in %s\n",
		app.ResourceRequirementClassID, configPath)
}

// applySynthesizedResources copies the server's resolved class onto c in
// the shape apps pull writes (FCR 753): a named class records only its id,
// because the class carries every dimension and a pulled yaml omits them; "custom" records every cpu/memory value. Returns false,
// and changes nothing, when c already carries any resource field or the
// server has no class yet.
func applySynthesizedResources(c *deploy.DeployConfig, app *deploy.AppShow) bool {
	if hasResourceFields(c) || app == nil || app.ResourceRequirementClassID == "" {
		return false
	}
	c.ResourceRequirementClassID = app.ResourceRequirementClassID
	if app.ResourceRequirementClassID != "custom" {
		return true
	}
	c.CPURequestMc = app.CPURequestMc
	c.CPULimitMc = app.CPULimitMc
	c.MemoryRequestMb = app.MemoryRequestMb
	c.MemoryLimitMb = app.MemoryLimitMb
	return true
}

// hasResourceFields reports whether the yaml already sets a class or any
// cpu/memory value; the stamp never overwrites what the user typed.
func hasResourceFields(c *deploy.DeployConfig) bool {
	return c.ResourceRequirementClassID != "" ||
		c.CPURequestMc > 0 || c.CPULimitMc > 0 ||
		c.MemoryRequestMb > 0 || c.MemoryLimitMb > 0
}
