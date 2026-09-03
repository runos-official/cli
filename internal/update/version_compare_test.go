package update

import "testing"

// TestIsNewerVersionPreReleasePrecedence covers SemVer 2.0.0 section 11, the
// rule for deciding whether an update is available. It is deliberately kept
// apart from TestIsNewerVersion in updater_test.go: that table asserts the
// lenient numeric parse this repository has always used, and it must keep
// passing unmodified.
//
// The gap this test closes was measured on dev: the shipped 1.20.0-rc.1
// artefact answered "The CLI is already up to date." while dev advertised
// 1.20.0-rc.2, because the comparison truncated at the first '-' and threw the
// pre-release identifiers away. Advertising a release candidate therefore
// reached nobody already on an earlier candidate of the same train.
func TestIsNewerVersionPreReleasePrecedence(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		// The seven cases the story's acceptance criterion names.
		{name: "later rc beats earlier rc", a: "1.20.0-rc.2", b: "1.20.0-rc.1", want: true},
		{name: "final beats its own rc", a: "1.20.0", b: "1.20.0-rc.2", want: true},
		{name: "rc never beats its own final", a: "1.20.0-rc.2", b: "1.20.0", want: false},
		{name: "numeric identifiers compare numerically not lexically", a: "1.20.0-rc.10", b: "1.20.0-rc.2", want: true},
		{name: "identical rc is not an update", a: "1.20.0-rc.1", b: "1.20.0-rc.1", want: false},
		{name: "higher core beats a lower core's rc", a: "1.21.0", b: "1.20.0-rc.1", want: true},
		{name: "an rc never beats a higher core", a: "1.20.0-rc.1", b: "1.21.0", want: false},

		// The rule needs these too, and the criterion does not name them.
		{name: "alphanumeric identifier outranks a numeric one", a: "1.0.0-alpha", b: "1.0.0-1", want: true},
		{name: "the longer identifier list wins when the prefix is equal", a: "1.0.0-rc.1.1", b: "1.0.0-rc.1", want: true},
		{name: "build metadata carries no precedence", a: "1.20.0+virt", b: "1.20.0", want: false},
		{name: "build metadata does not mask a real pre-release difference", a: "1.20.0-rc.2+build", b: "1.20.0-rc.1", want: true},
		{name: "a dev stamp is never newer than a release", a: "dev-20260903T120000Z", b: "1.20.0-rc.3", want: false},

		// Ordering is antisymmetric: the reverse of a true case is false.
		{name: "earlier rc is not newer than later rc", a: "1.20.0-rc.1", b: "1.20.0-rc.2", want: false},
		{name: "numeric identifier does not outrank an alphanumeric one", a: "1.0.0-1", b: "1.0.0-alpha", want: false},
		{name: "the shorter identifier list loses", a: "1.0.0-rc.1", b: "1.0.0-rc.1.1", want: false},
		{name: "build metadata alone is not an update in either direction", a: "1.20.0", b: "1.20.0+virt", want: false},

		// A leading v is still stripped on both sides.
		{name: "leading v is ignored on a pre-release", a: "v1.20.0-rc.3", b: "1.20.0-rc.2", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNewerVersion(tt.a, tt.b); got != tt.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			// IsNewerVersion is the exported name the desktop path calls.
			// One rule serves every caller, so the two can never disagree.
			if got := IsNewerVersion(tt.a, tt.b); got != tt.want {
				t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
