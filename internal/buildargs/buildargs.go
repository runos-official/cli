// Package buildargs parses and validates the `--build-arg KEY=VALUE`
// flag values for `runos deploy`. The helpers are kept pure so they can
// be unit-tested without a cobra command tree, and so the same parse +
// validate pipeline runs for both the CLI-deploy and VCS-deploy paths.
//
// Wire contract (locked at objective 40 stitch time, shared with the
// conductor and cluster-agent stories):
//   - The CLI never merges the yaml `buildArgs:` map and the CLI flag
//     list. The CLI sends the CLI list verbatim under a SEPARATE wire
//     field `buildArgsCli` while the yaml map ships under the existing
//     `buildArgs` field on the deploy request body. Conductor owns
//     precedence (cli > yaml) and forwards the merged result.
//   - Keys must match Docker ARG name rules: `^[A-Za-z_][A-Za-z0-9_]*$`.
//     Both the CLI and the conductor enforce this; refusing client-side
//     keeps the error close to the user's edit.
//   - Repeating the same key within the CLI flag set is a hard error
//     (no implicit last-wins). A CLI key that also appears in the yaml
//     `buildArgs:` map is NOT a conflict; conductor lets the CLI value
//     override the yaml value.
package buildargs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/deploy"
)

// argNameRe matches Docker's ARG name rules: letters, digits and
// underscores, not starting with a digit. Compiled once at package
// init; the helpers below are safe to call concurrently.
var argNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsValidArgName reports whether key matches the Docker ARG name regex.
// Exported so the conductor-side error messages can be mirrored in CLI
// tests if needed; the package's own callers go through Parse.
func IsValidArgName(key string) bool {
	return argNameRe.MatchString(key)
}

// Parse turns the raw `--build-arg` flag values (as collected by
// cobra's StringArrayVar) into the structured list the CLI ships on
// the deploy request body. Returns the entries in CLI invocation order
// so the conductor's merge step receives a deterministic input.
//
// Validation (in order, first failure wins so the error message names
// the specific offending entry):
//
//  1. Each entry must contain at least one `=`. Bare `KEY` is rejected;
//     `KEY=` is allowed (empty value is the docker build semantics for
//     "set ARG to the empty string", distinct from "use the Dockerfile
//     default").
//  2. The key (everything before the first `=`) must be non-empty and
//     match the Docker ARG name regex.
//  3. The same key must not appear twice across the flag values
//     (within a single invocation). Dups are a hard error; conductor
//     enforces the same rule but failing client-side keeps the error
//     attached to the user's argv.
//
// Returns ([]BuildArgCliEntry{}, nil) when raw is empty; an empty slice
// (vs nil) is intentional: the request marshaller drops it via
// `omitempty` either way, but tests can distinguish "ran, no entries"
// from "didn't run".
func Parse(raw []string) ([]deploy.BuildArgCliEntry, error) {
	out := make([]deploy.BuildArgCliEntry, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, entry := range raw {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("--build-arg[%d]: expected KEY=VALUE, got %q (missing '=')", i, entry)
		}
		if key == "" {
			return nil, fmt.Errorf("--build-arg[%d]: empty key in %q", i, entry)
		}
		if !IsValidArgName(key) {
			return nil, fmt.Errorf("--build-arg[%d]: invalid ARG name %q (must match ^[A-Za-z_][A-Za-z0-9_]*$ per Docker ARG rules)", i, key)
		}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("--build-arg: key %q supplied more than once (no implicit last-wins; remove the duplicate)", key)
		}
		seen[key] = struct{}{}
		out = append(out, deploy.BuildArgCliEntry{Key: key, Value: value})
	}
	return out, nil
}
