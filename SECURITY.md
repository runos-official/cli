# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in any RunOS product or service, including the CLI, API, web console, or infrastructure, please report it responsibly.

**Email:** security@runos.com

Please include:
- A description of the vulnerability
- Steps to reproduce
- Potential impact

We will acknowledge reports within 8 hours and aim to provide a fix or mitigation plan within 3 days for critical issues.

## Security Practices

- **No hardcoded secrets.** Credentials are loaded at runtime from user config (`~/.runos/config.json`) or environment variables.
- **Strict file permissions.** Config and cache files are created with `0600` (owner read/write only). Directories use `0700`.
- **Binary verification.** Self-updates verify SHA256 checksums against the release manifest before installing.
- **Short-lived tokens.** Authentication uses short-lived Firebase ID tokens refreshed from a locally stored refresh token. Tokens are never logged.
- **HTTPS only.** All API communication uses HTTPS.

## Release Integrity & Trust Model

The `runos` binary a user installs is cryptographically tied to the artifact this
repository's release pipeline produced. The chain has three parts:

1. **Keyless build-provenance attestation (this repo).** The release workflow
   (`.github/workflows/release.yml`) runs `actions/attest-build-provenance`, which
   issues a keyless Sigstore attestation for every released archive
   (`runos-{os}-{arch}.{tar.gz,zip}`). Each attestation binds the archive's sha256
   to this workflow's OIDC identity,
   `https://github.com/runos-official/cli/.github/workflows/release.yml@<ref>`. No
   long-lived signing secret is stored, which is safe for a public repo. Verify a
   release manually with `gh attestation verify <archive> --repo runos-official/cli`.

2. **Server-side validator + digest registry (Templates app + Cloudflare R2).** A
   release webhook drives the Templates service to verify each archive's attestation
   against the identity above, compute its sha256, and publish a per-version digest
   to a dedicated Cloudflare R2 registry at
   `cli/validated/{tag}/runos-{os}-{arch}.sha256` (raw hex body). A mutable
   `cli/validated/current.json` pointer is flipped last, only after every platform
   digest for the tag exists. Per-version digests are write-once; a conflicting
   re-write is blocked and alerted.

3. **Fail-closed installers (curl | bash and PowerShell).** The installer resolves
   the version, downloads the archive by **exact tag** (never `latest/download`),
   fetches the validated digest over plain HTTPS, recomputes the sha256 locally, and
   compares. A match installs; a mismatch, missing digest, or pinned-but-unvalidated
   version aborts with nothing installed. The client needs no Sigstore or `gh`
   tooling, only a sha256 compare. The self-updater (`runos update`) independently
   verifies the archive against the release's `checksums.txt`.

Together these defend against **post-build tampering of the distributed artifact**
(replace-asset and swap-checksum attacks): a swapped release asset no longer matches
the validated digest and fails closed.

### What attestation does NOT guarantee (accepted limitation)

Attestation proves an artifact *came from this workflow*. It does **not** prove the
artifact is benign. A compromised **build**, malicious code merged into the release
branch, or a subverted workflow/runner, would produce an evil-but-validly-attested
binary that passes every downstream check above. The attestation and the digest
registry are blind to this class of attack.

The **only** defense against a subverted build is repo-side access control: keeping
malicious code and workflow edits out in the first place. The two layers below are
that defense.

### Codified in-repo: pinned action SHAs

Every `uses:` in `release.yml` and `ci.yml` is pinned to a full 40-character commit
SHA with a trailing version comment (for example
`actions/attest-build-provenance@a2bbfa25375fe432b6a289bc6b6cd05ecd0c4c32 # v4.1.0`).
A moved tag on a third-party action therefore cannot silently swap in new code.
**Re-confirm this on every workflow edit**, and pin any newly added action the same
way before merging.

### Admin-only hardening checklist (human/admin action required)

These controls cannot be enforced by files in this repo. A GitHub org/repo admin
must apply them in repository settings; they are the actual mitigation for the
build-compromise limitation above:

- [ ] **Branch protection** on the release branch: require pull requests, block
  direct pushes and force-pushes.
- [ ] **Required PR review**: at least one approving review before merge.
- [ ] **Restrict workflow edits**: limit who can modify files under
  `.github/workflows/` and tighten the repo's Actions permissions.
- [ ] **Restrict tag push and release publishing**: the release workflow triggers on
  `v*` tag pushes, so control over who can push tags and publish releases is control
  over what gets attested and shipped.

Immutable Releases is already enabled on the repo (assets cannot be replaced after a
release is published; see the note in `release.yml`), which closes the
swap-asset-after-publish window.

For the full cross-repo trust model, including the Templates validator, the R2
registry immutability/alarm design, and the client-report ops-canary limitation
(spoofable and blind to binaries that never reach a reporting client; the periodic
server-side re-validation detector is deferred to a future objective), see the RunOS
handbook trust-model article.

## MCP Server Permissions

The CLI includes an MCP server for AI assistant integration with four permission levels:

| Level | Risk | Description |
|---|---|---|
| `read` | Low | Query-only access to clusters, services, and apps |
| `sensitive-read` | High | Returns credentials, connection strings, and secrets (visible to the AI model) |
| `write` | High | Create, update, and delete operations on live infrastructure |
| `sensitive-write` | Critical | Credential rotation and secret management |

Each level must be explicitly configured. Use the minimum level required for your workflow.
