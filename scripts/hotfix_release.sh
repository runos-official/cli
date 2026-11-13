#!/usr/bin/env bash
#
# Release a reviewed main hotfix: tag the exact source, wait for the release workflow, verify the
# artefact attestation, then fast-forward main.
#
# Usage:
#   scripts/hotfix_release.sh vX.Y.Z [reviewed-ref]            # the release
#   scripts/hotfix_release.sh vX.Y.Z [reviewed-ref] --dry-run  # every refusal, then stop before the tag
#
# Run it inside the managed clone, on the detached HEAD the runner supplies. It never checks out a
# branch, never touches dev or deployed, never installs a binary, and never pushes a branch: the
# reviewed ref is already published. Only the tag is pushed, then main by fast-forward.
#
# The digest and provenance registry is written by Foreman after publication (its registry action),
# not by this script.
#
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/hotfix_lib.sh
source "$HERE/hotfix_lib.sh"

VERSION=""; REF=""; DRY="false"
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY="true" ;;
    -*) hotfix_die "unknown option: $arg" ;;
    *) if [[ -z "$VERSION" ]]; then VERSION="$arg"; elif [[ -z "$REF" ]]; then REF="$arg"; else hotfix_die "unexpected argument: $arg"; fi ;;
  esac
done
[[ -n "$VERSION" ]] || hotfix_die "usage: scripts/hotfix_release.sh vX.Y.Z [reviewed-ref] [--dry-run]"

VERSION="$(hotfix_tag "$VERSION")"
MODE="release"; [[ "$DRY" == "true" ]] && MODE="dry-run"
hotfix_preflight "$VERSION" "$REF" "$MODE"
SOURCE="$HOTFIX_SOURCE_SHA"

if [[ "$DRY" == "true" ]]; then
  hotfix_say "dry run: would tag $VERSION at $SOURCE and push the tag"
  hotfix_say "dry run: would wait for $HOTFIX_RELEASE_WORKFLOW at the tag, verify the attestation of $HOTFIX_ATTEST_ASSET, then fast-forward $HOTFIX_PROD_BRANCH to $SOURCE"
  hotfix_say "dry run: stopping before the tag; nothing was changed"
  exit 0
fi

for tool in git gh python3; do command -v "$tool" >/dev/null 2>&1 || hotfix_die "required tool not found: $tool"; done
gh auth status >/dev/null 2>&1 || hotfix_die "gh is not authenticated"
NWO="$(gh api 'repos/{owner}/{repo}' --jq .full_name)"

# 1. Tag the exact source and push the tag. No branch is pushed or checked out.
git tag "$VERSION" "$SOURCE"
git push --quiet origin "refs/tags/$VERSION"
hotfix_say "tagged $VERSION at $SOURCE and pushed the tag"

# 2. Wait for the release workflow this tag started, and require it ran on the source.
RUN_ID=""; waited=0
while [[ -z "$RUN_ID" && $waited -lt 120 ]]; do
  RUN_ID="$(gh run list --workflow="$HOTFIX_RELEASE_WORKFLOW" --limit 15 --json databaseId,headBranch,event,headSha \
    -q "[.[] | select(.headBranch==\"$VERSION\" and .event==\"push\" and .headSha==\"$SOURCE\")][0].databaseId" 2>/dev/null || true)"
  [[ -n "$RUN_ID" ]] && break
  sleep 10; waited=$((waited + 10))
done
[[ -n "$RUN_ID" ]] || hotfix_die "no release run for $VERSION at $SOURCE (gh run list --workflow=$HOTFIX_RELEASE_WORKFLOW)"
gh run watch "$RUN_ID" --exit-status >/dev/null 2>&1 \
  || hotfix_die "release workflow run $RUN_ID failed (gh run view $RUN_ID --log-failed)"
hotfix_say "release workflow $RUN_ID succeeded"

# 3. Verify the published artefact was built by this workflow at this tag.
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
gh release download "$VERSION" --repo "$NWO" --pattern "$HOTFIX_ATTEST_ASSET" --dir "$TMP" --clobber \
  || hotfix_die "could not download $HOTFIX_ATTEST_ASSET from release $VERSION"
EXPECTED="https://github.com/$NWO/.github/workflows/$HOTFIX_RELEASE_WORKFLOW@refs/tags/$VERSION"
ACTUAL="$(gh attestation verify "$TMP/$HOTFIX_ATTEST_ASSET" --repo "$NWO" --format json 2>/dev/null \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["verificationResult"]["signature"]["certificate"].get("subjectAlternativeName",""))' 2>/dev/null || true)"
[[ -n "$ACTUAL" ]] || hotfix_die "attestation verification returned no signer identity"
[[ "$ACTUAL" == "$EXPECTED" ]] || hotfix_die "attestation identity mismatch: expected '$EXPECTED', got '$ACTUAL'"
hotfix_say "attestation bound to $ACTUAL"

# 4. Record: fast-forward main to the tag's commit. Nothing is forced; dev and deployed are not touched.
hotfix_advance_main "$VERSION" "$SOURCE"
