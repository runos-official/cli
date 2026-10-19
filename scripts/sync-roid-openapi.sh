#!/bin/bash
# Copy the identity service's access API document into the CLI's test data,
# so internal/roidapi's contract test checks the client against the current
# routes. Usage: scripts/sync-roid-openapi.sh <path to the roid repo>
set -euo pipefail
src="${1:?usage: $0 <roid repo>}/docs/api/rbac.yaml"
dst="$(cd "$(dirname "$0")/.." && pwd)/internal/roidapi/testdata/rbac.yaml"
cp "$src" "$dst"
echo "copied $src to $dst; run: go test ./internal/roidapi/"
