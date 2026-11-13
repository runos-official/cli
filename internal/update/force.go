package update

import (
	"fmt"
	"regexp"
	"strings"
)

// finalRelease is a version with no prerelease and no build metadata: the only kind --force installs.
var finalRelease = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// FinalRelease returns latest without a leading "v" and refuses anything that is not a final
// release (vX.Y.Z). `runos update --force` is explicit, so it must never reach a release candidate
// through a misconfigured latest version.
func FinalRelease(latest string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if !finalRelease.MatchString(v) {
		return "", fmt.Errorf("the latest published version %q is not a final release; --force installs only the latest final release", latest)
	}
	return v, nil
}

// ForceKind says what a forced update does to the running binary.
type ForceKind int

const (
	ForceUpgrade   ForceKind = iota // the release is newer than the running build
	ForceReinstall                  // the release is the running version
	ForceDowngrade                  // the running build is newer than the release (a release candidate, say)
	ForceDevBuild                   // the running binary is a local development build
)

// ForcePlan is what `runos update --force` is about to do, said before it downloads anything.
type ForcePlan struct {
	Running string
	Latest  string
	Kind    ForceKind
}

// PlanForce decides the kind from the running version and the latest final release.
func PlanForce(running, latest string) ForcePlan {
	plan := ForcePlan{Running: running, Latest: latest}
	switch {
	case running == "dev" || strings.HasPrefix(running, "dev-"):
		plan.Kind = ForceDevBuild
	case running == latest:
		plan.Kind = ForceReinstall
	case isNewerVersion(running, latest):
		plan.Kind = ForceDowngrade
	default:
		plan.Kind = ForceUpgrade
	}
	return plan
}

// Describe is the line printed before the download: exactly what is replaced and by what.
func (p ForcePlan) Describe() string {
	switch p.Kind {
	case ForceDevBuild:
		return fmt.Sprintf("Replacing the development build %s with the published release v%s", p.Running, p.Latest)
	case ForceReinstall:
		return fmt.Sprintf("Reinstalling the published release v%s over the same version", p.Latest)
	case ForceDowngrade:
		return fmt.Sprintf("Replacing %s with the published release v%s\nThis is a downgrade: the running build is newer than the latest published release.", p.Running, p.Latest)
	default:
		return fmt.Sprintf("Replacing %s with the published release v%s", p.Running, p.Latest)
	}
}
