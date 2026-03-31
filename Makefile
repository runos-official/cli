# Makefile for RunOS CLI

# Color codes for output
GREEN := \033[0;32m
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
local: clean build
	@mkdir -p ~/.local/bin
	@rm -f ~/.local/bin/runos
	@cp runos ~/.local/bin/
	@xattr -c ~/.local/bin/runos 2>/dev/null || true
	@echo "$(GREEN)Installed runos $(VERSION) to ~/.local/bin/runos$(NC)"

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
	@echo "Cross-platform builds and releases are handled by GitHub Actions."
	@echo "To release: git tag v$(VERSION) && git push origin v$(VERSION)"
