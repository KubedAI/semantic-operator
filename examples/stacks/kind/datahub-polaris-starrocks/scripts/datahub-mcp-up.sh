#!/usr/bin/env bash
# Deploy the DataHub MCP server into the cluster and wait for it to be ready.
# Requires DataHub (GMS) already up (make datahub-up) and the image built
# (make datahub-mcp-build).
. "$(dirname "$0")/lib.sh"

log "deploying DataHub MCP server (namespace datahub)"
kubectl apply -f "$DEPLOY_DIR/datahub/mcp/deployment.yaml"

log "waiting for datahub-mcp rollout"
kubectl -n datahub rollout status deploy/datahub-mcp --timeout=120s

log "DataHub MCP ready — host agent endpoint http://localhost:8091/mcp"
log "next: make agent"
