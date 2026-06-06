#!/usr/bin/env bash
#
# RunOS CLI release runbook: deterministic, fail-fast, automation-friendly.
#
# Usage:
#   scripts/release.sh v1.7.0          # full release
#   scripts/release.sh v1.7.0 --check  # run every gate, stop before tag/push (no side effects)
#   make release VERSION=v1.7.0        # same, via the Makefile
#
# What it does, in order (any failure aborts before the tag is created):
#   1. Preflight   - tools present, VERSION well-formed, tag not already taken,
#                    CHANGELOG has a matching section, working tree clean & synced,
#                    sensitivity scan (PUBLIC repo: fail closed on secret-shaped
#                    content in the release payload).
#   2. Code gates  - go build ./..., go vet ./..., go test ./..., make local.
#   3. Cutover     - fast-forward main to dev (refuses if not fast-forwardable),
#                    tag main, push main + tag + dev.
#   4. CI watch    - wait for the Release workflow run for this tag to succeed.
#   5. Attest      - download a published asset and verify its build-provenance
#                    attestation is bound to release.yml @ refs/tags/<VERSION>.
#
# This is the single source of truth for cutting a CLI release. foreman (or any
# agent driven by the poller) should invoke this rather than running git/gh by
# hand. The CLI ships as GitHub release artifacts, so "deploy" == this release.
# The Templates R2 backfill + k8s deploy is a separate repo with its own process.
#
set -euo pipefail

# ---- constants -------------------------------------------------------------
INTEGRATION_BRANCH="dev"   # where work lands; gates run against this tree
RELEASE_BRANCH="main"      # releases are tagged here
RELEASE_WORKFLOW="release.yml"
ATTEST_ASSET="runos-linux-amd64.tar.gz"  # representative asset for attestation check
CI_POLL_SECONDS=10
CI_MAX_WAIT_SECONDS=900

# ---- output helpers --------------------------------------------------------
GREEN='\033[0;32m'; BLUE='\033[0;34m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; GRAY='\033[0;90m'; NC='\033[0m'
step() { printf "${BLUE}==>${NC} %s\n" "$1"; }
ok()   { printf "${GREEN} ok${NC} %s\n" "$1"; }
warn() { printf "${YELLOW}  !${NC} %s\n" "$1"; }
die()  { printf "${RED}FAIL${NC} %s\n" "$1" >&2; exit 1; }

# ---- args ------------------------------------------------------------------
VERSION="${1:-}"
CHECK_ONLY="false"
[[ "${2:-}" == "--check" || "${1:-}" == "--check" ]] && CHECK_ONLY="true"
[[ "$VERSION" == "--check" ]] && VERSION=""

[[ -n "$VERSION" ]] || die "usage: scripts/release.sh vX.Y.Z [--check]"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || \
  die "VERSION must look like v1.7.0 (got '$VERSION')"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# =============================================================================
# 1. Preflight
# =============================================================================
step "Preflight ($VERSION)"
for tool in git go gh; do
  command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done
gh auth status >/dev/null 2>&1 || die "gh is not authenticated (run: gh auth login)"

git rev-parse --git-dir >/dev/null 2>&1 || die "not inside a git repository"
NWO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
ok "repo $NWO"

# Tag must not already exist (locally or on the remote).
git fetch --quiet --tags origin
if git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null; then
  die "tag $VERSION already exists locally"
fi
if git ls-remote --exit-code --tags origin "refs/tags/$VERSION" >/dev/null 2>&1; then
  die "tag $VERSION already exists on origin"
fi

# CHANGELOG must carry a section for exactly this version (explicit-arg model).
grep -qE "^## ${VERSION//./\\.}([[:space:]]|$)" CHANGELOG.md || \
  die "CHANGELOG.md has no '## $VERSION' section (add release notes first)"
ok "CHANGELOG has a $VERSION section"

# Working tree must be clean.
[[ -z "$(git status --porcelain)" ]] || die "working tree is dirty; commit or stash first"

# Both branches must exist and be in sync with origin (no behind/ahead surprises).
git fetch --quiet origin "$INTEGRATION_BRANCH" "$RELEASE_BRANCH"
for br in "$INTEGRATION_BRANCH" "$RELEASE_BRANCH"; do
  git rev-parse -q --verify "refs/heads/$br" >/dev/null || die "missing local branch: $br"
done
[[ "$(git rev-parse "$INTEGRATION_BRANCH")" == "$(git rev-parse "origin/$INTEGRATION_BRANCH")" ]] || \
  die "$INTEGRATION_BRANCH is not in sync with origin/$INTEGRATION_BRANCH"
# main may legitimately be behind dev (we are about to fast-forward it), but it
# must not have commits origin/main lacks, and must be an ancestor of dev.
[[ "$(git rev-parse "$RELEASE_BRANCH")" == "$(git rev-parse "origin/$RELEASE_BRANCH")" ]] || \
  die "$RELEASE_BRANCH is not in sync with origin/$RELEASE_BRANCH"
git merge-base --is-ancestor "$RELEASE_BRANCH" "$INTEGRATION_BRANCH" || \
  die "$RELEASE_BRANCH cannot fast-forward to $INTEGRATION_BRANCH (histories diverged); reconcile manually"
ok "branches clean & synced; $RELEASE_BRANCH fast-forwardable to $INTEGRATION_BRANCH"

# ---- Sensitivity scan: this is a PUBLIC repo ------------------------------
# Deterministic, fail-closed floor over the exact payload being released
# (every added line in main..dev). High-precision secret patterns only, so it
# does not false-positive on commit SHAs, pinned action SHAs, or the public
# OIDC identity URL. Fuzzy judgment (real account/cluster/app IDs, OSIDs, org
# or customer names, context-dependent leaks) is the skill's reasoning audit,
# NOT this gate. The two layers are intentional: this can never be bypassed.
step "Sensitivity scan (public repo)"
ADDED_LINES="$(git diff "$RELEASE_BRANCH..$INTEGRATION_BRANCH" -- . | grep '^+' | grep -v '^+++' || true)"
# Provider token prefixes, cloud keys, and PEM private-key headers: near-zero
# false-positive rate. Plus quoted credential assignments (quotes required so
# prose like `password: POSTGRES_PASSWORD` does not trip it).
SECRET_RE='(gh[pousr]_[A-Za-z0-9]{20,})'
SECRET_RE+='|(github_pat_[A-Za-z0-9_]{20,})'
SECRET_RE+='|(xox[baprs]-[A-Za-z0-9-]{10,})'
SECRET_RE+='|(AKIA[0-9A-Z]{16})'
SECRET_RE+='|(-----BEGIN [A-Z ]*PRIVATE KEY-----)'
SECRET_RE+='|((api[_-]?key|secret|password|passwd|token|bearer)["'\'' ]*[:=]["'\'' ]*["'\''][A-Za-z0-9/+_.=-]{16,}["'\''])'
SECRET_HITS="$(printf '%s\n' "$ADDED_LINES" | grep -niE "$SECRET_RE" || true)"
if [[ -n "$SECRET_HITS" ]]; then
  warn "secret-shaped content in the release payload ($RELEASE_BRANCH..$INTEGRATION_BRANCH):"
  printf '%s\n' "$SECRET_HITS" | head -20 >&2
  die "sensitivity scan failed (public repo): remove the above before releasing (or, if a confirmed false positive, narrow it in scripts/release.sh)"
fi
ok "no secret-shaped content in release payload"

# =============================================================================
# 2. Code gates (run against the dev tree that will be released)
# =============================================================================
ORIGINAL_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
git checkout --quiet "$INTEGRATION_BRANCH"

step "Build"
go build ./... || die "go build failed"
ok "go build"

step "Vet"
go vet ./... || die "go vet failed"
ok "go vet"

step "Test"
go test ./... || die "go test failed"
ok "go test"

step "make local (build + install to ~/.local/bin)"
make local || die "make local failed"
ok "make local"

if [[ "$CHECK_ONLY" == "true" ]]; then
  git checkout --quiet "$ORIGINAL_BRANCH"
  printf "${GREEN}All gates passed.${NC} --check mode: stopping before tag/push.\n"
  exit 0
fi

# =============================================================================
# 3. Cutover: fast-forward main, tag, push
# =============================================================================
step "Cutover: fast-forward $RELEASE_BRANCH to $INTEGRATION_BRANCH"
git checkout --quiet "$RELEASE_BRANCH"
git merge --ff-only "$INTEGRATION_BRANCH" --quiet || die "fast-forward of $RELEASE_BRANCH failed"
RELEASE_SHA="$(git rev-parse --short HEAD)"
ok "$RELEASE_BRANCH at $RELEASE_SHA"

step "Tag $VERSION"
git tag "$VERSION"
git push --quiet origin "$RELEASE_BRANCH"
git push --quiet origin "$VERSION"
# Keep dev in sync (it equals main now, but origin/dev ref should match).
git checkout --quiet "$INTEGRATION_BRANCH"
git push --quiet origin "$INTEGRATION_BRANCH"
ok "pushed $RELEASE_BRANCH, $INTEGRATION_BRANCH, and tag $VERSION"

# =============================================================================
# 4. Watch the Release workflow run for this tag
# =============================================================================
step "Waiting for Release workflow run for $VERSION"
RUN_ID=""
waited=0
while [[ -z "$RUN_ID" && $waited -lt 60 ]]; do
  RUN_ID="$(gh run list --workflow="$RELEASE_WORKFLOW" --limit 15 \
    --json databaseId,headBranch,event \
    -q "[.[] | select(.headBranch==\"$VERSION\" and .event==\"push\")][0].databaseId" 2>/dev/null || true)"
  [[ -n "$RUN_ID" ]] && break
  sleep "$CI_POLL_SECONDS"; waited=$((waited + CI_POLL_SECONDS))
done
[[ -n "$RUN_ID" ]] || die "could not find a Release run for $VERSION (check: gh run list --workflow=$RELEASE_WORKFLOW)"
ok "run $RUN_ID"

if ! gh run watch "$RUN_ID" --exit-status >/dev/null 2>&1; then
  die "Release workflow run $RUN_ID failed (inspect: gh run view $RUN_ID --log-failed)"
fi
ok "Release workflow succeeded"

# =============================================================================
# 5. Verify build-provenance attestation on a published asset
# =============================================================================
step "Verifying attestation on $ATTEST_ASSET"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
gh release download "$VERSION" --repo "$NWO" --pattern "$ATTEST_ASSET" --dir "$TMP" --clobber \
  || die "could not download $ATTEST_ASSET from release $VERSION"

EXPECTED_SAN="https://github.com/$NWO/.github/workflows/$RELEASE_WORKFLOW@refs/tags/$VERSION"
ACTUAL_SAN="$(gh attestation verify "$TMP/$ATTEST_ASSET" --repo "$NWO" --format json 2>/dev/null \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["verificationResult"]["signature"]["certificate"].get("subjectAlternativeName",""))' 2>/dev/null || true)"

[[ -n "$ACTUAL_SAN" ]] || die "attestation verification returned no signer identity"
[[ "$ACTUAL_SAN" == "$EXPECTED_SAN" ]] || \
  die "attestation identity mismatch: expected '$EXPECTED_SAN', got '$ACTUAL_SAN'"
ok "attestation bound to $ACTUAL_SAN"

# =============================================================================
# Done
# =============================================================================
printf "\n${GREEN}Released %s${NC} (%s at %s)\n" "$VERSION" "$RELEASE_BRANCH" "$RELEASE_SHA"
printf "${GRAY}Release: $(gh release view "$VERSION" --repo "$NWO" --json url -q .url 2>/dev/null)${NC}\n"
printf "${GRAY}Note: this is CLI go-live step 1. Templates R2 backfill + installer deploy are separate.${NC}\n"
