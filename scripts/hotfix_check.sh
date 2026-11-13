#!/usr/bin/env bash
#
# The full checks for a main hotfix, with no side effect on the machine: nothing is installed, no
# branch is checked out, nothing is pushed. Foreman runs it as the blocking first step.
#
# Usage: scripts/hotfix_check.sh vX.Y.Z [reviewed-ref]
#
# Run it inside the managed clone, on the detached HEAD the runner supplies. Every gate of the
# dev-first path stays: the refusals in scripts/hotfix_lib.sh, the sensitivity scan over the exact
# release range (main to the source commit), the whole-tree leak gate, the scannability gate, and
# go build, vet and test. The binary is built into a scratch directory and removed.
#
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/hotfix_lib.sh
source "$HERE/hotfix_lib.sh"
cd "$HERE/.."

VERSION="${1:-}"; REF="${2:-}"
[[ -n "$VERSION" ]] || hotfix_die "usage: scripts/hotfix_check.sh vX.Y.Z [reviewed-ref]"
for tool in git go python3; do command -v "$tool" >/dev/null 2>&1 || hotfix_die "required tool not found: $tool"; done

SCRATCH=""
trap '[[ -z "$SCRATCH" ]] || rm -rf -- "$SCRATCH"' EXIT

hotfix_preflight "$VERSION" "$REF" check
hotfix_sensitivity_scan "$HOTFIX_BASE" "$HOTFIX_SOURCE_SHA"

hotfix_say "leak gate (public repo, whole tree)"
LEAK_OUTPUT="$(python3 scripts/leakcheck.py 2>&1)" || { printf '%s\n' "$LEAK_OUTPUT" >&2; hotfix_die "leak gate failed: remove the identifiers above. Do not hand-edit scripts/leakcheck.baseline."; }
hotfix_say "$(printf '%s' "$LEAK_OUTPUT" | tail -1)"
SCAN_OUTPUT="$(python3 scripts/unscannable_check.py 2>&1)" || { printf '%s\n' "$SCAN_OUTPUT" >&2; hotfix_die "scannability gate failed: leakcheck cannot read the files above."; }
hotfix_say "$(printf '%s' "$SCAN_OUTPUT" | tail -1)"

hotfix_say "go build"; go build ./... || hotfix_die "go build failed"
hotfix_say "go vet";   go vet ./...   || hotfix_die "go vet failed"
hotfix_say "go test";  go test ./...  || hotfix_die "go test failed"

# The release binary, built where nothing is installed.
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/runos-hotfix-check.XXXXXX")"
go build -ldflags="-X github.com/runos-official/cli/version.Version=${VERSION#v}" -o "$SCRATCH/runos" . || hotfix_die "go build of the release binary failed"
BUILT="$(HOME="$SCRATCH/home" "$SCRATCH/runos" version 2>/dev/null || true)"
[[ "$BUILT" == "${VERSION#v}" ]] || hotfix_die "the built binary reports '$BUILT', not ${VERSION#v}"
hotfix_say "built ${VERSION#v} in a scratch directory (nothing installed)"

# The check must leave the source exactly as it found it.
[[ "$(git rev-parse HEAD)" == "$HOTFIX_SOURCE_SHA" ]] || hotfix_die "HEAD moved during the checks"
[[ -z "$(git status --porcelain)" ]] || hotfix_die "the checks left the tree dirty"
hotfix_say "all hotfix checks passed for $VERSION at $HOTFIX_SOURCE_SHA (nothing was tagged, pushed or installed)"
