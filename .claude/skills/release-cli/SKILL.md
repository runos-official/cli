---
name: release-cli
description: Cut (deploy) a RunOS CLI release. Use whenever the task is to release, deploy, ship, publish, or tag a new CLI version, including when foreman dispatches an agent to do so. The CLI ships as attested GitHub release artifacts, so a release IS the deployment. Covers the mandatory code-verification and public-repo sensitivity gates before the deterministic release runs.
---

# Releasing the RunOS CLI

A CLI release IS its deployment: the CLI ships as GitHub release artifacts that are build-provenance attested and then validated server-side by Templates. This skill is the **canonical entry point** for cutting one. Do not hand-run git/gh; the deterministic mechanism is [`scripts/release.sh`](../../../scripts/release.sh) (fronted by `make release`). This skill wraps it with the two judgment gates a machine cannot do: **code verification** and a **public-repo sensitivity audit**.

This is a **PUBLIC repo.** Never let real account/cluster/app IDs, OSIDs, org or customer names, internal hostnames, or any credential reach a release. The script enforces a deterministic secret-pattern floor; you enforce the rest by judgment below.

Scope: **CLI repo only.** The Templates R2 digest backfill + installer/k8s deploy is a separate repo with its own process (see foreman obj-50 for the full cross-repo go-live sequence). Do not attempt it from here.

## Procedure

Run these in order. Any failure stops the release.

### 1. Confirm version and CHANGELOG
- Decide the version `vX.Y.Z` (semver: features/security -> minor, fixes -> patch).
- Ensure `CHANGELOG.md` has a `## vX.Y.Z` section with accurate notes, **committed on `dev`**. CI uses that section as the GitHub release notes; the script refuses a version with no matching section.
- If you wrote the CHANGELOG this session, commit it on `dev` before proceeding.

### 2. Code verification (judgment gate)
Review the release payload, do not just trust tests:
- `git diff main..dev` (or `git log main..dev`) is exactly what ships. Read it.
- Confirm: the diff actually does what the CHANGELOG claims; the version bump matches the scope; nothing unintended is included; no debug/temp code.
- The script will also run `go build ./...`, `go vet ./...`, `go test ./...`, and `make local` as hard gates, but those are necessary, not sufficient. If the diff is large or risky, run `/code-review` on it first.
- If anything looks wrong, stop and fix on `dev` before releasing.

### 3. Sensitivity audit (judgment gate, PUBLIC repo)
Beyond the script's deterministic secret-pattern scan, reason over the payload for what patterns cannot catch:
- Real **account / cluster / app IDs, OSIDs** (opaque identifiers). Only placeholders (`myacct`, `mycluster`, `myapp`, `myosid`, `acme`) are allowed.
- **Org or customer names**, internal hostnames, private URLs, internal-only infra details.
- Identifiers that are *meant* to be public (the release-workflow OIDC identity URL, the public install domain, the public registry path) are fine; distinguish those from leaks by intent.
- Check both the diff and any new files (e.g. fixtures, test data, help text, comments).
- If anything sensitive is present, stop, remove it on `dev`, and re-verify. Note: prior commit history already had a real-ID purge; do not reintroduce.

### 4. Dry run (no side effects)
```
scripts/release.sh vX.Y.Z --check
```
Runs preflight (incl. the deterministic sensitivity floor) + all code gates, then stops before any tag/push. Must pass clean.

### 5. Release
```
scripts/release.sh vX.Y.Z      # or: make release VERSION=vX.Y.Z
```
The script then, deterministically: re-runs preflight + gates -> fast-forwards `main` to `dev` -> tags -> pushes `main` + tag + `dev` -> waits for the Release workflow to succeed -> verifies the published asset's attestation is bound to `release.yml @ refs/tags/vX.Y.Z`. It aborts before tagging on any gate failure.

### 6. Report
- State the released version, the GitHub release URL, and that the attestation verified.
- Remind that this is **CLI go-live step 1 only**: the Templates R2 backfill + installer deploy (separate repo) is still required for the end-to-end install-verification guarantee, and sequencing there is load-bearing (deploying the new installer before the digests are backfilled bricks installs).

## If a gate fails
- **Sensitivity floor (script) or audit (you):** remove the content on `dev`, re-run from step 2. If the script's deterministic scan is a confirmed false positive, narrow the pattern in `scripts/release.sh` rather than skipping the gate.
- **Code gate (build/vet/test):** fix on `dev`, re-run.
- **Not fast-forwardable / not synced:** histories diverged or a branch is behind origin; reconcile manually, never force.
- **CI run failed:** the tag is already pushed; inspect `gh run view <id> --log-failed`, fix forward with a new patch version. Do not delete published tags.
