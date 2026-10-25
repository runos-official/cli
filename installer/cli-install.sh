#!/bin/bash
#
# RunOS CLI installer.
#
# This file is STATIC. The RunOS API serves it unchanged at /cli.sh, the same file for everyone.
#
#   curl -fsSL https://api.runos.com/cli.sh | bash
#   curl -fsSL https://api.runos.com/cli.sh | bash -s -- --release v1.2.3
#   curl -fsSL https://api.runos.com/cli.sh | bash -s -- --install-dir "$HOME/bin"
#
# RUNOS_VERSION and INSTALL_DIR in the environment do the same as --release and --install-dir.
#
# Every argument is checked with shell builtins before any command runs. The installer
# downloads the exact release archive from GitHub Releases and installs it only when its sha256
# matches the digest in the RunOS validated registry, which is written only after the release's
# build-provenance attestation is verified. When the GitHub CLI is present it also verifies the
# attestation signature itself.
#
# The source of truth for this file is the CLI repository (installer/cli-install.sh). The RunOS
# API ships a copy pinned at its build, and a test fails when the copy and this file differ.
set -e
set -o pipefail

# Character classes such as [0-9] must mean ASCII only, whatever the caller's locale is.
export LC_ALL=C

refuse() {
    # Never echo the refused value: it can carry terminal escape sequences.
    printf 'RunOS CLI installer: %s\n' "$1" >&2
    printf 'Nothing was installed.\n' >&2
    exit 2
}

RELEASE="${RUNOS_VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-}"

while [ "$#" -gt 0 ]; do
    case "$1" in
        --release|--install-dir)
            [ "$#" -ge 2 ] || refuse "the argument $1 needs a value"
            opt="$1"
            val="$2"
            shift 2
            ;;
        --release=*|--install-dir=*)
            opt="${1%%=*}"
            val="${1#*=}"
            shift
            ;;
        *)
            refuse "unknown argument (this installer takes only --release and --install-dir)"
            ;;
    esac
    case "$opt" in
        --release) RELEASE="$val" ;;
        --install-dir) INSTALL_DIR="$val" ;;
    esac
done

# A release tag: v, then MAJOR.MINOR.PATCH, then an optional pre-release suffix.
TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
# An absolute path of printable characters.
DIR_RE='^/[[:print:]]*$'

if [ -n "$RELEASE" ]; then
    case "$RELEASE" in
        v*) ;;
        *) RELEASE="v$RELEASE" ;;
    esac
    [[ "$RELEASE" =~ $TAG_RE ]] || refuse "the release is malformed (expected a release tag such as v1.2.3)"
fi
if [ -n "$INSTALL_DIR" ]; then
    [[ "$INSTALL_DIR" =~ $DIR_RE ]] || refuse "the install directory must be an absolute path"
fi

# --- From here on every input has a known, safe shape ------------------------------------------

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

echo -e "${GREEN}RunOS CLI Installer${NC}"
echo ""

# The validated digest registry (public read). Only the release validator writes it.
REGISTRY_BASE="https://runoscdn.com/cli/validated"

# The identity the release attestation must carry: the CLI repository's release workflow,
# signed through GitHub's OIDC issuer. The release validator enforces the same identity.
ATTEST_REPO="runos-official/cli"
ATTEST_SAN_REGEX='^https://github\.com/runos-official/cli/\.github/workflows/[^@]+@.+$'
ATTEST_OIDC_ISSUER="https://token.actions.githubusercontent.com"

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    arm64)   ARCH="arm64" ;;
    *)       echo -e "${RED}Unsupported architecture: $ARCH${NC}"; exit 1 ;;
esac

case "$OS" in
    darwin) PLATFORM="darwin" ;;
    linux)  PLATFORM="linux" ;;
    *)      echo -e "${RED}Unsupported OS: $OS${NC}"; exit 1 ;;
esac

# abort fails closed: prints a clear reason and exits without installing anything.
abort() {
    echo ""
    echo -e "${RED}Installation aborted: $1${NC}" >&2
    exit 1
}

# sha256_of computes the lowercase hex sha256 of a file using whatever tool is
# available, with no external dependency beyond coreutils.
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        echo ""
    fi
}

# Resolve the version (tag) to install: the --release pin, else the validated
# current.json pointer. A pin that has no validated digest yet fails closed
# with "version pending verification".
PINNED=""
if [ -n "$RELEASE" ]; then
    TAG="$RELEASE"
    PINNED="1"
else
    CURRENT_JSON=$(curl -fsSL "${REGISTRY_BASE}/current.json" 2>/dev/null || true)
    if [ -z "$CURRENT_JSON" ]; then
        abort "could not resolve the current validated version"
    fi
    TAG=$(printf '%s' "$CURRENT_JSON" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
    if [ -z "$TAG" ]; then
        abort "could not parse the current validated version"
    fi
    [[ "$TAG" =~ $TAG_RE ]] || abort "the registry named a malformed version"
fi

# Fetch the validated digest for this exact tag + platform.
DIGEST_URL="${REGISTRY_BASE}/${TAG}/runos-${PLATFORM}-${ARCH}.sha256"
EXPECTED=$(curl -fsSL "$DIGEST_URL" 2>/dev/null | tr -d '[:space:]' || true)
EXPECTED=$(printf '%s' "$EXPECTED" | tr '[:upper:]' '[:lower:]')
if [ -z "$EXPECTED" ]; then
    if [ -n "$PINNED" ]; then
        abort "version ${TAG} is pending verification (no validated digest yet)"
    fi
    abort "verification failed: no validated digest for ${TAG} (${PLATFORM}-${ARCH})"
fi
[[ "$EXPECTED" =~ ^[0-9a-f]{64}$ ]] || abort "verification failed: the registry digest for ${TAG} is malformed"

# Determine install directory
# Priority: explicit --install-dir / INSTALL_DIR > root > CI with writable /usr/local/bin > ~/.local/bin
if [ -n "$INSTALL_DIR" ]; then
    mkdir -p "$INSTALL_DIR"
elif [ "$EUID" -eq 0 ]; then
    INSTALL_DIR="/usr/local/bin"
elif [ -n "$CI" ] && [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

# Download the archive by EXACT tag into a temp dir (never piped into tar).
ARCHIVE_URL="https://github.com/runos-official/cli/releases/download/${TAG}/runos-${PLATFORM}-${ARCH}.tar.gz"
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/runos-install.XXXXXX")
trap 'rm -rf "$TMP_DIR"' EXIT
ARCHIVE_PATH="${TMP_DIR}/runos.tar.gz"

echo -e "${YELLOW}Downloading RunOS CLI ${TAG} from GitHub...${NC}"
echo -e "  Source: ${ARCHIVE_URL}"
echo -e "  View source code: https://github.com/runos-official/cli"
echo ""
if ! curl -fsSL -o "$ARCHIVE_PATH" "$ARCHIVE_URL"; then
    abort "could not download the release archive for ${TAG}"
fi

# Verify the download against the validated digest. Fail closed.
ACTUAL=$(sha256_of "$ARCHIVE_PATH" | tr '[:upper:]' '[:lower:]')
if [ -z "$ACTUAL" ]; then
    abort "no sha256 tool available (need sha256sum or shasum)"
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
    echo -e "  ${YELLOW}expected:${NC} ${EXPECTED}"
    echo -e "  ${YELLOW}computed:${NC} ${ACTUAL}"
    abort "verification failed: archive digest does not match the validated digest"
fi
echo -e "${GREEN}Verified build provenance (sha256 matches the validated registry).${NC}"

# --- Layer 2: signature-level attestation check (optional, defense-in-depth) ---
# When the GitHub CLI is present and able, verify the downloaded ARCHIVE against
# its build-provenance attestation, pinned to the same release-workflow identity
# and OIDC issuer the release validator enforces. ABORT only on a genuine FAIL
# verdict. Every "could not reach a verdict" case (gh absent, not authenticated,
# too old, offline, API error, rate limit) degrades to a warning, because the
# digest floor above already pinned this exact artifact.
warn_attestation() {
    echo ""
    echo -e "${YELLOW}Signature verification skipped: $1${NC}" >&2
    echo -e "${YELLOW}  This is the optional signature-level check. Your install is still digest-verified${NC}" >&2
    echo -e "${YELLOW}  (sha256 matched the validated registry). For best security, verify by signature,${NC}" >&2
    echo -e "${YELLOW}  not just by SHA: install + authenticate a recent GitHub CLI${NC}" >&2
    echo -e "${YELLOW}  (gh >= 2.49, then 'gh auth login'), then re-run this installer.${NC}" >&2
}

if ! command -v gh >/dev/null 2>&1; then
    warn_attestation "the GitHub CLI ('gh') is not installed"
elif ! gh attestation verify --help >/dev/null 2>&1; then
    warn_attestation "this 'gh' is too old for 'attestation verify' (need gh >= 2.49)"
else
    ATTEST_OUT=$(gh attestation verify "$ARCHIVE_PATH" \
        --repo "$ATTEST_REPO" \
        --cert-identity-regex "$ATTEST_SAN_REGEX" \
        --cert-oidc-issuer "$ATTEST_OIDC_ISSUER" 2>&1) && ATTEST_RC=0 || ATTEST_RC=$?

    if [ "$ATTEST_RC" -eq 0 ]; then
        echo -e "${GREEN}Verified build-provenance attestation signature (gh attestation verify).${NC}"
    elif printf '%s' "$ATTEST_OUT" | grep -qiE 'verifying with issuer|expected issuer|verification failed|failed to verify|does not match|signature.*(invalid|fail)'; then
        echo "$ATTEST_OUT" >&2
        abort "signature verification failed: the archive does not match a valid build-provenance attestation for the expected release workflow"
    else
        warn_attestation "gh could not complete attestation verification ($(printf '%s' "$ATTEST_OUT" | grep -v '^$' | head -n1))"
    fi
fi

# Verified: extract + install.
tar -xzf "$ARCHIVE_PATH" -C "$INSTALL_DIR" runos
chmod +x "$INSTALL_DIR/runos"

# Clear quarantine on macOS
if [ "$PLATFORM" = "darwin" ]; then
    xattr -c "$INSTALL_DIR/runos" 2>/dev/null || true
fi

echo -e "${GREEN}Installed to $INSTALL_DIR/runos${NC}"

# Check PATH
if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
    if [ -n "$GITHUB_PATH" ]; then
        # GitHub Actions: persist PATH for subsequent steps
        echo "$INSTALL_DIR" >> "$GITHUB_PATH"
        echo -e "${GREEN}Added $INSTALL_DIR to \$GITHUB_PATH for subsequent steps.${NC}"
    elif [ -n "$GITHUB_ACTIONS" ]; then
        echo ""
        echo -e "${YELLOW}Note: $INSTALL_DIR is not in PATH and \$GITHUB_PATH is unset.${NC}"
        echo "Add this to a step that runs before any 'runos' invocation:"
        echo "  echo \"$INSTALL_DIR\" >> \"\$GITHUB_PATH\""
    elif [ -n "$GITLAB_CI" ]; then
        echo ""
        echo -e "${YELLOW}Note: $INSTALL_DIR is not in your PATH.${NC}"
        echo "Add this to your job's before_script so later steps can find runos:"
        echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    elif [ -n "$CI" ]; then
        echo ""
        echo -e "${YELLOW}Note: $INSTALL_DIR is not in your PATH.${NC}"
        echo "Export it before invoking runos in this job:"
        echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    else
        echo ""
        echo -e "${YELLOW}Note: $INSTALL_DIR is not in your PATH.${NC}"
        echo "Add it by running:"
        if [ -f "$HOME/.zshrc" ]; then
            echo "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc && source ~/.zshrc"
        else
            echo "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.bashrc && source ~/.bashrc"
        fi
    fi
fi

# Show success
RUNOS_BIN=""
if [ -x "$INSTALL_DIR/runos" ]; then
    RUNOS_BIN="$INSTALL_DIR/runos"
fi
if [ -n "$RUNOS_BIN" ]; then
    VERSION=$("$RUNOS_BIN" version 2>/dev/null || echo "unknown")

    echo ""
    echo -e "${GREEN}════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  RunOS CLI Installation Complete${NC}"
    echo -e "${GREEN}════════════════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${YELLOW}Version:${NC}     $VERSION"
    echo ""
    echo -e "  ${YELLOW}Next steps:${NC}"
    echo -e "    1. Run ${GREEN}runos login${NC} to sign in"
    echo -e "    2. Run ${GREEN}runos mcp configure claude${NC} in your project directory"
    echo ""
fi
