#!/bin/bash
# Conductor MCP wrapper script
# This script proxies MCP requests to the Conductor backend server.
# Update CONDUCTOR_MCP_URL if the Conductor server location changes.

CONDUCTOR_MCP_URL="${CONDUCTOR_MCP_URL:-http://127.0.0.1:3026/mcp}"

exec curl -sS -N -X POST \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  --no-buffer \
  -d @- \
  "$CONDUCTOR_MCP_URL"
