#!/usr/bin/env bash
# Build the DataHub MCP server image and load it into the kind node. The image
# installs the published mcp-server-datahub package from PyPI at build time —
# this needs network, once, like a docker pull. After `kind load`, the image is
# cached in the node and cluster bring-up stays offline.
#
# Set DATAHUB_MCP_VERSION to pin the package (e.g. 1.2.3) for a reproducible
# build; leave it empty for the latest release.
. "$(dirname "$0")/lib.sh"

REPO="${DATAHUB_MCP_IMAGE_REPO:-chd}"
TAG="${DATAHUB_MCP_IMAGE_TAG:-local}"
CTX="$DEPLOY_DIR/datahub/mcp"
SPEC="mcp-server-datahub${DATAHUB_MCP_VERSION:+==$DATAHUB_MCP_VERSION}"

log "building image $REPO/datahub-mcp:$TAG from $SPEC (pulls Python deps once — can take a few minutes)"
docker build \
  --build-arg "MCP_SERVER_DATAHUB_SPEC=$SPEC" \
  -f "$CTX/Dockerfile" -t "$REPO/datahub-mcp:$TAG" "$CTX"

log "loading image into kind node ($CLUSTER_NAME)"
kind load docker-image "$REPO/datahub-mcp:$TAG" --name "$CLUSTER_NAME"

log "datahub-mcp image ready: $REPO/datahub-mcp:$TAG"
