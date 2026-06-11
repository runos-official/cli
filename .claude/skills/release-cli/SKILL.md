---
name: release-cli
description: Deploy (release) the RunOS CLI. Use whenever the task is to release, deploy, ship, publish, or tag a new CLI version, including when foreman dispatches an agent to do so. The CLI ships as attested GitHub release artifacts, so a release IS the deployment. Covers the mandatory sanity-check / unit-test, code-verification, and public-repo sensitivity gates before the deterministic deploy runs, and the deployed-branch bookkeeping after.
---

# Deploying the RunOS CLI

A CLI deploy IS a release: the CLI ships as GitHub release artifacts that are build-provenance attested and then validated server-side by Templates (release webhook -> attestation verify -> R2 digest registry -> fail-closed installer). This skill is the **canonical entry point** for deploying. Do not hand-run git/gh; the deterministic mechanism is [`scripts/release.sh`](../../../scripts/release.sh) (fronted by `make release`). This skill wraps it with the judgment gates a machine cannot do: **sanity check + code verification** and a **public-repo sensitivity audit**.

## Branch model (important)

- **`dev`** — local development; all work lands here. This is the commit that gets tagged and deployed.
- **`deployed`** — the record of what has shipped. The deploy script fast-forwards it to the shipped commit **only after a successful deploy** (CI green + attestation verified). Created on first deploy.
- **`main`** — **human-controlled; tooling NEVER touches it.** The human merges `dev`/`deployed` into `main` themselves once they have personally verified the deploy. Do not fast-forward, merge, tag, or push `main` from this skill or the script.

So a deploy advances `dev` (tag + push) and `deployed` (on success); it leaves `main` exactly where it is.

## Guardrails

- This is a **PUBLIC repo.** Never let real account/cluster/app IDs, OSIDs, org or customer names, internal hostnames, or any credential reach a release. The script enforces a deterministic secret-pattern floor; you enforce the rest by judgment (step 3).
- Scope: **CLI repo only.** The Templates R2 registry + installer/k8s deploy is a separate repo with its own deploy skill. Do not attempt it from here.

## Permission gates

When this runs unattended in Claude Code auto mode, a safety classifier may deny a command it considers risky (tag pushes and release publishing are typical). A denial is not a failure of the deploy and not a license to find another route. Call the foreman MCP tool `request_approval` with a precise one-line `action` (exactly the command you will run: target, version) and the command + reason as `detail`, then poll `approval_get` with the returned id every 30-60 seconds. On `approved`, the returned `human_decision` is the human operator's explicit authorization for exactly that action: state it and re-run the command. If `denied`, or still pending after ~30 minutes, STOP and report the approval id.

## Procedure

Run these in order. Any failure stops the deploy.

### 1. Confirm version and CHANGELOG
- Decide the version `vX.Y.Z` from the diff since the last deploy: **patch (`Z`) for a bugfix, minor (`Y`) for new functionality** (features and security fixes are minor). This is the one judgment that drives every bump; pick it from what actually changed, not a default.
- Ensure `CHANGELOG.md` has a `## vX.Y.Z` section with accurate notes, **committed on `dev`**. CI uses that section as the GitHub release notes; the script refuses a version with no matching section.
- If you wrote the CHANGELOG this session, commit it on `dev` before proceeding.

### 2. Sanity check + code verification (judgment gate)
Review the deploy payload, do not just trust tests:
- `git diff deployed..dev` (or `git log deployed..dev`) is exactly what ships since the last deploy. Read it. (First-ever deploy: diff against the latest tag.)
- Confirm: the diff does what the CHANGELOG claims; the version bump matches the scope; nothing unintended is included; no debug/temp code.
- The script also runs `go build ./...`, `go vet ./...`, `go test ./...`, and `make local` as hard gates (the sanity check + unit tests), but those are necessary, not sufficient. If the diff is large or risky, run `/code-review` on it first.
- If anything looks wrong, stop and fix on `dev` before deploying.

### 3. Sensitivity audit (judgment gate, PUBLIC repo)
Beyond the script's deterministic secret-pattern scan, reason over the payload for what patterns cannot catch:
- Real **account / cluster / app IDs, OSIDs** (opaque identifiers). Only placeholders (`myacct`, `mycluster`, `myapp`, `myosid`, `acme`) are allowed.
- **Org or customer names**, internal hostnames, private URLs, internal-only infra details.
- Identifiers that are *meant* to be public (the release-workflow OIDC identity URL, the public install domain `get.runos.com`, the public registry `runoscdn.com/cli/validated`) are fine; distinguish those from leaks by intent.
- Check both the diff and any new files (fixtures, test data, help text, comments).
- If anything sensitive is present, stop, remove it on `dev`, and re-verify. Prior history already had a real-ID purge; do not reintroduce.

### 4. Dry run (no side effects)
```
scripts/release.sh vX.Y.Z --check     # or: make release VERSION=vX.Y.Z CHECK=1
```
Runs preflight (incl. the deterministic sensitivity floor) + all code gates, then stops before any tag/push. Must pass clean.

### 5. Deploy
```
scripts/release.sh vX.Y.Z      # or: make release VERSION=vX.Y.Z
```
The script then, deterministically: re-runs preflight + gates -> **tags the `dev` commit and pushes the tag + `dev`** (main untouched) -> waits for the Release workflow to succeed -> verifies the published asset's attestation is bound to `release.yml @ refs/tags/vX.Y.Z` -> **on success, fast-forwards `deployed` to the shipped commit and pushes it.** It aborts before tagging on any gate failure; `deployed` advances only when the deploy fully succeeds.

### 6. Advertise the new version in foreman (dev)
The CLI's advertised version lives in foreman as `CLI_VERSION`; conductor dev consumes it (foreman auto-syncs dev downstream), so set it here and a deploy never leaves a manual `/versions` step behind. Call the foreman MCP tool **`advertise_version`** with `component: "cli"`, `env: "dev"`, and `version:` the tag you just shipped (a leading `v` is stripped, so `vX.Y.Z` or `X.Y.Z` both work). It returns the stored entry; confirm its `value` matches. The deploy already succeeded by this point, so if foreman is unreachable do NOT fail the release: report it as "published but NOT advertised, advertise `CLI_VERSION` dev manually" so it can be fixed.

### 7. Report
- State the deployed version and the GitHub release URL; confirm the attestation verified and `deployed` advanced.
- Optionally confirm the live validation pipeline picked it up: webhook delivery `release`/`published` -> 202, and `https://runoscdn.com/cli/validated/current.json` flipped to the new tag (Templates side; it is live).
- Confirm `CLI_VERSION` dev now reads the shipped version in foreman (or flag it as not-advertised if step 6 could not reach foreman).
- Remind the human that **`main` is theirs to merge** once they have verified.

## If a gate fails
- **Sensitivity floor (script) or audit (you):** remove the content on `dev`, re-run from step 2. If the script's deterministic scan is a confirmed false positive, narrow the pattern in `scripts/release.sh` rather than skipping the gate.
- **Code gate (build/vet/test):** fix on `dev`, re-run.
- **`dev` not synced / `deployed` not fast-forwardable:** push/pull `dev`, or reconcile diverged `deployed` history manually. Never force-push to work around it.
- **CI run failed:** the tag is already pushed but `deployed` did NOT advance; inspect `gh run view <id> --log-failed`, fix forward with a new patch version. Do not delete published tags.
