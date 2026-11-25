#!/usr/bin/env bash
#
# Shared helpers for the main-line hotfix recipe: scripts/hotfix_check.sh,
# scripts/hotfix_release.sh and .foreman/pipelines/hotfix-main.yml. SOURCE this file; it runs
# nothing.
#
# WHY NEW FILES. scripts/release.sh and scripts/record_ref.sh are the dev-first release path and
# change on dev. The hotfix recipe lives beside them so the later main-to-dev merge has nothing to
# conflict on. It never calls them and never edits them.
#
# THE RULES, from Foreman's docs/main-hotfix.md:
#   - The managed runner supplies an exact commit on a DETACHED HEAD. That HEAD is the source:
#     `git rev-parse HEAD` is its full id. Nothing here checks out a branch or switches a worktree.
#   - It never touches dev or deployed, and never installs a binary over the operator's.
#   - main must be an ancestor of the source, and the release range starts at main itself.
#   - The tag is created at the source id. main moves by fast-forward only, after the published
#     artefacts are verified.
#
# Public repo: no hostnames, ids or addresses here.

HOTFIX_PROD_BRANCH="main"
HOTFIX_ATTEST_ASSET="runos-linux-amd64.tar.gz"
HOTFIX_RELEASE_WORKFLOW="release.yml"

hotfix_say() { printf 'hotfix: %s\n' "$1" >&2; }
hotfix_die() { printf 'hotfix: FAIL %s\n' "$1" >&2; exit 1; }
hotfix_fact() { local k="$1"; shift; printf '::foreman:: %s=%s\n' "$k" "$(printf %s "$*" | tr '\r\n' '  ')"; }

# hotfix_origin_sha <branch>: the commit the branch holds on origin, or nothing when absent. A failed
# query dies, so a transport failure never reads as "absent".
hotfix_origin_sha() {
  local out rc=0
  out="$(git ls-remote --exit-code origin "refs/heads/$1" 2>/dev/null)" || rc=$?
  case "$rc" in
    0) printf '%s' "${out%%[[:space:]]*}" ;;
    2) printf '' ;;
    *) hotfix_die "could not query origin for refs/heads/$1 (git ls-remote exit $rc). Refusing to guess." ;;
  esac
}

# hotfix_main_version: the highest final tag reachable from origin/main, the version prod runs.
hotfix_main_version() {
  git tag --merged "refs/remotes/origin/$HOTFIX_PROD_BRANCH" --list 'v[0-9]*' \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -t. -k1.2,1n -k2,2n -k3,3n | tail -1
}

# hotfix_next_patch <vMAJOR.MINOR.PATCH>: the version after it in the same minor line.
hotfix_next_patch() {
  local v="${1#v}" major minor patch
  IFS=. read -r major minor patch <<<"$v"
  printf 'v%s.%s.%s' "$major" "$minor" "$((patch + 1))"
}

# hotfix_preflight <version> [reviewed-ref] <mode>   (mode: check, dry-run or release)
#
# Refuses, with a plain message and a non-zero exit, when: the version is not a final; the tree is
# dirty; a checked-out branch is not the reviewed ref; the tag already exists; main is not an
# ancestor of HEAD, or HEAD is main itself; the version is not the next patch of the version main
# runs; HEAD's CHANGELOG has no section for the version; the reviewed ref on origin is not HEAD.
#
# The version may arrive as 1.2.3 (Foreman's `${{ version }}`) or as v1.2.3. hotfix_tag turns either
# into the tag spelling vX.Y.Z; anything else is refused by the pattern in hotfix_preflight.
hotfix_tag() { local v="$1"; [[ "$v" == v* ]] && printf '%s' "$v" || printf 'v%s' "$v"; }

# Sets HOTFIX_SOURCE_SHA (the full id of HEAD, the commit that will be tagged), HOTFIX_MAIN_SHA and
# HOTFIX_BASE (main, the start of the release range).
hotfix_preflight() {
  local version ref="${2:-}" mode="$3" branch
  version="$(hotfix_tag "$1")"
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || hotfix_die "$version is not a final version (vX.Y.Z); a main hotfix is a final patch"
  git rev-parse --git-dir >/dev/null 2>&1 || hotfix_die "not inside a git repository"
  [[ -z "$(git status --porcelain)" ]] || hotfix_die "working tree is dirty; the release must build exactly the source commit"
  ref="${ref#refs/heads/}"

  HOTFIX_SOURCE_SHA="$(git rev-parse --verify HEAD)"
  # The runner supplies a detached HEAD. If a branch IS checked out it must be the reviewed ref,
  # or this would release something nobody reviewed.
  branch="$(git symbolic-ref --quiet --short HEAD || true)"
  if [[ -n "$ref" && -n "$branch" && "$branch" != "$ref" ]]; then
    hotfix_die "the checked-out branch $branch is not the reviewed hotfix ref $ref"
  fi

  git fetch --quiet --tags origin "+refs/heads/$HOTFIX_PROD_BRANCH:refs/remotes/origin/$HOTFIX_PROD_BRANCH" \
    || hotfix_die "cannot fetch $HOTFIX_PROD_BRANCH and the tags from origin"
  HOTFIX_MAIN_SHA="$(git rev-parse --verify "refs/remotes/origin/$HOTFIX_PROD_BRANCH")"

  if git rev-parse -q --verify "refs/tags/$version" >/dev/null; then
    hotfix_die "tag $version already exists locally"
  fi
  [[ -z "$(git ls-remote --tags origin "refs/tags/$version" 2>/dev/null)" ]] \
    || hotfix_die "tag $version already exists on origin"

  git merge-base --is-ancestor "$HOTFIX_MAIN_SHA" "$HOTFIX_SOURCE_SHA" \
    || hotfix_die "origin/$HOTFIX_PROD_BRANCH (${HOTFIX_MAIN_SHA:0:12}) is not an ancestor of the source (${HOTFIX_SOURCE_SHA:0:12}); cut the hotfix from $HOTFIX_PROD_BRANCH"
  HOTFIX_BASE="$(git merge-base "$HOTFIX_SOURCE_SHA" "$HOTFIX_MAIN_SHA")"
  [[ "$HOTFIX_BASE" == "$HOTFIX_MAIN_SHA" ]] \
    || hotfix_die "the release base is not $HOTFIX_PROD_BRANCH itself"
  [[ "$HOTFIX_SOURCE_SHA" != "$HOTFIX_MAIN_SHA" ]] \
    || hotfix_die "the source is $HOTFIX_PROD_BRANCH itself: there is nothing to release"

  local running next
  running="$(hotfix_main_version)"
  [[ -n "$running" ]] || hotfix_die "no final version tag is reachable from origin/$HOTFIX_PROD_BRANCH, so the version prod runs is unknown"
  next="$(hotfix_next_patch "$running")"
  [[ "$version" == "$next" ]] \
    || hotfix_die "$version is not the next patch of the version $HOTFIX_PROD_BRANCH runs ($running); the next patch is $next"

  # The CHANGELOG is read from the commit that will be tagged, not from the working tree.
  # Read into a variable first: `git show | grep -q` makes git take a SIGPIPE on a long file, which
  # pipefail turns into a false "no section".
  local changelog
  changelog="$(git show "$HOTFIX_SOURCE_SHA:CHANGELOG.md" 2>/dev/null || true)"
  grep -qE "^## ${version//./\\.}([[:space:]]|\$)" <<<"$changelog" \
    || hotfix_die "CHANGELOG.md at the source commit has no '## $version' section"

  if [[ -n "$ref" ]]; then
    local published
    published="$(hotfix_origin_sha "$ref")"
    if [[ -z "$published" ]]; then
      [[ "$mode" != "release" ]] || hotfix_die "the reviewed ref $ref is not published on origin"
      hotfix_say "warning: the reviewed ref $ref is not published on origin; a real release requires it"
    elif [[ "$published" != "$HOTFIX_SOURCE_SHA" ]]; then
      hotfix_die "the reviewed ref $ref moved on origin to ${published:0:12}; the source is ${HOTFIX_SOURCE_SHA:0:12}"
    fi
  fi
  hotfix_say "source ${HOTFIX_SOURCE_SHA}, $version is the next patch after $running"
  hotfix_say "release range ${HOTFIX_MAIN_SHA:0:12}..${HOTFIX_SOURCE_SHA:0:12}: $(git rev-list --count "$HOTFIX_MAIN_SHA..$HOTFIX_SOURCE_SHA") commit(s) beyond $HOTFIX_PROD_BRANCH"
}

# hotfix_sensitivity_scan <base> <head>: the deterministic floor over every line the range adds.
# PUBLIC repo: fail closed on secret-shaped content. The filter below is the same one
# scripts/release.sh uses; scripts/hotfix_replay.sh fails when the two differ.
hotfix_sensitivity_scan() {
  local base="$1" head="$2" added hits
  command -v python3 >/dev/null 2>&1 || hotfix_die "python3 is required for the sensitivity scan"
  added="$(git diff "$base..$head" -- . | grep '^+' | grep -v '^+++' || true)"
  read -r -d '' PLACEHOLDER_FILTER <<'PYFILTER' || true
import re, sys

CREDENTIAL = re.compile(
    r"(gh[pousr]_[A-Za-z0-9]{20,})"
    r"|(github_pat_[A-Za-z0-9_]{20,})"
    r"|(runos_pat_[A-Za-z0-9]{6,}\.[A-Za-z0-9]{20,})"
    r"|(xox[baprs]-[A-Za-z0-9-]{10,})"
    r"|(AKIA[0-9A-Z]{16})"
    r"|(-----BEGIN [A-Z ]*PRIVATE KEY-----)"
    r"|((api[_-]?key|secret|password|passwd|token|bearer)[\"' ]*[:=][\"' ]*[\"'][A-Za-z0-9/+_.=-]{16,}[\"'])",
    re.IGNORECASE,
)
QUOTED_VALUE = re.compile(r"[\"']([A-Za-z0-9/+_.=-]{16,})[\"']\s*$")
NOT_A_SECRET_VALUE = re.compile(r"^(?:[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+|[a-z]+(?:-[a-z]+)+)$")

for number, line in enumerate(sys.stdin, 1):
    for match in CREDENTIAL.finditer(line):
        value = QUOTED_VALUE.search(match.group(0))
        if value and NOT_A_SECRET_VALUE.match(value.group(1)):
            continue
        sys.stdout.write("%d:%s" % (number, line if line.endswith("\n") else line + "\n"))
        break
PYFILTER
  hits="$(printf '%s\n' "$added" | python3 -c "$PLACEHOLDER_FILTER" || true)"
  if [[ -n "$hits" ]]; then
    printf '%s\n' "$hits" | head -20 >&2
    hotfix_die "sensitivity scan failed (public repo): secret-shaped content in $base..$head"
  fi
  hotfix_say "no secret-shaped content in $base..$head"
}

# hotfix_advance_main <version> <source-sha>: fast-forward main to the source, never force. The tag
# must exist on origin and name the source. Prints main_before, main_after and main_equals_final.
hotfix_advance_main() {
  local version="$1" source="$2" tagged before after
  git fetch --quiet --tags origin "+refs/heads/$HOTFIX_PROD_BRANCH:refs/remotes/origin/$HOTFIX_PROD_BRANCH" \
    || hotfix_die "cannot fetch $HOTFIX_PROD_BRANCH and the tags from origin"
  tagged="$(git rev-parse -q --verify "refs/tags/$version^{commit}" || true)"
  [[ -n "$tagged" ]] || hotfix_die "tag $version does not exist; main follows the tag"
  [[ "$tagged" == "$source" ]] || hotfix_die "tag $version names ${tagged:0:12}, not the source ${source:0:12}; main is not moved"
  before="$(hotfix_origin_sha "$HOTFIX_PROD_BRANCH")"
  [[ -n "$before" ]] || hotfix_die "$HOTFIX_PROD_BRANCH does not exist on origin; this recipe does not create it"
  hotfix_fact main_before "$before"
  if [[ "$before" != "$source" ]]; then
    git merge-base --is-ancestor "$before" "$source" || {
      hotfix_fact main_after "$before"; hotfix_fact main_equals_final false
      hotfix_die "$HOTFIX_PROD_BRANCH on origin (${before:0:12}) is not an ancestor of ${source:0:12}. This only fast-forwards and never forces. Reconcile $HOTFIX_PROD_BRANCH by hand."
    }
    git push --quiet origin "$source:refs/heads/$HOTFIX_PROD_BRANCH" || {
      hotfix_fact main_after "$(hotfix_origin_sha "$HOTFIX_PROD_BRANCH")"; hotfix_fact main_equals_final false
      hotfix_die "the push of ${source:0:12} to $HOTFIX_PROD_BRANCH was refused"
    }
  fi
  after="$(hotfix_origin_sha "$HOTFIX_PROD_BRANCH")"
  hotfix_fact main_after "$after"
  if [[ "$after" == "$source" ]]; then
    hotfix_fact main_equals_final true
    printf 'main_equals_final=true\n'
    hotfix_say "$HOTFIX_PROD_BRANCH is at ${after:0:12} (was ${before:0:12})"
  else
    hotfix_fact main_equals_final false
    hotfix_die "$HOTFIX_PROD_BRANCH is at ${after:0:12} after the push, not at the source ${source:0:12}"
  fi
}
