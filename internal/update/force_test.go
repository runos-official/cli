package update

import (
	"strings"
	"testing"
)

// `runos update --force` installs the latest FINAL release only. A prerelease, build metadata or
// anything else that is not vX.Y.Z is refused before anything is downloaded.
func TestForcedUpdateAcceptsOnlyAFinalRelease(t *testing.T) {
	for _, tc := range []struct {
		latest  string
		want    string
		wantErr bool
	}{
		{latest: "1.24.2", want: "1.24.2"},
		{latest: "v1.24.2", want: "1.24.2"},
		{latest: "1.25.0-rc.2", wantErr: true},
		{latest: "1.24.2+virt", wantErr: true},
		{latest: "1.24", wantErr: true},
		{latest: "", wantErr: true},
		{latest: "latest", wantErr: true},
	} {
		got, err := FinalRelease(tc.latest)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("FinalRelease(%q) = %q, %v; want %q, error=%v", tc.latest, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestPlanForceSaysWhatIsReplacedAndByWhat(t *testing.T) {
	for _, tc := range []struct {
		name, running, latest string
		kind                  ForceKind
		says                  []string
	}{
		{"a development build", "dev-2026-11-13T09:00:00Z", "1.24.2", ForceDevBuild,
			[]string{"Replacing the development build dev-2026-11-13T09:00:00Z with the published release v1.24.2"}},
		{"the bare dev label", "dev", "1.24.2", ForceDevBuild,
			[]string{"Replacing the development build dev with the published release v1.24.2"}},
		{"an rc newer than the release", "1.25.0-rc.2", "1.24.2", ForceDowngrade,
			[]string{"Replacing 1.25.0-rc.2 with the published release v1.24.2", "downgrade"}},
		{"the same version", "1.24.2", "1.24.2", ForceReinstall,
			[]string{"Reinstalling the published release v1.24.2"}},
		{"an older release", "1.23.0", "1.24.2", ForceUpgrade,
			[]string{"Replacing 1.23.0 with the published release v1.24.2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanForce(tc.running, tc.latest)
			if plan.Kind != tc.kind {
				t.Fatalf("kind = %v, want %v", plan.Kind, tc.kind)
			}
			for _, want := range tc.says {
				if !strings.Contains(plan.Describe(), want) {
					t.Errorf("Describe() = %q, want it to contain %q", plan.Describe(), want)
				}
			}
		})
	}
}
