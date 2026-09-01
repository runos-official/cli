# Makefile for RunOS CLI

# Color codes for output
GREEN := \033[0;32m
RED := \033[0;31m
BLUE := \033[0;34m
CYAN := \033[0;36m
GRAY := \033[0;90m
NC := \033[0m

# Extract version from latest git tag (falls back to "dev")
VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "dev")

# Default target
.DEFAULT_GOAL := help

# ============================================================================
# Development
# ============================================================================

.PHONY: local
local: clean
	$(eval BUILD_STAMP := dev-$(shell date -u +%Y-%m-%dT%H:%M:%SZ))
	@go build -ldflags="-X github.com/runos-official/cli/version.Version=$(BUILD_STAMP)" -o runos .
	@mkdir -p ~/.local/bin
	@rm -f ~/.local/bin/runos
	@cp runos ~/.local/bin/
	@xattr -c ~/.local/bin/runos 2>/dev/null || true
	@echo "$(GREEN)Installed runos $(BUILD_STAMP) to ~/.local/bin/runos$(NC)"

.PHONY: build
build:
	@go build -ldflags="-X github.com/runos-official/cli/version.Version=$(VERSION)" -o runos .

.PHONY: test
test:
	@echo "$(GRAY)[`date '+%H:%M:%S'`]$(NC) $(BLUE)Running tests...$(NC)"
	@go test ./...
	@echo "$(GRAY)[`date '+%H:%M:%S'`]$(NC) $(GREEN)Tests passed$(NC)"

.PHONY: clean
clean:
	@rm -f runos

# ============================================================================
# Leak gate (PUBLIC repo)
# ============================================================================

# Install the tracked git hooks for this clone. .git/hooks is not tracked, so
# every clone must do this once. Run it right after you clone.
.PHONY: hooks
hooks:
	@git config core.hooksPath .githooks
	@echo "$(GREEN)core.hooksPath = .githooks$(NC) (pre-commit now runs leakcheck on the staged diff)"

# Scan every tracked file for credentials and un-baselined internal identifiers.
.PHONY: leakcheck
leakcheck:
	@python3 scripts/leakcheck.py

# Scan only the staged diff, the same way the pre-commit hook does.
.PHONY: leakcheck-staged
leakcheck-staged:
	@python3 scripts/leakcheck.py --staged

# Ratchet the baseline down after you REMOVE an identifier from the source.
# Never run this to get a new identifier past the gate.
.PHONY: leakcheck-update
leakcheck-update:
	@python3 scripts/leakcheck.py --update

# Test the checker itself: what it must catch and what it must not.
.PHONY: leakcheck-test
leakcheck-test:
	@python3 scripts/leakcheck_test.py

# Fail on a tracked file leakcheck cannot READ. leakcheck reads UTF-8 text; a
# file holding a NUL byte or other bytes is read best effort at most, and some
# encodings escape it entirely (measured on checker 1.2.0: a token in a UTF-32
# file, and a token broken up by NUL bytes, both pass with exit 0). This target
# turns such a file into a decision somebody records, not a silent gap.
.PHONY: unscannable
unscannable:
	@python3 scripts/unscannable_check.py

# ============================================================================
# Release
# ============================================================================

# Cut a release: runs every gate (build/vet/test/make local), fast-forwards
# main to dev, tags, pushes, watches CI, and verifies the attestation.
# Usage: make release VERSION=v1.7.0   (add CHECK=1 to run gates only, no push)
.PHONY: release
release:
	@test -n "$(VERSION)" || { echo "$(RED)set VERSION, e.g. make release VERSION=v1.7.0$(NC)"; exit 1; }
	@scripts/release.sh $(VERSION) $(if $(CHECK),--check,)

.PHONY: version
version:
	@echo "$(VERSION)"

# ============================================================================
# Help
# ============================================================================

.PHONY: help
help:
	@echo "$(CYAN)RunOS CLI$(NC)"
	@echo ""
	@echo "  make local    Build and install to ~/.local/bin"
	@echo "  make build    Build binary for current platform"
	@echo "  make test     Run tests"
	@echo "  make version  Show current version"
	@echo "  make clean    Remove build artifacts"
	@echo ""
	@echo "  make hooks            Install the tracked git hooks (run once per clone)"
	@echo "  make leakcheck        Scan every tracked file for leaks (PUBLIC repo gate)"
	@echo "  make leakcheck-staged Scan only the staged diff"
	@echo "  make leakcheck-update Ratchet the baseline down after removing an identifier"
	@echo "  make leakcheck-test   Test the leak checker itself"
	@echo "  make unscannable      Fail on a tracked file leakcheck cannot read"
	@echo ""
	@echo "  make release VERSION=vX.Y.Z         Cut a release (gates, tag, push, verify)"
	@echo "  make release VERSION=vX.Y.Z CHECK=1 Run release gates only, no tag/push"
	@echo ""
	@echo "Release runs scripts/release.sh: build/vet/test/make local, fast-forward"
	@echo "main to dev, tag, push, watch CI, and verify the build-provenance attestation."
