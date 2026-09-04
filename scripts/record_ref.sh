#!/usr/bin/env bash
#
# The release RECORD REF, in one place.
#
# The record ref is the branch that records what has shipped: the shared
# `deployed`, or an objective-scoped `deployed-rc/obj-N` (item 198).
#
# WHY THIS FILE EXISTS. The rule used to live in three copies: the preflight in
# scripts/release.sh, the advance in .foreman/pipelines/release.yml, and the
# manual advance in scripts/release.sh step 6. Two of the three drifted. The
# pipeline pushed straight at refs/heads/$record and then ran a fetch with no
# destination refspec, which moves FETCH_HEAD and the remote-tracking ref only,
# so the LOCAL branch never moved. The preflight read that local branch and died
# "not in sync with origin/...". Deployment 36 died exactly there. One definition
# site is the fix; two copies of a rule are a defect waiting for its second half.
#
# THE AUTHORITY IS THE REMOTE. Every check reads origin/<record>. A stale or an
# absent local copy neither fails the preflight nor silently skips it.
#
# DESIGN CONTRACT, so the replay harness can drive the real code:
#   - Operates on the CURRENT working directory's repository. Never cd.
#   - Uses git only. No gh, no go, no GitHub remote. A local bare repo as
#     `origin` is enough.
#   - Each subcommand does one thing and exits with its verdict.
#   - `check` prints PAYLOAD_BASE, one line, on STDOUT, and ONLY on success.
#     Every human-readable line goes to STDERR. Callers read one value from
#     stdout and trust the exit code.
#   - Plain text with a `record-ref:` prefix, no colour: this also runs inside a
#     pipeline step, where colour is noise.
#
# Usage:
#   scripts/record_ref.sh check   <record> <integration-branch>
#   scripts/record_ref.sh advance <record> <source-ref>
#
set -euo pipefail

say()  { printf 'record-ref: %s\n' "$1" >&2; }
die()  { printf 'record-ref: FAIL %s\n' "$1" >&2; exit 1; }

# ---- shared discovery, and it FAILS CLOSED ---------------------------------
#
# A fetch that fails for a transport or an authentication reason must NEVER read
# as "the record ref does not exist yet". That misreading skips every check on
# the shared `deployed` silently, which is the class of defect this file exists
# to remove. So existence is queried explicitly and the three outcomes stay
# separate:
#
#   exit 0 -> the ref EXISTS. The fetch that follows MUST succeed.
#   exit 2 -> `--exit-code` found no matching ref, so the ref is ABSENT. This is
#             the only path that may report a first release.
#   other  -> the query FAILED. Die, naming origin and the exit code.
#
# No `|| true` guards any discovery command here. `|| true` is precisely what
# turns a transport failure into a silent skip.
#
# Sets RECORD_ON_ORIGIN to "true" or "false".
discover_record_ref() {
  local record="$1" rc=0
  git ls-remote --exit-code origin "refs/heads/$record" >/dev/null 2>&1 || rc=$?
  case "$rc" in
    0) RECORD_ON_ORIGIN="true"  ;;
    2) RECORD_ON_ORIGIN="false" ;;
    *) die "could not query origin for refs/heads/$record (git ls-remote exit $rc). Refusing to guess: a transport or authentication failure must not read as a first release." ;;
  esac
}

# fetch_record_tracking updates refs/remotes/origin/<record> and dies if the
# fetch fails. Only ever called when discovery said the ref EXISTS, so a failure
# here is real and must stop the release.
fetch_record_tracking() {
  local record="$1"
  git fetch --quiet origin "+refs/heads/$record:refs/remotes/origin/$record" \
    || die "origin has refs/heads/$record but fetching it failed; refusing to continue"
}

# short_or_absent prints an abbreviated sha, or "absent" when the ref is missing.
short_or_absent() {
  git rev-parse -q --verify --short "$1" 2>/dev/null || printf 'absent'
}

# objective_scoped reports whether this record ref is an objective-scoped one.
#
# DEPLOYED_BRANCH defaults to `deployed` and is overridden only by
# RUNOS_RELEASE_RECORD_BRANCH, which exists only for an objective-scoped release
# (item 198). So the scope test IS the override: any record ref that is not
# `deployed` is objective-scoped. The shared ref cannot reach the exemption,
# because `deployed` is the default that the override replaces.
objective_scoped() {
  [[ "$1" != "deployed" ]]
}

# ---- check -----------------------------------------------------------------
cmd_check() {
  local record="$1" integration="$2"

  discover_record_ref "$record"

  local local_sha remote_sha
  if [[ "$RECORD_ON_ORIGIN" == "true" ]]; then
    fetch_record_tracking "$record"
  fi
  local_sha="$(short_or_absent "refs/heads/$record")"
  remote_sha="$(short_or_absent "refs/remotes/origin/$record")"
  say "$record local=$local_sha remote=$remote_sha"

  if [[ "$RECORD_ON_ORIGIN" != "true" ]]; then
    say "$record does not exist on origin yet; it will be created on the first deploy"
  else
    # A local copy that differs from origin is a WARNING, not a death.
    #
    # The pipeline never reads the local copy, and `advance` re-points it, so
    # the drift is self-healing. Dying on it IS the deployment 36 failure this
    # story removes. So the old local-vs-origin refusal is GONE, deliberately.
    # What still refuses for the shared `deployed` is the ancestor test below,
    # which reads origin, and a failed discovery, which dies before this point.
    if [[ "$local_sha" != "absent" && "$local_sha" != "$remote_sha" ]]; then
      say "warning: the local copy of $record ($local_sha) differs from origin ($remote_sha). The advance re-points it; no check below reads it."
    fi

    if objective_scoped "$record"; then
      # DELIBERATE AND VISIBLE EXEMPTION (finding 27 fixer caution).
      # An objective-scoped RC records a commit of an UNMERGED objective branch,
      # which is not on the integration branch yet. Applying the ancestor test
      # would refuse every RC after the first one. The shared `deployed` keeps
      # the test; only a scoped ref is exempt.
      say "$record is objective-scoped, so the ancestor-of-$integration test is SKIPPED deliberately: an RC records an unmerged objective branch. See RunOS item 253."
    else
      git merge-base --is-ancestor "refs/remotes/origin/$record" "$integration" \
        || die "refs/remotes/origin/$record is not an ancestor of $integration (histories diverged); reconcile manually"
      say "$record on origin is an ancestor of $integration"
    fi
  fi

  # PAYLOAD_BASE: the last shipped point, on STDOUT and only on success.
  #
  # The shared record ref is read from its REMOTE tracking ref, so the value is
  # correct even on a host that carries no local branch. That is a strengthening
  # over the old code, which needed the local branch to exist.
  #
  # An objective-scoped run keeps the old base, the last tag on the integration
  # branch, because origin/<scoped record> is not on that branch. The scan window
  # this leaves is bounded by RunOS item 253 and is out of scope here; the
  # whole-tree leak gate reads the entire tree and cannot be skipped, so the real
  # floor is unaffected.
  local payload_base
  if [[ "$RECORD_ON_ORIGIN" == "true" ]] && ! objective_scoped "$record"; then
    payload_base="refs/remotes/origin/$record"
  elif payload_base="$(git describe --tags --abbrev=0 "$integration" 2>/dev/null)"; then
    :
  else
    payload_base="$(git rev-list --max-parents=0 "$integration" | tail -1)"
  fi
  printf '%s\n' "$payload_base"
}

# ---- advance ---------------------------------------------------------------
cmd_advance() {
  local record="$1" source_ref="$2"

  discover_record_ref "$record"

  # THE LEASE CARRIES ITS EXPECTED VALUE EXPLICITLY.
  #
  # A bare `--force-with-lease` does NOT mean "compare against whatever I last
  # fetched". Git derives the lease from the remote-tracking ref that the
  # remote's CONFIGURED FETCH REFSPEC maps the destination to. A clone made with
  # `--single-branch --branch dev` carries `+refs/heads/dev:refs/remotes/origin/dev`
  # only, so the record ref is not covered, and fetching it into a tracking ref
  # by hand gives the bare lease nothing to read: the push is still rejected with
  # "stale info". Measured on git 2.39.5; the replay's case 6 keeps that measured.
  #
  # The lease is NEVER dropped. No --force, no `|| git push --force` fallback.
  # A rejected push is a real answer: someone else moved the ref, and the release
  # stops (objective 83 plan rule U10).
  local lease
  if [[ "$RECORD_ON_ORIGIN" == "true" ]]; then
    fetch_record_tracking "$record"
    local expected
    expected="$(git rev-parse -q --verify "refs/remotes/origin/$record" || true)"
    [[ -n "$expected" ]] || die "origin has refs/heads/$record but its value could not be read after a successful fetch"
    lease="refs/heads/$record:$expected"
    say "advancing $record from $expected to $source_ref"
  else
    # A lease that expects the ref to be ABSENT, so a ref created between the
    # query and the push is not silently clobbered.
    lease="refs/heads/$record:"
    say "creating $record at $source_ref (absent on origin)"
  fi

  git push --quiet --force-with-lease="$lease" origin "$source_ref:refs/heads/$record"

  # Move the LOCAL copy too, so a persistent host never falls behind again. This
  # is the half the pipeline used to miss: a fetch with no destination refspec
  # moves FETCH_HEAD and the tracking ref only.
  #
  # Git refuses to fetch into the branch that is checked out. The pipeline checks
  # out target.branch, so this guard never fires there; it exists because this
  # script is callable from the manual flow too.
  local head_branch
  head_branch="$(git symbolic-ref -q --short HEAD || true)"
  if [[ "$head_branch" == "$record" ]]; then
    say "$record is the checked-out branch, so its local copy is left to the caller"
  else
    git fetch --quiet origin \
      "+refs/heads/$record:refs/heads/$record" \
      "+refs/heads/$record:refs/remotes/origin/$record" \
      || die "the push succeeded but $record could not be fetched back; the local copy is stale"
  fi

  # Behavior 5 as an ASSERTION, not a claim.
  local local_sha remote_sha
  local_sha="$(short_or_absent "refs/heads/$record")"
  remote_sha="$(short_or_absent "refs/remotes/origin/$record")"
  say "$record local=$local_sha remote=$remote_sha"
  if [[ "$head_branch" != "$record" && "$local_sha" != "$remote_sha" ]]; then
    die "$record local ($local_sha) and remote ($remote_sha) disagree after the advance"
  fi
}

# ---- dispatch --------------------------------------------------------------
usage() {
  cat >&2 <<'EOF'
usage:
  scripts/record_ref.sh check   <record> <integration-branch>
  scripts/record_ref.sh advance <record> <source-ref>
EOF
  exit 2
}

[[ $# -eq 3 ]] || usage
case "$1" in
  check)   cmd_check   "$2" "$3" ;;
  advance) cmd_advance "$2" "$3" ;;
  *)       usage ;;
esac
