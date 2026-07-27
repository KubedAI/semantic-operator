#!/usr/bin/env bash
# Load the pinned images from the local Docker cache into the kind node with
# 'kind load'. Run after the cluster exists and after 'make images-pull' has
# populated the Docker cache.
. "$(dirname "$0")/lib.sh"

kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME" || die "cluster '$CLUSTER_NAME' not found; run 'make cluster-up' first"

refs=()
while IFS= read -r _ref; do refs+=("$_ref"); done < <(image_refs)

# Docker Desktop's containerd image store keeps the full multi-arch index in
# the local cache but only the blobs for the host platform. `kind load
# docker-image` shells out to `ctr images import --all-platforms`, which then
# fails with "content digest ...: not found" on the other platform's manifest.
# Exporting a single-platform archive first works on both the classic and the
# containerd image store. Older Docker without `docker save --platform` falls
# back to the original path.
PLATFORM="linux/$(docker version --format '{{.Server.Arch}}')"
SAVE_PLATFORM=0
if docker save --help 2>&1 | grep -q -- '--platform'; then
  SAVE_PLATFORM=1
fi

log "loading ${#refs[@]} images into kind node '$CLUSTER_NAME' (platform $PLATFORM)"
missing=0
for ref in "${refs[@]}"; do
  if docker image inspect "$ref" >/dev/null 2>&1; then
    log "cache -> node: $ref"
    if [ "$SAVE_PLATFORM" = 1 ]; then
      docker save --platform "$PLATFORM" "$ref" \
        | kind load image-archive /dev/stdin --name "$CLUSTER_NAME"
    else
      kind load docker-image "$ref" --name "$CLUSTER_NAME"
    fi
  else
    warn "not in Docker cache: $ref"
    missing=$((missing+1))
  fi
done
[ "$missing" -eq 0 ] || die "$missing image(s) missing from the Docker cache; run 'make images-pull'"
log "done"
