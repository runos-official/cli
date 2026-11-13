#!/usr/bin/env bash
#
# Replay the main-line hotfix recipe (scripts/hotfix_lib.sh, hotfix_check.sh,
# hotfix_release.sh, .foreman/pipelines/hotfix-main.yml) in scratch repos.
#
# It needs no network, no gh and no go toolchain run: every case builds a bare repo as `origin`
# under mktemp -d and drives the REAL scripts by path. Each refusal is exercised, because a gate
# that is never seen to refuse is not known to work.
#
# Run it with:  make replay-hotfix
#
set -euo pipefail

R="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

PASS=0
FAIL=0
ok()   { printf '  PASS  %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '  FAIL  %s\n' "$1"; FAIL=$((FAIL + 1)); }
case_hdr() { printf '\n=== %s\n' "$1"; }
check() { if [[ "$2" == "$3" ]]; then ok "$1 ($2)"; else bad "$1: got '$2', want '$3'"; fi; }

export GIT_CONFIG_GLOBAL="$T/gitconfig"
export GIT_CONFIG_NOSYSTEM=1
git config --file "$GIT_CONFIG_GLOBAL" user.email replay@example.invalid
git config --file "$GIT_CONFIG_GLOBAL" user.name  "hotfix replay"
git config --file "$GIT_CONFIG_GLOBAL" init.defaultBranch main
git config --file "$GIT_CONFIG_GLOBAL" advice.detachedHead false

changelog() { # changelog <dir> <version...>: a CHANGELOG with these sections, newest first
  local dir="$1"; shift
  { echo "# Changelog"; echo; for v in "$@"; do printf '## %s\n\n- change\n\n' "$v"; done; } > "$dir/CHANGELOG.md"
}
commit_in() { ( cd "$1" && git add -A && git commit --quiet -m "$2" ); }

# fixture <name>: origin with main at M1 (tagged v1.0.0), dev at D1 beyond it, a deployed record at D1,
# a published hotfix branch H1 (one commit beyond main, CHANGELOG v1.0.1). Sets F, M1, D1, H1.
# The host is a clone left on a DETACHED HEAD at H1, the way the managed runner supplies it.
fixture() {
  F="$T/$1"; mkdir -p "$F"
  git init --quiet --bare "$F/origin.git"
  git init --quiet "$F/seed"
  ( cd "$F/seed" && git remote add origin "$F/origin.git" )
  changelog "$F/seed" v1.0.0
  echo base > "$F/seed/file.txt"
  commit_in "$F/seed" "base"
  ( cd "$F/seed" && git tag v1.0.0 && git push --quiet origin main v1.0.0 )
  M1="$(git -C "$F/seed" rev-parse HEAD)"
  ( cd "$F/seed" && git checkout --quiet -b dev && echo dev >> file.txt ) ; commit_in "$F/seed" "dev work"
  ( cd "$F/seed" && git push --quiet origin dev && git push --quiet origin dev:refs/heads/deployed )
  D1="$(git -C "$F/seed" rev-parse HEAD)"
  ( cd "$F/seed" && git checkout --quiet -b hotfix/fix "$M1" && echo fix >> file.txt )
  changelog "$F/seed" v1.0.1 v1.0.0
  commit_in "$F/seed" "the fix"
  ( cd "$F/seed" && git push --quiet origin hotfix/fix )
  H1="$(git -C "$F/seed" rev-parse HEAD)"
  git clone --quiet "$F/origin.git" "$F/host"
  ( cd "$F/host" && git checkout --quiet --detach "$H1" )
}
osha() { git -C "$F/origin.git" rev-parse -q --verify "$1" 2>/dev/null || echo absent; }
run_release() { ( cd "$F/host" && "$R/scripts/hotfix_release.sh" "$@" ) >"$F/out" 2>"$F/err"; }
refuses() { # refuses <label> <stderr pattern> <args...>
  local label="$1" pattern="$2"; shift 2
  set +e; run_release "$@"; local rc=$?; set -e
  if [[ "$rc" -ne 0 ]] && grep -q -- "$pattern" "$F/err"; then ok "$label"; else bad "$label (exit $rc): $(cat "$F/err")"; fi
}
refs_unmoved() { # refs_unmoved <label>: dev, deployed and main are where the fixture put them, and no tag was made
  check "$1: main untouched" "$(osha refs/heads/main)" "$M1"
  check "$1: dev untouched" "$(osha refs/heads/dev)" "$D1"
  check "$1: deployed untouched" "$(osha refs/heads/deployed)" "$D1"
  check "$1: no tag v1.0.1 on origin" "$(osha refs/tags/v1.0.1)" "absent"
}

printf 'main hotfix replay\ngit: %s\n' "$(git --version)"

case_hdr "Case 1, the dry run on a good hotfix stops before the tag"
fixture c1
run_release v1.0.1 hotfix/fix --dry-run; rc=$?
check "the dry run succeeds" "$rc" "0"
grep -q "dry run" "$F/err" && ok "it says it is a dry run" || bad "no dry-run notice: $(cat "$F/err")"
grep -q "$H1" "$F/err" && ok "it names the full source commit" || bad "source commit not named"
check "no local tag was made" "$(git -C "$F/host" tag --list v1.0.1)" ""
refs_unmoved "after the dry run"
check "HEAD is still the detached source" "$(git -C "$F/host" rev-parse HEAD)" "$H1"
check "still detached (no branch was checked out)" "$(git -C "$F/host" symbolic-ref -q HEAD || echo detached)" "detached"
( cd "$F/host" && "$R/scripts/hotfix_release.sh" v1.0.1 refs/heads/hotfix/fix --dry-run ) >/dev/null 2>&1 && ok "a full refs/heads/ ref is accepted" || bad "refs/heads/ form refused"

case_hdr "Case 1b, a long CHANGELOG (grep -q must not break a pipe under pipefail)"
fixture c1b
( cd "$F/seed" && git checkout --quiet hotfix/fix && { head -n 3 CHANGELOG.md; for i in $(seq 1 4000); do echo "- filler line $i of a long changelog to fill the pipe"; done; tail -n +4 CHANGELOG.md; } > c.tmp && mv c.tmp CHANGELOG.md \
  && git add -A && git commit --quiet -m "long changelog" && git push --quiet origin hotfix/fix )
H1="$(git -C "$F/seed" rev-parse HEAD)"; ( cd "$F/host" && git fetch --quiet origin && git checkout --quiet --detach "$H1" )
run_release v1.0.1 hotfix/fix --dry-run && ok "the section is found in a CHANGELOG longer than a pipe buffer" || bad "long CHANGELOG refused: $(cat "$F/err")"

case_hdr "Case 2, refusals"
fixture c2a; echo dirty >> "$F/host/file.txt"
refuses "a dirty tree is refused" "dirty" v1.0.1 hotfix/fix --dry-run
refs_unmoved "dirty"

fixture c2b   # main advanced past the branch point: the hotfix no longer contains main
( cd "$F/seed" && git checkout --quiet main && echo more >> file.txt ) ; commit_in "$F/seed" "main moved"
( cd "$F/seed" && git push --quiet origin main )
M1="$(git -C "$F/seed" rev-parse HEAD)"
refuses "main not an ancestor of HEAD is refused" "not an ancestor" v1.0.1 hotfix/fix --dry-run

fixture c2c
refuses "a version that is not the next patch (skipped) is refused" "next patch" v1.0.5 hotfix/fix --dry-run
refuses "a minor bump is refused" "next patch" v1.1.0 hotfix/fix --dry-run
refuses "a major bump is refused" "next patch" v2.0.0 hotfix/fix --dry-run
refuses "the version main already has is refused" "already exists" v1.0.0 hotfix/fix --dry-run
refuses "a candidate version is refused" "final" v1.0.1-rc.1 hotfix/fix --dry-run

fixture c2d
( cd "$F/host" && git tag v1.0.1 "$H1" )
refuses "a tag that exists locally is refused" "already exists" v1.0.1 hotfix/fix --dry-run
fixture c2e
( cd "$F/seed" && git tag v1.0.1 "$H1" && git push --quiet origin v1.0.1 )
M1_SAVE="$M1"
refuses "a tag that exists on origin is refused" "already exists" v1.0.1 hotfix/fix --dry-run

fixture c2f   # the source's CHANGELOG has no section for the version
( cd "$F/seed" && git checkout --quiet hotfix/fix && changelog . v1.0.0 && git add -A && git commit --quiet -m "drop section" && git push --quiet origin hotfix/fix )
H1="$(git -C "$F/seed" rev-parse HEAD)"; ( cd "$F/host" && git fetch --quiet origin && git checkout --quiet --detach "$H1" )
refuses "a CHANGELOG without the version's section is refused" "CHANGELOG" v1.0.1 hotfix/fix --dry-run

fixture c2g   # a branch is checked out and it is not the reviewed ref
( cd "$F/host" && git checkout --quiet -b some-other-branch "$H1" )
refuses "a checked-out branch that is not the reviewed ref is refused" "reviewed" v1.0.1 hotfix/fix --dry-run
( cd "$F/host" && git checkout --quiet -B hotfix/fix "$H1" )
run_release v1.0.1 hotfix/fix --dry-run && ok "the reviewed branch checked out is accepted" || bad "reviewed branch refused: $(cat "$F/err")"

fixture c2h   # the reviewed ref moved on origin after the source was supplied
( cd "$F/seed" && echo late >> file.txt && git add -A && git commit --quiet -m late && git push --quiet origin hotfix/fix )
refuses "a reviewed ref that moved on origin is refused" "moved" v1.0.1 hotfix/fix --dry-run

fixture c2i   # nothing beyond main
( cd "$F/host" && git checkout --quiet --detach "$M1" )
refuses "a HEAD equal to main is refused" "nothing to release" v1.0.1 --dry-run

fixture c2j
( cd "$F/host" && git checkout --quiet --detach "$H1" )
set +e; run_release v1.0.1 hotfix/fix; rc=$?; set -e
if [[ "$rc" -ne 0 ]] && ! grep -q "dry run" "$F/err"; then ok "a real run fails closed here (no gh, no network) before any tag"; else bad "real run did not stop"; fi
refs_unmoved "after the failed real run"
check "no local tag" "$(git -C "$F/host" tag --list v1.0.1)" ""

case_hdr "Case 3, the sensitivity scan"
fixture c3
# shellcheck disable=SC1091
source "$R/scripts/hotfix_lib.sh"
# A secret-shaped value built at run time, so this file holds no credential-shaped text itself.
FAKE_TOKEN="gh$(printf 'p')_$(printf 'a%.0s' $(seq 1 30))"
( cd "$F/host" && hotfix_sensitivity_scan "$M1" "$H1" ) >/dev/null 2>&1 && ok "a clean range passes" || bad "clean range refused"
( cd "$F/seed" && git checkout --quiet hotfix/fix && printf 'token = "%s"\n' "$FAKE_TOKEN" >> file.txt && git add -A && git commit --quiet -m secret && git push --quiet origin hotfix/fix )
H2="$(git -C "$F/seed" rev-parse HEAD)"; ( cd "$F/host" && git fetch --quiet origin )
set +e; ( cd "$F/host" && hotfix_sensitivity_scan "$M1" "$H2" ) >/dev/null 2>"$F/err"; rc=$?; set -e
[[ "$rc" -ne 0 ]] && ok "a secret-shaped line in main..source is refused" || bad "secret accepted"
# The floor must be the same patterns release.sh uses, or the two drift.
extract() { sed -n "/<<'PYFILTER'/,/^PYFILTER\$/p" "$1" | sed '1d;$d'; }
check "the scan patterns equal release.sh's" "$(extract "$R/scripts/hotfix_lib.sh" | shasum | cut -d' ' -f1)" "$(extract "$R/scripts/release.sh" | shasum | cut -d' ' -f1)"

case_hdr "Case 4, main is fast-forwarded and nothing else moves"
fixture c4
( cd "$F/host" && git tag v1.0.1 "$H1" && git push --quiet origin v1.0.1 )
( cd "$F/host" && hotfix_advance_main v1.0.1 "$H1" ) >"$F/out" 2>"$F/err"; rc=$?
check "the fast-forward succeeds" "$rc" "0"
check "main is at the source" "$(osha refs/heads/main)" "$H1"
check "dev is untouched" "$(osha refs/heads/dev)" "$D1"
check "deployed is untouched" "$(osha refs/heads/deployed)" "$D1"
grep -q "main_equals_final=true" "$F/out" && ok "main_equals_final is reported" || bad "no main_equals_final: $(cat "$F/out")"
( cd "$F/host" && hotfix_advance_main v1.0.1 "$H1" ) >/dev/null 2>&1 && ok "running it again is a no-op" || bad "second run failed"
fixture c4b   # main diverged: never forced
( cd "$F/host" && git tag v1.0.1 "$H1" && git push --quiet origin v1.0.1 )
( cd "$F/seed" && git checkout --quiet -b side "$M1" && echo side >> file.txt && git add -A && git commit --quiet -m side && git push --quiet --force origin side:refs/heads/main )
DIV="$(osha refs/heads/main)"
set +e; ( cd "$F/host" && hotfix_advance_main v1.0.1 "$H1" ) >"$F/out" 2>"$F/err"; rc=$?; set -e
[[ "$rc" -ne 0 ]] && ok "a diverged main is refused" || bad "diverged main accepted"
check "the diverged main was not moved" "$(osha refs/heads/main)" "$DIV"
fixture c4c   # the tag names a different commit than the source
( cd "$F/host" && git tag v1.0.1 "$M1" && git push --quiet origin v1.0.1 )
set +e; ( cd "$F/host" && hotfix_advance_main v1.0.1 "$H1" ) >"$F/out" 2>"$F/err"; rc=$?; set -e
[[ "$rc" -ne 0 ]] && grep -q "tag" "$F/err" && ok "a tag that does not name the source is refused" || bad "tag mismatch accepted"
check "main was not moved" "$(osha refs/heads/main)" "$M1"

case_hdr "Case 5, the pipeline recipe has the guide's shape"
Y="$R/.foreman/pipelines/hotfix-main.yml"
has() { grep -qE -- "$2" "$Y" && ok "$1" || bad "$1"; }
lacks() { grep -qE -- "$2" "$Y" && bad "$1" || ok "$1"; }
has "name: Main hotfix" '^name: Main hotfix$'
has "kind: github_release" '^kind: github_release$'
has "hotfix base_branch main" '^  base_branch: main$'
has "hotfix record_branch main" '^  record_branch: main$'
has "preserves_dev" '^  preserves_dev: true$'
has "advertises cli" '^  component: cli$'
has "advertises prod" '^  env: prod$'
has "the first step is the full checks" '^  - name: Full hotfix checks$'
has "the second step releases the reviewed patch" '^  - name: Release reviewed patch$'
lacks "no always flag" '^\s+always:'
lacks "target.branch is never the source" 'target\.branch'
lacks "no source_commit variable" 'source_commit'
has "the check step is a blocking slow gate" '^    slow: true$'
has "the check step does not install a local binary" '^    installs_local_binary: false$'

printf '\n=== hotfix replay summary: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
