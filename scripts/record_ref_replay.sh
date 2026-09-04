#!/usr/bin/env bash
#
# Replay the release record-ref behaviour in a scratch bare repo.
#
# WHY. Findings 27 and 20 were both about a release that cannot be rehearsed:
# scripts/release.sh needs gh, a real GitHub remote, and its own repository, so
# nobody could exercise the record-ref rule without cutting a release. This
# harness drives the REAL scripts/record_ref.sh by absolute path (never a copy
# of it, because a copy is the thing that drifts) against a local bare repo.
#
# It needs no network, no gh and no go. It builds everything under mktemp -d and
# removes it at exit.
#
# Run it with:  make replay-record-ref
#
# Case 0 reproduces the DEFECT with the old commands. Cases 1 to 8 exercise the
# new script, including every refusal, because a gate that is never seen to
# refuse is not known to work. Case 9 exercises the scripts/release.sh call
# shape, which `release.sh --check` cannot rehearse in a container without gh.
#
set -euo pipefail

R="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RECORD_REF="$R/scripts/record_ref.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

PASS=0
FAIL=0
ok()   { printf '  PASS  %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '  FAIL  %s\n' "$1"; FAIL=$((FAIL + 1)); }
case_hdr() { printf '\n=== %s\n' "$1"; }
check() { if [[ "$2" == "$3" ]]; then ok "$1 ($2)"; else bad "$1: got '$2', want '$3'"; fi; }

# git needs an identity and a predictable default branch in a scratch HOME.
export GIT_CONFIG_GLOBAL="$T/gitconfig"
export GIT_CONFIG_NOSYSTEM=1
git config --file "$GIT_CONFIG_GLOBAL" user.email replay@example.invalid
git config --file "$GIT_CONFIG_GLOBAL" user.name  "record-ref replay"
git config --file "$GIT_CONFIG_GLOBAL" init.defaultBranch dev
git config --file "$GIT_CONFIG_GLOBAL" advice.detachedHead false

commit_on() { # commit_on <dir> <message>
  ( cd "$1" && echo "$2 $(date +%s%N)" >> log.txt && git add log.txt && git commit --quiet -m "$2" )
}

printf 'record-ref replay\n'
printf 'git: %s\n' "$(git --version)"
printf 'scripts under test: %s\n' "$RECORD_REF"

# ---- layout ----------------------------------------------------------------
git init --quiet --bare "$T/origin.git"
git init --quiet "$T/seed"
commit_on "$T/seed" "root"
( cd "$T/seed" && git branch -M dev && git remote add origin "$T/origin.git" && git push --quiet -u origin dev )

git clone --quiet "$T/origin.git" "$T/host"          # the persistent release host
git clone --quiet "$T/origin.git" "$T/other"         # a second actor
cd "$T/host"

# =============================================================================
case_hdr "Case 0, the CONTROL: the defect as it stands today"
# =============================================================================
# The OLD advance, run verbatim: push straight at the ref, then a fetch with no
# destination refspec.
git push --quiet --force-with-lease origin dev:refs/heads/deployed
git fetch --quiet origin deployed
c0_local="$(git rev-parse -q --verify refs/heads/deployed 2>/dev/null || echo absent)"
c0_remote="$(git rev-parse refs/remotes/origin/deployed)"
printf '  old advance left: local=%s remote=%s\n' "${c0_local:0:7}" "${c0_remote:0:7}"
if [[ "$c0_local" == "absent" ]]; then
  ok "the old advance never created refs/heads/deployed (this is the drift)"
else
  bad "expected no local refs/heads/deployed after the old advance"
fi
# The OLD preflight comparison, as scripts/release.sh ran it before this story.
# It SKIPPED every check because the local branch was absent, which is the
# silent-skip half of the defect.
if git rev-parse -q --verify refs/heads/deployed >/dev/null 2>&1; then
  bad "old preflight would have run its checks"
else
  ok "the old preflight skipped every record-ref check (local branch absent)"
fi
# Now give the host a stale local branch, which is the release host's real state,
# and show the old preflight DYING on it. This is deployment 36.
git update-ref refs/heads/deployed "$(git rev-parse HEAD~0)"
commit_on "$T/other" "second release commit"
( cd "$T/other" && git push --quiet origin dev && git push --quiet --force origin dev:refs/heads/deployed )
git fetch --quiet origin deployed || true
if [[ "$(git rev-parse refs/heads/deployed)" == "$(git rev-parse refs/remotes/origin/deployed)" ]]; then
  bad "expected the stale local branch to disagree with origin"
else
  ok "old preflight: local != origin, so it died 'not in sync' (deployment 36)"
fi
# Reset the scratch state for the cases that follow.
git fetch --quiet origin dev && git reset --quiet --hard origin/dev
git update-ref -d refs/heads/deployed
git update-ref -d refs/remotes/origin/deployed
( cd "$T/other" && git push --quiet --delete origin deployed )

# =============================================================================
case_hdr "Case 1, two consecutive plain releases on the shared ref"
# =============================================================================
# The first advance of the replay runs against an ABSENT record ref, so it also
# measures the expect-absence lease.
set +e
"$RECORD_REF" check deployed dev >"$T/c1a.out" 2>"$T/c1a.err"; c1a=$?
set -e
check "check #1 exit" "$c1a" "0"
[[ -s "$T/c1a.out" ]] && ok "check #1 printed a payload base ($(cat "$T/c1a.out"))" || bad "check #1 printed no payload base"
set +e
"$RECORD_REF" advance deployed dev >"$T/c1b.out" 2>"$T/c1b.err"; c1b=$?
set -e
check "advance #1 exit (expect-absence lease)" "$c1b" "0"
check "advance #1 local == origin" \
  "$(git rev-parse refs/heads/deployed)" "$(git rev-parse refs/remotes/origin/deployed)"

commit_on "$T/other" "release two"
( cd "$T/other" && git push --quiet origin dev )
git fetch --quiet origin dev && git reset --quiet --hard origin/dev

set +e
"$RECORD_REF" check deployed dev >"$T/c1c.out" 2>"$T/c1c.err"; c1c=$?
set -e
check "check #2 exit (the run that dies today)" "$c1c" "0"
set +e
"$RECORD_REF" advance deployed dev >/dev/null 2>"$T/c1d.err"; c1d=$?
set -e
check "advance #2 exit" "$c1d" "0"
check "advance #2 local == origin" \
  "$(git rev-parse refs/heads/deployed)" "$(git rev-parse refs/remotes/origin/deployed)"

# =============================================================================
case_hdr "Case 2, a stale local copy warns and passes"
# =============================================================================
git update-ref refs/heads/deployed "$(git rev-parse HEAD~1)"
set +e
"$RECORD_REF" check deployed dev >/dev/null 2>"$T/c2.err"; c2=$?
set -e
check "check exit with a stale local copy" "$c2" "0"
grep -q "local copy" "$T/c2.err" && ok "the warning names the local copy and both shas" \
  || bad "no local-copy warning: $(cat "$T/c2.err")"
git update-ref refs/heads/deployed "$(git rev-parse refs/remotes/origin/deployed)"

# =============================================================================
case_hdr "Case 3, a DIVERGED remote record ref is refused (criterion 2)"
# =============================================================================
# A repository of its own, so $T/other keeps a clean tree for the later cases.
git init --quiet "$T/alien"
( cd "$T/alien" && echo unrelated > u.txt && git add u.txt \
  && git commit --quiet -m "unrelated root" && git branch -M unrelated \
  && git remote add origin "$T/origin.git" \
  && git push --quiet --force origin unrelated:refs/heads/deployed )
set +e
"$RECORD_REF" check deployed dev >"$T/c3.out" 2>"$T/c3.err"; c3=$?
set -e
[[ "$c3" -ne 0 ]] && ok "check refused a diverged record ref (exit $c3)" || bad "check accepted a diverged record ref"
[[ ! -s "$T/c3.out" ]] && ok "no payload base on stdout when it refuses" || bad "payload base leaked on a refusal"
grep -q "ancestor" "$T/c3.err" && ok "the message names the ancestor test" || bad "message: $(cat "$T/c3.err")"

# =============================================================================
case_hdr "Case 4, a record ref AHEAD of dev is refused (criterion 2)"
# =============================================================================
( cd "$T/other" && git fetch --quiet origin dev && git reset --quiet --hard origin/dev )
commit_on "$T/other" "ahead of dev, never pushed to dev"
( cd "$T/other" && git push --quiet --force origin HEAD:refs/heads/deployed )
set +e
"$RECORD_REF" check deployed dev >"$T/c4.out" 2>"$T/c4.err"; c4=$?
set -e
[[ "$c4" -ne 0 ]] && ok "check refused a record ref ahead of dev (exit $c4)" || bad "check accepted a record ref ahead of dev"
[[ ! -s "$T/c4.out" ]] && ok "no payload base on stdout when it refuses" || bad "payload base leaked on a refusal"

# =============================================================================
case_hdr "Case 5, an objective-scoped record ref is EXEMPT (criterion 3)"
# =============================================================================
# The second-RC shape in miniature: the record ref already exists on origin and
# its commit is NOT on dev.
( cd "$T/other" && git fetch --quiet origin dev && git checkout --quiet -B obj origin/dev )
commit_on "$T/other" "objective work, not on dev"
( cd "$T/other" && git push --quiet origin obj && git checkout --quiet dev )
git fetch --quiet origin "+refs/heads/obj:refs/remotes/origin/obj"
set +e
"$RECORD_REF" advance deployed-rc/obj-99 refs/remotes/origin/obj >/dev/null 2>"$T/c5a.err"; c5a=$?
set -e
check "RC one on a scoped ref: advance exit" "$c5a" "0"
set +e
"$RECORD_REF" check deployed-rc/obj-99 dev >"$T/c5b.out" 2>"$T/c5b.err"; c5b=$?
set -e
check "RC two on a scoped ref: check exit (not refused by ancestor-of-dev)" "$c5b" "0"
grep -q "objective-scoped" "$T/c5b.err" && ok "the exemption and its reason are printed" \
  || bad "no printed exemption: $(cat "$T/c5b.err")"
grep -q "253" "$T/c5b.err" && ok "the printed reason names RunOS item 253" || bad "item 253 not named"

# =============================================================================
case_hdr "Case 6, a runner whose fetch refspec does not cover the record ref (criterion 4)"
# =============================================================================
# Restore a sane shared `deployed`: an ancestor of dev, and BEHIND it.
#
# Behind matters. If origin/deployed already equalled dev the push would be
# "Everything up-to-date", git would never evaluate the lease, and the two
# controls below would pass for the wrong reason. Assert the gap explicitly so
# this case cannot silently degenerate into a no-op again.
( cd "$T/other" && git fetch --quiet origin dev \
  && git push --quiet --force origin "origin/dev~1:refs/heads/deployed" )
git clone --quiet --single-branch --branch dev "$T/origin.git" "$T/fresh"
cd "$T/fresh"
printf '  fetch refspec: %s\n' "$(git config --get-all remote.origin.fetch | tr '\n' ' ')"
c6_dev="$(git rev-parse dev)"
c6_rec="$(git ls-remote origin refs/heads/deployed | cut -f1)"
[[ "$c6_dev" != "$c6_rec" ]] && ok "the push under test really updates the ref (dev != deployed)" \
  || bad "dev == deployed, so the controls below would be a no-op, not a lease test"
set +e
git push --quiet --force-with-lease origin dev:refs/heads/deployed >/dev/null 2>"$T/c6a.err"; c6a=$?
set -e
[[ "$c6a" -ne 0 ]] && ok "control 1: a bare lease with NO fetch is rejected (exit $c6a)" \
  || bad "control 1: the bare lease was accepted; the explicit lease may be unnecessary"
git fetch --quiet origin "+refs/heads/deployed:refs/remotes/origin/deployed"
set +e
git push --quiet --force-with-lease origin dev:refs/heads/deployed >/dev/null 2>"$T/c6b.err"; c6b=$?
set -e
[[ "$c6b" -ne 0 ]] && ok "control 2: a bare lease AFTER fetching by hand is STILL rejected (exit $c6b)" \
  || bad "control 2: the bare lease worked after a manual fetch; re-check the plan's premise"
set +e
"$RECORD_REF" advance deployed dev >/dev/null 2>"$T/c6c.err"; c6c=$?
set -e
check "the EXPLICIT lease is accepted on that same clone" "$c6c" "0"
cd "$T/host"

# =============================================================================
case_hdr "Case 7, the lease still refuses a ref moved after the fetch"
# =============================================================================
git fetch --quiet origin "+refs/heads/deployed:refs/remotes/origin/deployed"
expected="$(git rev-parse -q --verify refs/remotes/origin/deployed)"
( cd "$T/other" && git checkout --quiet dev 2>/dev/null || true
  cd "$T/other" && git fetch --quiet origin dev && git reset --quiet --hard origin/dev )
commit_on "$T/other" "another actor clobbers the record ref"
( cd "$T/other" && git push --quiet --force origin HEAD:refs/heads/deployed )
set +e
git push --force-with-lease="refs/heads/deployed:$expected" origin dev:refs/heads/deployed \
  >/dev/null 2>"$T/c7.err"; c7=$?
set -e
[[ "$c7" -ne 0 ]] && ok "the explicit lease refused to discard a value it did not see (exit $c7)" \
  || bad "the explicit lease clobbered a ref that moved after the fetch"

# =============================================================================
case_hdr "Case 8, origin unreachable FAILS CLOSED"
# =============================================================================
git remote set-url origin "$T/does-not-exist.git"
set +e
"$RECORD_REF" check deployed dev >"$T/c8.out" 2>"$T/c8.err"; c8=$?
set -e
[[ "$c8" -ne 0 ]] && ok "check died when origin could not be queried (exit $c8)" \
  || bad "an unreachable origin was accepted; it must never read as a first release"
[[ ! -s "$T/c8.out" ]] && ok "no payload base on stdout" || bad "payload base leaked with origin unreachable"
grep -q "origin" "$T/c8.err" && ok "the message names origin" || bad "message: $(cat "$T/c8.err")"
grep -qi "first release" "$T/c8.err" && ok "the message says it refuses to read this as a first release" \
  || bad "the message does not distinguish absent from unreachable"
git remote set-url origin "$T/origin.git"

# =============================================================================
case_hdr "Case 9, the scripts/release.sh call shape"
# =============================================================================
# scripts/release.sh --check cannot run in a container without gh: it dies at
# its tool check long before any record-ref code. So the CALL SHAPE that block
# uses is exercised here instead, verbatim, because a caller that mishandles
# stdout or the exit code would undo everything above.
git remote set-url origin "$T/origin.git"
git fetch --quiet origin dev && git reset --quiet --hard origin/dev
( cd "$T/other" && git fetch --quiet origin dev \
  && git push --quiet --force origin origin/dev:refs/heads/deployed )

release_sh_shape() { # release_sh_shape <record> <integration>
  local payload_base
  payload_base="$("$RECORD_REF" check "$1" "$2")" || return 1
  [[ -n "$payload_base" ]] || return 2
  printf '%s\n' "$payload_base"
}

set +e
c9_base="$(release_sh_shape deployed dev)"; c9=$?
set -e
check "the release.sh shape succeeds and captures a payload base" "$c9" "0"
[[ -n "$c9_base" ]] && ok "PAYLOAD_BASE captured: $c9_base" || bad "PAYLOAD_BASE was empty"

# And it must FAIL when the check refuses, rather than continuing with an empty
# base. An unreachable origin is the cheapest refusal to stage.
git remote set-url origin "$T/does-not-exist.git"
set +e
c9b_base="$(release_sh_shape deployed dev)"; c9b=$?
set -e
[[ "$c9b" -ne 0 ]] && ok "the release.sh shape aborts when the check refuses (exit $c9b)" \
  || bad "the caller continued after a refused check"
[[ -z "$c9b_base" ]] && ok "no payload base was captured on the refusal" \
  || bad "captured '$c9b_base' from a refused check"
git remote set-url origin "$T/origin.git"

# =============================================================================
printf '\n=== replay summary: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
