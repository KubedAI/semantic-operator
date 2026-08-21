#!/usr/bin/env bash
# Build the operator (manager) + server images for the local
# cluster and load them into the kind node. Fully offline: the static binaries
# are compiled from the repo root with CGO disabled (host module cache), copied
# into the cached distroless base, then `kind load`ed. The chart references
# ${REPO}/manager and ${REPO}/server with imagePullPolicy=IfNotPresent.
. "$(dirname "$0")/lib.sh"

[ -f "$ROOT_DIR/go.mod" ] || die "repo root (go.mod) not found at $ROOT_DIR"

REPO="${OPERATOR_IMAGE_REPO:-account-demo}"
TAG="${OPERATOR_IMAGE_TAG:-local}"
BUILD_DIR="$DATA_DIR/operator-build"
mkdir -p "$BUILD_DIR"

log "compiling manager + server (CGO off, linux/amd64)"
( cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o "$BUILD_DIR/manager" ./cmd/manager )
( cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=$TAG" -o "$BUILD_DIR/server" ./cmd/server )

cp "$DEPLOY_DIR/operator/Dockerfile" "$BUILD_DIR/Dockerfile"
log "building images $REPO/manager:$TAG and $REPO/server:$TAG"
docker build -f "$BUILD_DIR/Dockerfile" --target manager -t "$REPO/manager:$TAG" "$BUILD_DIR"
docker build -f "$BUILD_DIR/Dockerfile" --target server  -t "$REPO/server:$TAG"  "$BUILD_DIR"

log "loading images into kind node ($CLUSTER_NAME)"
kind load docker-image "$REPO/manager:$TAG" "$REPO/server:$TAG" --name "$CLUSTER_NAME"

log "operator images ready: $REPO/manager:$TAG, $REPO/server:$TAG"
