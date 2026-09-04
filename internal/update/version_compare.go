package update

import (
	"fmt"
	"strings"
)

// IsNewerVersion reports whether a is newer than b. Exported because the DESKTOP update path needs
// the same rule: it treated any difference as needing an install, which downgraded a build that was
// AHEAD of the latest release. One comparison, so the two can never disagree again.
func IsNewerVersion(a, b string) bool { return isNewerVersion(a, b) }

// isNewerVersion returns true if version a is newer than version b.
//
// This is the "is an update available" rule, and it is SemVer 2.0.0
// precedence: build metadata is dropped (section 10), then the numeric core
// decides, and the pre-release identifiers break a tie on an equal core
// (section 11). A pre-release sorts BELOW its own final, so 1.20.0 is newer
// than 1.20.0-rc.2 and 1.20.0-rc.2 is newer than 1.20.0-rc.1.
//
// Do not confuse this with the CLI MANIFEST version rule. There, a version
// such as 45.3.0+virt carries the enabled module set in its build metadata and
// is compared as an OPAQUE STRING, because a changed module set must read as
// different. Here the same +virt is discarded, because it carries no
// precedence. The two rules live apart on purpose.
//
// The numeric core keeps this repository's long-standing lenient parse rather
// than a strict SemVer one: a missing component is zero ("1.2" equals
// "1.2.0"), and anything that is not a number is zero ("abc" and "" are both
// 0.0.0). The dev-build guard in IsDevBuild depends on that last property.
func isNewerVersion(a, b string) bool {
	aCore, aPre := splitVersion(a)
	bCore, bPre := splitVersion(b)

	aMajor, aMinor, aPatch := parseCore(aCore)
	bMajor, bMinor, bPatch := parseCore(bCore)

	if aMajor != bMajor {
		return aMajor > bMajor
	}
	if aMinor != bMinor {
		return aMinor > bMinor
	}
	if aPatch != bPatch {
		return aPatch > bPatch
	}
	return comparePreRelease(aPre, bPre) > 0
}

// trimBuildMetadata cuts everything from the first '+'. SemVer 2.0.0
// section 10: build metadata is ignored when determining precedence.
func trimBuildMetadata(v string) string {
	if idx := strings.IndexByte(v, '+'); idx >= 0 {
		return v[:idx]
	}
	return v
}

// splitVersion returns the numeric core and the pre-release string, with a
// leading "v" and any build metadata already removed. A version with no
// pre-release returns an empty second value.
func splitVersion(v string) (core, pre string) {
	v = trimBuildMetadata(strings.TrimPrefix(v, "v"))
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

// parseCore reads major.minor.patch leniently: a missing or non-numeric
// component reads as zero.
func parseCore(v string) (major, minor, patch int) {
	parts := strings.Split(v, ".")
	if len(parts) >= 1 {
		fmt.Sscanf(parts[0], "%d", &major)
	}
	if len(parts) >= 2 {
		fmt.Sscanf(parts[1], "%d", &minor)
	}
	if len(parts) >= 3 {
		fmt.Sscanf(parts[2], "%d", &patch)
	}
	return major, minor, patch
}

// comparePreRelease applies SemVer 2.0.0 section 11 to two pre-release
// strings and returns -1, 0 or 1. The callers only ever reach it with equal
// numeric cores.
//
// The rule, in the order it is applied:
//   - a version with no pre-release outranks the same core with one;
//   - identifiers compare left to right, dot-separated;
//   - two all-numeric identifiers compare numerically;
//   - an all-numeric identifier ranks BELOW an alphanumeric one;
//   - when every shared identifier is equal, the longer list wins.
func comparePreRelease(a, b string) int {
	if a == b {
		return 0
	}
	// An empty pre-release means a final release, which outranks any
	// pre-release of the same core.
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}

	aIDs := strings.Split(a, ".")
	bIDs := strings.Split(b, ".")
	for i := 0; i < len(aIDs) && i < len(bIDs); i++ {
		if c := comparePreReleaseIdentifier(aIDs[i], bIDs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(aIDs) > len(bIDs):
		return 1
	case len(aIDs) < len(bIDs):
		return -1
	default:
		return 0
	}
}

func comparePreReleaseIdentifier(a, b string) int {
	aNum, bNum := isNumericIdentifier(a), isNumericIdentifier(b)
	switch {
	case aNum && bNum:
		return compareNumericIdentifier(a, b)
	case aNum:
		// Numeric ranks below alphanumeric.
		return -1
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func isNumericIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// compareNumericIdentifier compares two all-digit identifiers numerically
// without converting them, so an absurdly long identifier cannot overflow.
// Leading zeros are tolerated even though SemVer forbids them.
func compareNumericIdentifier(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}
