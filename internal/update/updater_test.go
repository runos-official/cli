package update

import (
	"runtime"
	"testing"
)

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		name string
		a    string // candidate version
		b    string // current version
		want bool
	}{
		// Basic comparisons
		{name: "patch bump is newer", a: "0.1.2", b: "0.1.1", want: true},
		{name: "minor bump is newer", a: "0.2.0", b: "0.1.9", want: true},
		{name: "major bump is newer", a: "1.0.0", b: "0.99.99", want: true},
		{name: "same version is not newer", a: "1.2.3", b: "1.2.3", want: false},
		{name: "older patch is not newer", a: "0.1.1", b: "0.1.2", want: false},
		{name: "older minor is not newer", a: "0.1.9", b: "0.2.0", want: false},
		{name: "older major is not newer", a: "0.99.99", b: "1.0.0", want: false},

		// v-prefix handling
		{name: "v-prefix on a only", a: "v1.2.4", b: "1.2.3", want: true},
		{name: "v-prefix on b only", a: "1.2.4", b: "v1.2.3", want: true},
		{name: "v-prefix on both", a: "v1.2.4", b: "v1.2.3", want: true},
		{name: "v-prefix same version", a: "v1.2.3", b: "v1.2.3", want: false},
		{name: "v-prefix older", a: "v1.2.2", b: "v1.2.3", want: false},

		// Pre-release suffix stripping
		{name: "pre-release suffix stripped from a", a: "1.2.4-beta", b: "1.2.3", want: true},
		{name: "pre-release suffix stripped from b", a: "1.2.4", b: "1.2.3-beta", want: true},
		{name: "pre-release suffix on both same base", a: "1.2.3-alpha", b: "1.2.3-beta", want: false},
		{name: "pre-release suffix a newer base", a: "1.2.4-rc1", b: "1.2.3-rc2", want: true},
		{name: "pre-release suffix a older base", a: "1.2.2-rc1", b: "1.2.3-rc2", want: false},
		{name: "v-prefix with pre-release", a: "v2.0.0-alpha", b: "v1.9.9-beta", want: true},
		{name: "complex pre-release suffix", a: "1.0.0-beta.1", b: "0.9.9", want: true},

		// Edge cases with zero versions
		{name: "zero to first patch", a: "0.0.1", b: "0.0.0", want: true},
		{name: "zero to first minor", a: "0.1.0", b: "0.0.0", want: true},
		{name: "zero to first major", a: "1.0.0", b: "0.0.0", want: true},
		{name: "both zero", a: "0.0.0", b: "0.0.0", want: false},

		// Large version numbers
		{name: "large patch numbers", a: "0.0.100", b: "0.0.99", want: true},
		{name: "large minor numbers", a: "0.100.0", b: "0.99.0", want: true},
		{name: "large major numbers", a: "100.0.0", b: "99.0.0", want: true},

		// Major takes priority over minor and patch
		{name: "higher major lower minor and patch", a: "2.0.0", b: "1.9.9", want: true},
		{name: "lower major higher minor and patch", a: "1.9.9", b: "2.0.0", want: false},

		// Minor takes priority over patch
		{name: "higher minor lower patch", a: "1.2.0", b: "1.1.9", want: true},
		{name: "lower minor higher patch", a: "1.1.9", b: "1.2.0", want: false},

		// Partial versions (missing components default to 0)
		{name: "major only vs full", a: "2", b: "1.9.9", want: true},
		{name: "major.minor only vs full", a: "1.3", b: "1.2.9", want: true},
		{name: "major only same as zero-padded", a: "1", b: "1.0.0", want: false},
		{name: "major.minor only same as zero-padded", a: "1.2", b: "1.2.0", want: false},

		// Empty and malformed input
		{name: "empty a and b", a: "", b: "", want: false},
		{name: "empty a", a: "", b: "1.0.0", want: false},
		{name: "empty b", a: "1.0.0", b: "", want: true},
		{name: "non-numeric defaults to zero", a: "abc", b: "0.0.0", want: false},
		{name: "non-numeric a vs valid b", a: "abc", b: "0.0.1", want: false},
		{name: "valid a vs non-numeric b", a: "0.0.1", b: "abc", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNewerVersion(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestGetPlatformInfo(t *testing.T) {
	osDir, osName, arch, ext := getPlatformInfo()

	// osName must match runtime.GOOS
	if osName != runtime.GOOS {
		t.Errorf("osName = %q, want %q", osName, runtime.GOOS)
	}

	// arch must match runtime.GOARCH
	if arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", arch, runtime.GOARCH)
	}

	// Validate osDir mapping
	expectedDirs := map[string]string{
		"darwin":  "mac",
		"linux":   "linux",
		"windows": "windows",
	}
	if expected, ok := expectedDirs[runtime.GOOS]; ok {
		if osDir != expected {
			t.Errorf("osDir = %q, want %q for GOOS=%q", osDir, expected, runtime.GOOS)
		}
	}

	// Validate extension based on OS
	switch runtime.GOOS {
	case "windows":
		if ext != "zip" {
			t.Errorf("ext = %q, want %q for windows", ext, "zip")
		}
	default:
		if ext != "tar.gz" {
			t.Errorf("ext = %q, want %q for %s", ext, "tar.gz", runtime.GOOS)
		}
	}

	// osDir should not be empty on supported platforms
	supportedPlatforms := map[string]bool{"darwin": true, "linux": true, "windows": true}
	if supportedPlatforms[runtime.GOOS] && osDir == "" {
		t.Errorf("osDir is empty for supported platform %q", runtime.GOOS)
	}

	// All returned values should be non-empty on supported platforms
	if supportedPlatforms[runtime.GOOS] {
		if osName == "" {
			t.Error("osName is empty")
		}
		if arch == "" {
			t.Error("arch is empty")
		}
		if ext == "" {
			t.Error("ext is empty")
		}
	}
}
